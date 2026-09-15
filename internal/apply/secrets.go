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

// forSpec returns a snapshot of every secret producer registered for key,
// suitable for assigning straight to a resource.Spec's Secrets field.
//
// A fresh map, never a reference to the index's own storage: a caller that
// stores the result on a long-lived Spec must not see it mutate out from
// under it if another goroutine registers a secret for the same binding
// afterwards, and the index itself must not be corrupted by a caller that
// (incorrectly) wrote into what it got back.
func (s *secretIndex) forSpec(key bindingKey) map[string]resource.Secret {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.byBinding[key]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]resource.Secret, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
