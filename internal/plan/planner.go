package plan

import (
	"context"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/naming"
	"github.com/evatt-labs/kraai/internal/resource"
)

// defaultConcurrency bounds Get calls within one phase when the caller
// does not set one explicitly (D13: never one goroutine per resource
// unbounded, but also no reason to hardcode one number for every
// provider's rate limits). Modest on purpose — plan runs against live
// provider APIs, and a first-time caller should not have to discover a
// sane limit by getting rate-limited.
const defaultConcurrency = 10

// getter is the only capability Plan needs from a resource.Resource.
//
// Every resource.Resource satisfies getter, so assigning one into a
// getter-typed field is ordinary Go interface narrowing — but once stored
// that way, nothing in this package can call Create, Update, or Delete on
// it without first writing an explicit type assertion back to
// resource.Resource. That is the structural guarantee the package doc
// promises: not a rule this code happens to follow, but one the type
// checker enforces.
type getter interface {
	Get(ctx context.Context, ref resource.Ref) (*resource.State, error)
}

// Planner computes plans against a fixed registry.
type Planner struct {
	registry    *resource.Registry
	concurrency int
}

// Option configures a Planner.
type Option func(*Planner)

// WithConcurrency sets the maximum number of Get calls in flight at once
// within a single phase. Non-positive values are ignored, leaving the
// default in place, since zero or negative would either deadlock
// (errgroup.SetLimit(0) permits no goroutines at all) or mean "unlimited"
// (a negative value), both of which contradict D13's "never unbounded"
// and "sized" intent for the exact same reason — so this treats either as
// caller error to ignore rather than something to reject noisily on
// construction, matching the option pattern's no-error-return contract.
func WithConcurrency(n int) Option {
	return func(p *Planner) {
		if n > 0 {
			p.concurrency = n
		}
	}
}

// New builds a Planner against reg.
func New(reg *resource.Registry, opts ...Option) *Planner {
	p := &Planner{registry: reg, concurrency: defaultConcurrency}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// plannedItem is one resource type a binding expanded to, before Get has
// run — everything decide needs, with the resource narrowed to getter (see
// the type's own doc).
type plannedItem struct {
	Item
	ref  resource.Ref
	spec resource.Spec
	res  getter
}

// Plan walks m's services and reports what would happen to every resource
// type every declared binding expands to, without changing anything.
//
// environmentName is threaded through separately from m because
// manifest.Environment carries no name of its own — the same pattern
// internal/lockfile's MergeResources already uses for the same reason.
//
// The returned error is non-nil only when the walk itself could not be
// built at all (a binding's capability has no configured provider, or the
// registry has nothing for that capability/vendor pair) — a configuration
// problem, not a live one. A live failure reading one resource's current
// state never fails this call; it is reported as an ActionFailed entry in
// the returned Plan instead. See the package doc's "Partial failure"
// section.
func (p *Planner) Plan(ctx context.Context, m *manifest.Manifest, environmentName string) (*Plan, error) {
	if m == nil {
		return nil, kerrors.Validation("plan: manifest is nil")
	}
	if environmentName == "" {
		return nil, kerrors.Validation("plan: environment name must not be empty")
	}

	items, err := p.expand(m, environmentName)
	if err != nil {
		return nil, err
	}

	byPhase := make(map[resource.Phase][]plannedItem, len(resource.Phases()))
	for _, it := range items {
		byPhase[it.Phase] = append(byPhase[it.Phase], it)
	}

	// Phases run in sequence (D31); every phase runs regardless of whether
	// an earlier one had failures, so one unreachable resource never hides
	// the answers for every other one (see the package doc).
	var actions []Action
	for _, phase := range resource.Phases() {
		group := byPhase[phase]
		if len(group) == 0 {
			continue
		}
		actions = append(actions, p.getPhase(ctx, group)...)
	}

	// A cancelled run is not a plan. Every Get honours ctx, so cancelling
	// mid-walk leaves an Action per resource saying it could not be read —
	// which renders as a wall of failures and reads as "your infrastructure
	// is unreachable" rather than "you pressed Ctrl-C". Report the
	// cancellation instead; there is no partial plan worth showing, because
	// the reader cannot tell which entries are real.
	if err := ctx.Err(); err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "planning was cancelled")
	}
	return &Plan{Actions: actions}, nil
}

