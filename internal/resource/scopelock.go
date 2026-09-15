package resource

import "sync"

// ScopeLocker serializes mutating calls that share a Registration.Scope
// value, so two operations kraai itself issues concurrently for the same
// scope (see Registration.Scope's doc comment for the live 423 this
// prevents) never overlap, while operations in different scopes — or with
// no scope at all — are unaffected.
//
// # One shared implementation
//
// internal/apply and internal/destroy both need this: a phase's errgroup
// runs many actions concurrently in either package, and either package can
// issue a mutating call (Create/Delete) against a scoped registration. Both
// import internal/resource already, and ScopeLocker depends on nothing
// beyond sync — no plan, no manifest, no vendor package — so it lives here
// rather than as a new package one level up that both would have to import
// instead, or as two independent copies that would inevitably drift.
//
// # Lifetime: one per run, not a package-level singleton
//
// A caller constructs a new ScopeLocker for each Apply/Destroy call (via
// NewScopeLocker) rather than sharing one across the process. Locking
// within one run's set of concurrent goroutines is the whole requirement —
// nothing here needs to serialize two unrelated invocations against each
// other, and a per-process singleton would otherwise accumulate one
// *sync.Mutex per distinct scope string ever seen, for the life of the
// process, for scopes that will never be locked again once that run ends.
//
// # Deadlock safety
//
// Do takes at most one scope's lock for the duration of fn and releases it
// via defer, so it is released on every path out of fn — a normal return,
// an error return, a panic unwinding through Do, or fn returning because
// ctx was cancelled partway through. Since a caller only ever calls Do once
// per operation (see internal/apply's and internal/destroy's execute/mutate),
// no goroutine ever holds two scope locks at once, which rules out lock-
// ordering deadlocks by construction: there is only ever one lock to
// acquire, never a second to wait on while already holding the first.
type ScopeLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewScopeLocker builds an empty ScopeLocker, ready to guard one apply or
// destroy run.
func NewScopeLocker() *ScopeLocker {
	return &ScopeLocker{locks: make(map[string]*sync.Mutex)}
}

// Do runs fn while holding scope's lock, blocking until it is free. An
// empty scope means unscoped (Registration.Scope was nil, or its function
// returned ""): fn runs immediately with no lock at all, which is the
// common case and must not pay for synchronization it does not need.
//
// fn's own error is returned unchanged; Do adds no error of its own. A
// panic inside fn propagates out of Do exactly as it would without a lock
// — Do never recovers it — after the deferred unlock has already run, so
// the panic is observable to the caller instead of being swallowed here
// (see Rule 5/20: past defect hunts in this codebase have found silently
// swallowed exceptions, which this is written to not repeat).
func (l *ScopeLocker) Do(scope string, fn func() error) error {
	if scope == "" {
		return fn()
	}
	m := l.acquire(scope)
	defer m.Unlock()
	return fn()
}

// acquire returns scope's mutex, creating and locking it if this is the
// first request for scope, then blocks until it is held.
//
// The locks map itself is guarded by l.mu for the brief window needed to
// look up or insert scope's *sync.Mutex; that mutex is then locked outside
// l.mu's critical section, so a slow operation holding scope's lock never
// blocks an unrelated scope's lookup.
func (l *ScopeLocker) acquire(scope string) *sync.Mutex {
	l.mu.Lock()
	m, ok := l.locks[scope]
	if !ok {
		m = &sync.Mutex{}
		l.locks[scope] = m
	}
	l.mu.Unlock()

	m.Lock()
	return m
}
