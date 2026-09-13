package plugin

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// DefaultPoolSize is used for a Spec that doesn't set PoolSize.
const DefaultPoolSize = 4

// Provision names one capability a plugin implements: Key is whatever a
// caller registers it under in a Registry (this package places no
// constraint on its shape — see Registry), and Export is the plugin's own
// exported function implementing it, which must match the provision
// signature documented in doc.go or the plugin fails to load.
type Provision struct {
	Key    string
	Export string
}

// Spec describes one plugin to load. Grants and Provides are both
// explicit and exhaustive: a plugin gets exactly the host capabilities
// named in Grants and nothing else (D16), and only the exports named in
// Provides are ever considered for registration, regardless of what else
// the module happens to export.
type Spec struct {
	// Name identifies this plugin in warnings, pooled-instance names, and
	// as the "source" a Registry records for its registrations.
	Name string
	// Path is the module's .wasm file, resolved however the FS passed to
	// Host.Load resolves it — see Host.Load's doc comment for why
	// resolution is deliberately not this package's concern.
	Path string
	// Grants lists host capability names (Capability.Name()) this plugin
	// may import from HostNamespace. Importing anything else from that
	// namespace fails module instantiation.
	Grants []string
	// Provides lists the capability keys this plugin registers and the
	// exported function implementing each.
	Provides []Provision
	// PoolSize is how many goroutine-safe module instances to keep ready
	// for concurrent Invoke calls (D29: instances are not goroutine-safe,
	// pool them). Size it against docs/BLUEPRINT.md D13's global
	// concurrency limit. Defaults to DefaultPoolSize when <= 0.
	PoolSize int
}

// Host is the shared, process-lifetime state behind every loaded plugin:
// the on-disk compilation cache (D29) and the universe of host
// capabilities available to grant. Each Plugin loaded from a Host still
// gets its own private wazero.Runtime — see doc.go's "Compilation and
// pooling" section for why that's the isolation boundary, not Host
// itself.
type Host struct {
	cache        wazero.CompilationCache
	capabilities map[string]Capability
}

// NewHost builds a Host backed by an on-disk compilation cache rooted at
// cacheDir (created if absent) and the given host capabilities, available
// to be granted to a plugin via Spec.Grants. cacheDir must be
// durable across process runs — an ephemeral temp directory defeats D29's
// entire point, since the in-memory alternative (wazero.NewCompilationCache)
// already covers the case where persistence doesn't matter, and is
// deliberately not what this constructor uses.
func NewHost(cacheDir string, capabilities ...Capability) (*Host, error) {
	cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "opening plugin compilation cache at %s", cacheDir)
	}
	known := make(map[string]Capability, len(capabilities))
	for _, c := range capabilities {
		known[c.Name()] = c
	}
	return &Host{cache: cache, capabilities: known}, nil
}

// Close releases the shared compilation cache. It does not close any
// Plugin loaded from this Host — a Plugin's runtime and pooled instances
// outlive Host's own bookkeeping and must be closed individually.
func (h *Host) Close(ctx context.Context) error {
	return h.cache.Close(ctx)
}

// Load compiles and instantiates the plugin described by spec, reading
// its bytes from fsys.
//
// Resolving a manifest plugins: entry (docs/BLUEPRINT.md D4) to a
// filesystem path is deliberately kept out of this package: fsys already
// encapsulates that. A bare filename ("kraai-plugin-example.wasm") and a
// relative path ("./plugins/cost-guard.wasm") are both just strings
// ReadFile resolves however the caller's FS resolves them — this package
// never fetches a plugin from a registry or the network itself. That's a
// deliberate scope decision: a package-reference resolver (fetch-by-
// name-and-version, checksum pinning, a cache directory, a registry API)
// is real design surface of its own, and D3's dependency-weighing logic
// applies just as much to a home-grown fetch mechanism as to a third-party
// one — building it hastily in a binary that holds cloud credentials is a
// worse trade than not building it yet. Every plugins: entry today names
// a local .wasm file; a real registry resolver, if one is ever built,
// slots in as its own FS implementation without this package changing.
func (h *Host) Load(ctx context.Context, fsys FS, spec Spec) (*Plugin, error) {
	if spec.Name == "" {
		return nil, kerrors.Validation("plugin spec has no Name")
	}
	if spec.Path == "" {
		return nil, kerrors.Validation("plugin %q has no Path", spec.Name)
	}
	poolSize := spec.PoolSize
	if poolSize <= 0 {
		poolSize = DefaultPoolSize
	}

	wasmBytes, err := fsys.ReadFile(spec.Path)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "reading plugin %q at %s", spec.Name, spec.Path)
	}

	granted, err := h.grantedCapabilities(spec)
	if err != nil {
		return nil, err
	}

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(h.cache))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "instantiating WASI for plugin %q", spec.Name)
	}
	if len(granted) > 0 {
		if err := instantiateHostModule(ctx, rt, granted); err != nil {
			_ = rt.Close(ctx)
			return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "wiring host capabilities for plugin %q", spec.Name)
		}
	}

	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "compiling plugin %q", spec.Name)
	}

	p := &Plugin{name: spec.Name, runtime: rt, compiled: compiled, provides: spec.Provides}
	if err := p.validateABI(ctx); err != nil {
		_ = p.Close(ctx)
		return nil, err
	}
	if err := p.fillPool(ctx, poolSize); err != nil {
		_ = p.Close(ctx)
		return nil, err
	}
	return p, nil
}