// expand walks every service's declared bindings in a deterministic order
// and returns one plannedItem per resource type each binding expands to.
func (p *Planner) expand(m *manifest.Manifest, environmentName string) ([]plannedItem, error) {
	var out []plannedItem

	for _, svcKey := range sortedKeys(m.Services) {
		svc := m.Services[svcKey]

		for _, d := range svc.Databases {
			config := map[string]any{"engine": d.Engine}
			if d.Caching != nil {
				config["caching"] = *d.Caching
			}
			items, err := p.expandBinding(m, environmentName, svcKey, d.Binding, manifest.CapabilityPostgres, config)
			if err != nil {
				return nil, annotate(err, svcKey, "databases", d.Binding)
			}
			out = append(out, items...)
		}
		for _, kv := range svc.KeyValue {
			items, err := p.expandBinding(m, environmentName, svcKey, kv.Binding, manifest.CapabilityKeyValue, nil)
			if err != nil {
				return nil, annotate(err, svcKey, "keyvalue", kv.Binding)
			}
			out = append(out, items...)
		}
		for _, o := range svc.Objects {
			items, err := p.expandBinding(m, environmentName, svcKey, o.Binding, manifest.CapabilityObjects, nil)
			if err != nil {
				return nil, annotate(err, svcKey, "objects", o.Binding)
			}
			out = append(out, items...)
		}
		for _, q := range svc.Queues {
			items, err := p.expandBinding(m, environmentName, svcKey, q.Binding, manifest.CapabilityQueues,
				map[string]any{"consumer": q.Consumer})
			if err != nil {
				return nil, annotate(err, svcKey, "queues", q.Binding)
			}
			out = append(out, items...)
		}
	}
	return out, nil
}

// annotate names the manifest path a binding-expansion failure came from,
// so a validation error points at the entry to fix rather than just the
// underlying registry complaint.
func annotate(err error, svcKey, kind, binding string) error {
	return kerrors.Wrap(err, kerrors.CodeValidation, "services.%s.%s.%s", svcKey, kind, binding)
}

// expandBinding resolves one binding's capability to a vendor, expands
// that pair into every resource type it produces (D30), and derives each
// one's Ref and Spec.
func (p *Planner) expandBinding(
	m *manifest.Manifest, environmentName, svcKey, binding, capability string, config map[string]any,
) ([]plannedItem, error) {
	provider, ok := m.Root.Providers.For(capability)
	if !ok {
		return nil, kerrors.Validation("no provider is configured for capability %q", capability)
	}

	// Resolve returns every type the vendor choice implies, across providers:
	// a Postgres binding on Neon reaches both the branch and the Cloudflare
	// Hyperdrive configuration fronting it, because the registry keys this by
	// vendor rather than by which API creates each piece.
	regs, err := p.registry.Resolve(capability, provider.Vendor)
	if err != nil {
		return nil, err
	}

	name := naming.ResourceName(environmentName, svcKey, binding)

	out := make([]plannedItem, 0, len(regs))
	for _, r := range regs {
		out = append(out, plannedItem{
			Item: Item{
				ServiceKey: svcKey, Binding: binding, Capability: capability,
				Provider: r.Provider, Type: r.Type, Phase: r.Phase,
			},
			ref:  resource.Ref{Provider: r.Provider, Type: r.Type, Name: name},
			spec: resource.Spec{Binding: binding, Name: name, Config: config},
			res:  r.Resource,
		})
	}
	return out, nil
}

// getPhase runs Get for every item in one phase, bounded by p.concurrency,
// and returns one Action per item in the same order items was given in.
func (p *Planner) getPhase(ctx context.Context, items []plannedItem) []Action {
	actions := make([]Action, len(items))

	g := &errgroup.Group{}
	g.SetLimit(p.concurrency)
	for i, it := range items {
		g.Go(func() error {
			actions[i] = decide(ctx, it)
			// Deliberately always nil: a goroutine here must never cancel
			// or skip its siblings just because one Get failed. That
			// failure is already captured in actions[i] as ActionFailed;
			// letting it propagate through the group would only matter if
			// this errgroup used a derived, cancelable context, which it
			// does not (see the package doc's "Partial failure" section).
			return nil
		})
	}
	_ = g.Wait()

	return actions
}

// decide runs Get for one item and turns the result into an Action.
func decide(ctx context.Context, it plannedItem) Action {
	action := Action{Item: it.Item, Ref: it.ref, Spec: it.spec}

	state, err := it.res.Get(ctx, it.ref)
	if err != nil {
		action.Kind = ActionFailed
		action.Err = kerrors.Wrap(err, kerrors.CodeUnexpected, "reading %s/%s %q", it.Provider, it.Type, it.ref.Name)
		return action
	}
	action.Current = state

	if state == nil {
		action.Kind = ActionCreate
		return action
	}

	// A type assertion checks it.res's dynamic type, not its static
	// interface (getter) — so this reaches ImmutableDiffer on the
	// underlying resource.Resource without this package ever holding a
	// value statically typed as resource.Resource itself. ImmutableDiffer
	// has no mutating method to reach even if it did.
	if differ, ok := it.res.(ImmutableDiffer); ok {
		differs, dErr := differ.DiffersFromState(it.spec, state)
		if dErr != nil {
			action.Kind = ActionFailed
			action.Err = kerrors.Wrap(dErr, kerrors.CodeUnexpected,
				"comparing %s/%s %q to its desired spec", it.Provider, it.Type, it.ref.Name)
			return action
		}
		if differs {
			action.Kind = ActionReplace
			return action
		}
	}

	action.Kind = ActionNoChange
	return action
}

// sortedKeys returns m's keys in ascending order, so iterating a manifest's
// services map — Go gives no ordering guarantee over a map — produces the
// same plan order on every run.
func sortedKeys(m map[string]manifest.Service) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
