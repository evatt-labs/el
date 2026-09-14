// Package deployconfig builds the wrangler configuration actually deployed
// for an ephemeral environment, from the service's committed one.
//
// # Why this is not a mutation
//
// The first version of kraai edited the loaded configuration in place: it set
// name, vars and hyperdrive and left everything else alone. Every other
// binding declared in wrangler.jsonc — D1, KV, R2, queues, Durable Objects,
// service bindings — was therefore deployed to the ephemeral Worker verbatim,
// pointed at the same production resources the real deployment uses. A
// disposable preview environment silently held live read and write access to
// production data for every binding except the one already being managed
// explicitly. A security audit caught it; nothing about it was intentional.
//
// So the deployed configuration is assembled from an explicit allowlist
// instead of inherited by default. A key is carried forward only if it is
// structural (safeStructuralKeys), or it binds to an external resource and a
// freshly provisioned substitute has been supplied for it, or the service has
// opted in by name. Anything else is dropped.
//
// Allowlisting is what makes this safe against Cloudflare adding binding
// types faster than kraai learns about them: an unrecognised key is dropped
// rather than inherited, so a new kind of stateful binding cannot leak
// production by default just because this package has not heard of it yet.
package deployconfig

import (
	"sort"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/wrangler"
)

// safeStructuralKeys describe the deploy itself rather than an external
// resource, so they carry forward untouched.
var safeStructuralKeys = []string{
	"$schema",
	"main",
	"compatibility_date",
	"compatibility_flags",
	"observability",
	"account_id",
	"node_compat",
	// A Durable Object class lives inside the Worker script being deployed,
	// not in a separately provisioned resource: a fresh Worker name under a
	// new environment gets fresh Durable Object storage automatically. There
	// is nothing to provision and nothing shared with production to leak.
	// migrations travels with durable_objects because both describe this
	// deploy's own script rather than anything external.
	"durable_objects",
	"migrations",
}

// statefulBindingKeys bind to a real external resource. They are carried
// forward only when a fresh environment-scoped substitute has been provided,
// or the service has explicitly opted in.
var statefulBindingKeys = []string{
	"d1_databases",
	"kv_namespaces",
	"r2_buckets",
	"queues",
	"ai",
	"vectorize",
	"browser",
	"services",
	"mtls_certificates",
	"dispatch_namespaces",
	"analytics_engine_datasets",
	"send_email",
	"workflows",
	"images",
	"pipelines",
	"secrets_store_secrets",
	"unsafe",
	// Added against Cloudflare's current configuration reference rather than
	// inherited from the JavaScript's list, which had drifted. Each binds to
	// something outside the deployed script:
	//   ai_search / ai_search_namespaces — account-level search indexes
	//   tail_consumers                   — another Worker, which would then
	//                                      receive this ephemeral Worker's logs
	//   containers                       — account-level container images
	// Without these they fell through to "unrecognised key" and were dropped,
	// which was safe but told the reader nothing about why their binding had
	// vanished.
	"ai_search",
	"ai_search_namespaces",
	"tail_consumers",
	"containers",
}

// neverInheritedKeys are dropped unconditionally, opt-in or not.
//
// Reassigning a route or a cron trigger during an ephemeral deploy is a
// sharper failure than an ephemeral Worker reading production data: it can
// redirect real production traffic to a disposable Worker that is about to be
// deleted.
var neverInheritedKeys = []string{"routes", "route", "triggers"}

// Options are the environment-specific parts of a deployed configuration.
type Options struct {
	// Name is the generated Worker name for this environment.
	Name string
	// Vars are the plain Worker variables to deploy with.
	Vars map[string]any
	// Hyperdrive, when non-nil, is the hyperdrive binding block.
	Hyperdrive any
	// UnsafeInheritBindings carries production bindings forward. Named
	// deliberately.
	UnsafeInheritBindings bool
	// Overrides are freshly provisioned, environment-scoped replacements,
	// keyed by the configuration key they replace.
	Overrides map[string]any
	// Warn reports something dropped. Optional.
	Warn func(format string, args ...any)
}

