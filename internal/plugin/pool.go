package plugin

import (
	"context"

	"github.com/tetratelabs/wazero/api"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// pool is a bounded, blocking free-list of goroutine-safe module
// instances (D29: wazero module instances are not themselves
// goroutine-safe, so concurrent Invoke calls must never share one). A
// buffered channel gives both the free-list and the concurrency bound in
// one primitive: get blocks when every instance is checked out, which is
// exactly the backpressure docs/BLUEPRINT.md D13 asks a plugin's own
// concurrency to respect.
type pool struct {
	free chan api.Module
	all  []api.Module
}

func newPool(instances []api.Module) *pool {
	free := make(chan api.Module, len(instances))
	for _, m := range instances {
		free <- m
	}
	return &pool{free: free, all: instances}
}

// get borrows an instance, blocking until one is free or ctx is done.
func (p *pool) get(ctx context.Context) (api.Module, error) {
	select {
	case m := <-p.free:
		return m, nil
	case <-ctx.Done():
		return nil, kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected, "waiting for a free plugin instance")
	}
}

// put returns a borrowed instance to the pool.
func (p *pool) put(m api.Module) {
	p.free <- m
}

// closeAll closes every instance the pool owns, regardless of whether
// it's currently checked out — called only from Plugin.Close, once no
// caller should still be invoking this plugin.
func (p *pool) closeAll(ctx context.Context) {
	for _, m := range p.all {
		_ = m.Close(ctx)
	}
}
