package apply

import (
	"sync"

	"github.com/evatt-labs/kraai/internal/resource"
)

// bindingKey scopes a secret handoff to the manifest binding that produced
// it, independent of the provider/type of either end — see the package
// doc's "Secrets and outputs cross phases" section for why this, and not
// resource.Ref, is the correct scope.
type bindingKey struct {
	ServiceKey string
	Binding    string
}

// secretIndex is the binding-scoped credential handoff apply owns
// alongside resource.Outputs. Safe for concurrent use: actions within a
// phase run in parallel, and a producer in one phase must be visible to a
// consumer in the same or a later phase without either racing the other.
type secretIndex struct {
	mu        sync.RWMutex
	byBinding map[bindingKey]map[string]resource.Secret
}

func newSecretIndex() *secretIndex {
	return &secretIndex{byBinding: map[bindingKey]map[string]resource.Secret{}}
}

// put registers a secret producer under key, alongside whatever else has
// already been registered for the same binding.
func (s *secretIndex) put(key bindingKey, name string, secret resource.Secret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byBinding[key]
	if !ok {
		m = map[string]resource.Secret{}
		s.byBinding[key] = m
	}
	m[name] = secret
}

// forAction returns the union of every secret producer registered for
// svcKey across reads, suitable for assigning straight to a resource.Spec's
// Secrets field.
//
// reads is expected to already be resolved to its effective value (see
// effectiveReadsBindings in apply.go) — this method does not itself fall
// back to ownBinding when reads is empty, so an action with genuinely no
// readable bindings genuinely sees no secrets.
//
// Namespacing follows the rule documented in this package's doc comment:
// secrets from ownBinding keep their bare names — the case every existing
// resource (a Hyperdrive configuration reading its own branch's
// connection_uri) relies on and must see unchanged — while secrets from any
// other binding in reads appear as "<binding>.<name>", so a compute action
// reading two sibling bindings that both happen to produce a same-named
// secret (e.g. two databases each producing "connection_uri") cannot
// collide: at most one binding may ever claim the bare name, and every
// other binding's copy is keyed by a prefix unique to it.
//
// A fresh map, never a reference to the index's own storage: a caller that
// stores the result on a long-lived Spec must not see it mutate out from
// under it if another goroutine registers a secret for the same binding
// afterwards, and the index itself must not be corrupted by a caller that
// (incorrectly) wrote into what it got back.
func (s *secretIndex) forAction(svcKey, ownBinding string, reads []string) map[string]resource.Secret {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out map[string]resource.Secret
	for _, binding := range reads {
		src := s.byBinding[bindingKey{ServiceKey: svcKey, Binding: binding}]
		if len(src) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]resource.Secret, len(src))
		}
		for name, secret := range src {
			key := name
			if binding != ownBinding {
				key = binding + "." + name
			}
			out[key] = secret
		}
	}
	return out
}