// Build assembles the configuration to deploy.
//
// It fails rather than inheriting when the committed configuration declares a
// stateful binding that has no provisioned substitute and no opt-in: silently
// deploying that binding is the exact failure this package exists to prevent,
// and silently dropping it would produce a Worker that fails at runtime with
// an unbound variable instead.
func Build(base wrangler.Config, opts Options) (wrangler.Config, error) {
	warn := opts.Warn
	if warn == nil {
		warn = func(string, ...any) {}
	}

	deployed := wrangler.Config{}
	for _, key := range safeStructuralKeys {
		if value, present := base[key]; present {
			deployed[key] = value
		}
	}

	// A stateful binding with a provisioned substitute needs no opt-in: there
	// is nothing production-pointing left to inherit once it is replaced.
	var inherited []string
	for _, key := range statefulBindingKeys {
		if _, present := base[key]; !present {
			continue
		}
		if _, overridden := opts.Overrides[key]; overridden {
			continue
		}
		inherited = append(inherited, key)
	}
	if len(inherited) > 0 {
		if !opts.UnsafeInheritBindings {
			return nil, kerrors.Validation(
				"wrangler.jsonc declares %s, which would deploy this ephemeral environment with "+
					"live access to those production resources. Declare a matching d1/kv/r2/queues "+
					"entry so kraai provisions a fresh one, or set unsafeInheritBindings on this "+
					"service if inheriting production is really what you want — the name is "+
					"deliberate.", strings.Join(inherited, ", "))
		}
		for _, key := range inherited {
			deployed[key] = base[key]
		}
	}

	for key, value := range opts.Overrides {
		deployed[key] = value
	}

	// Enforced after the overrides, not merely by omission from the allowlist.
	// Nothing supplies these today, but "no caller does this right now" is a
	// property of the callers, and this is the one guarantee in the package
	// that protects production traffic rather than production data.
	for _, key := range neverInheritedKeys {
		if _, present := deployed[key]; present {
			delete(deployed, key)
			warn("  (refusing to deploy %s — it can redirect real production traffic to a disposable Worker)", key)
		}
	}
	if dropped := intersect(base, neverInheritedKeys); len(dropped) > 0 {
		warn("  (dropping %s from the deployed config — never carried forward, even with "+
			"unsafeInheritBindings: reassigning a route or trigger during an ephemeral deploy "+
			"can redirect real production traffic)", strings.Join(dropped, ", "))
	}

	// Anything the allowlist does not recognise is dropped, which is safe but
	// invisible: a Worker referencing it fails at runtime with an unbound
	// variable and nothing explaining why. Saying so turns a confusing runtime
	// failure into a line of output at deploy time.
	if unknown := unknownKeys(base); len(unknown) > 0 {
		warn("  (ignoring unrecognised wrangler config key(s) %s — kraai deploys an explicit "+
			"allowlist, so anything it does not know about is left out rather than inherited)",
			strings.Join(unknown, ", "))
	}

	deployed["name"] = opts.Name
	if opts.Vars != nil {
		deployed["vars"] = opts.Vars
	} else {
		deployed["vars"] = map[string]any{}
	}
	if opts.Hyperdrive != nil {
		deployed["hyperdrive"] = opts.Hyperdrive
	} else if _, declared := base["hyperdrive"]; declared {
		// The committed config binds Hyperdrive but no database was
		// provisioned for this environment, so there is nothing to replace it
		// with. Dropping it is right — it points at production — but silently
		// dropping it leaves a Worker that fails on an unbound binding.
		warn("  (dropping hyperdrive from the deployed config — it points at your production " +
			"database and no environment database was provisioned to replace it)")
	}
	return deployed, nil
}

// intersect returns the keys of base that appear in keys, in order.
func intersect(base wrangler.Config, keys []string) []string {
	var found []string
	for _, key := range keys {
		if _, present := base[key]; present {
			found = append(found, key)
		}
	}
	return found
}

// unknownKeys returns base's keys that no list accounts for, plus the ones
// this package always sets itself, sorted so the message is stable.
func unknownKeys(base wrangler.Config) []string {
	known := map[string]struct{}{
		// Set explicitly by Build, so their presence in the base config is
		// expected rather than unrecognised.
		"name": {}, "vars": {}, "hyperdrive": {},
	}
	for _, list := range [][]string{safeStructuralKeys, statefulBindingKeys, neverInheritedKeys} {
		for _, key := range list {
			known[key] = struct{}{}
		}
	}

	var unknown []string
	for key := range base {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}