// grantedCapabilities resolves spec.Grants against h.capabilities,
// failing loudly if a plugin asks for a capability the Host was never
// given.
func (h *Host) grantedCapabilities(spec Spec) ([]Capability, error) {
	granted := make([]Capability, 0, len(spec.Grants))
	for _, name := range spec.Grants {
		capa, ok := h.capabilities[name]
		if !ok {
			return nil, kerrors.Validation("plugin %q grants unknown capability %q", spec.Name, name)
		}
		granted = append(granted, capa)
	}
	return granted, nil
}

// instantiateHostModule registers exactly granted's capabilities under
// HostNamespace on rt — the only host functions a plugin instantiated on
// rt can ever resolve an import against. A plugin importing a name not in
// this exact list fails wazero's own instantiation-time import
// resolution; there is no runtime permission check to bypass because
// there is nothing to bypass — the function simply does not exist in this
// runtime's host module.
func instantiateHostModule(ctx context.Context, rt wazero.Runtime, granted []Capability) error {
	builder := rt.NewHostModuleBuilder(HostNamespace)
	for _, capa := range granted {
		builder.NewFunctionBuilder().
			WithGoModuleFunction(hostCapabilityFunc(capa), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).
			WithParameterNames("ptr", "len").
			Export(capa.Name())
	}
	_, err := builder.Instantiate(ctx)
	return err
}

// hostCapabilityFunc adapts capa into the (ptr,len)->i64 shape every host
// capability import shares (doc.go). mod, supplied by wazero, is the
// *calling* plugin's module: its memory holds the request capa reads and
// is where the response is written back, via the plugin's own
// kraai_alloc, exactly as a provision call's input is placed (host.go
// never assumes it may write into guest memory it didn't first ask the
// guest to allocate).
//
// A guest-side ABI violation here (a bad ptr/len for its own claimed
// request, a missing/failing kraai_alloc) panics rather than returning a
// value: per wazero's own documented convention (RATIONALE.md, "why panic
// with sys.ExitError after a host function exits"), panicking is the
// portable way for a host function to abort a call outright, and wazero
// recovers it into an error at the exported function's Call() boundary —
// there is no safe envelope to hand back to a module that cannot be
// trusted to have allocated its own request/response memory correctly.
func hostCapabilityFunc(capa Capability) api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		// stack holds this function's declared i32 params zero-extended
		// into uint64 slots (wazero's calling convention for every value
		// type); truncating back to uint32 recovers the exact i32 value,
		// it never discards real bits.
		ptr, length := uint32(stack[0]), uint32(stack[1]) //nolint:gosec // see comment above
		input, err := readRegion(mod.Memory(), ptr, length)
		if err != nil {
			panic(kerrors.Wrap(err, kerrors.CodeValidation, "plugin %q called %s with an invalid request region", mod.Name(), capa.Name()))
		}

		output, invokeErr := capa.Invoke(ctx, input)
		status := StatusOK
		payload := output
		if invokeErr != nil {
			status = StatusError
			payload = []byte(invokeErr.Error())
		}

		packed, err := placeEnvelope(ctx, mod, status, payload)
		if err != nil {
			panic(kerrors.Wrap(err, kerrors.CodeValidation, "plugin %q could not receive the %s response", mod.Name(), capa.Name()))
		}
		stack[0] = packed
	})
}

// placeInGuestMemory calls mod's own kraai_alloc to obtain a region large
// enough for data, then writes data into it, returning the region's
// pointer. It never writes into guest memory it didn't first ask the
// guest to allocate.
func placeInGuestMemory(ctx context.Context, mod api.Module, data []byte) (uint32, error) {
	allocFn := mod.ExportedFunction(funcAlloc)
	if allocFn == nil {
		return 0, kerrors.Validation("plugin %q is missing required export %s", mod.Name(), funcAlloc)
	}
	results, err := allocFn.Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, kerrors.Wrap(err, kerrors.CodeUnexpected, "calling %s on plugin %q", funcAlloc, mod.Name())
	}
	// results[0] is kraai_alloc's declared i32 return zero-extended into a
	// uint64 slot (wazero's calling convention); the truncation below is
	// exact, and the result is still range-checked by writeRegion just
	// after, regardless.
	ptr := uint32(results[0]) //nolint:gosec // see comment above
	if err := writeRegion(mod.Memory(), ptr, data); err != nil {
		return 0, err
	}
	return ptr, nil
}

// placeEnvelope writes status and payload as one ABI envelope (doc.go)
// into guest memory obtained via placeInGuestMemory, returning the packed
// (ptr, len) result value.
func placeEnvelope(ctx context.Context, mod api.Module, status byte, payload []byte) (uint64, error) {
	buf := make([]byte, 0, len(payload)+1)
	buf = append(buf, status)
	buf = append(buf, payload...)
	ptr, err := placeInGuestMemory(ctx, mod, buf)
	if err != nil {
		return 0, err
	}
	// len(buf) is already <= MaxTransferBytes here: placeInGuestMemory
	// (just above) calls writeRegion, which rejects anything larger
	// before this line is ever reached, so the uint32 conversion never
	// truncates a real value.
	return pack(ptr, uint32(len(buf))), nil //nolint:gosec // see comment above
}
