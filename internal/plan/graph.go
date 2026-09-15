package plan

import (
	"sort"
	"strconv"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// groupKey identifies one expansion group: the (service, binding) pair a
// single Registry.Resolve call's results share — expandCompute resolves
// one group per service (Binding == ServiceKey there), expandBinding one
// group per manifest binding. A resource.Registration.DependsOn key
// resolves only within its own item's group, never across the whole
// manifest: see that field's doc comment for why (a service's Lambda
// permission depends on that service's own function, not every function
// in the manifest).
type groupKey struct {
	serviceKey string
	binding    string
}

// computeWaves assigns every item in items a Wave: the length of the
// longest chain of dependencies that must finish first, so that a node
// with no dependencies gets wave 0 and every other node gets one more
// than the largest wave among the things it depends on.
//
// Two kinds of edges are resolved, both into the same graph:
//
//   - Type edges: each item's own resource.Registration.DependsOn,
//     resolved against the other items in its own group (see groupKey).
//     A dependency naming a type this group never planned (because it was
//     filtered out by When/Triggers/SelectedBy, or because this
//     registration is simply requesting a type another provider
//     supplies) contributes no edge — there is no node for it to point
//     at, which is correct: a filtered-out registration contributes no
//     node and no edge to anything.
//   - Service edges: serviceDependsOn (manifest.Service.DependsOn,
//     already validated to name real, non-self services at load time)
//     connects every item belonging to a named service to every item
//     belonging to each service it depends on.
//
// Kahn's algorithm, run in layers rather than one node at a time: a node
// enters the current layer exactly when every dependency it has has
// already been assigned an earlier layer, which is the standard
// longest-path-via-BFS-levels property of a topological sort — the layer
// a node lands in does not depend on the order nodes are visited within a
// layer, only on the graph's actual shape, so wave assignment is
// deterministic for a given manifest regardless of map iteration order
// upstream.
//
// Cycle detection is a side effect of the algorithm terminating early
// (Kahn's own well-known property: len(sorted) < len(nodes) if and only
// if a cycle exists), not a separate pass. When it happens the returned
// error names every resource still unresolved when the algorithm
// stalled — not a search for the cycle's minimal subset, but the full set
// of items that could not be ordered, which by construction includes
// every item that actually took part in the cycle plus anything that
// itself, transitively, depended on one of them.
func computeWaves(items []plannedItem, serviceDependsOn map[string][]string) ([]int, error) {
	n := len(items)
	waves := make([]int, n)
	if n == 0 {
		return waves, nil
	}

	adj, indegree := buildGraph(items, serviceDependsOn)

	var frontier []int
	for i, d := range indegree {
		if d == 0 {
			frontier = append(frontier, i)
		}
	}
	sort.Ints(frontier)

	assigned := 0
	wave := 0
	for len(frontier) > 0 {
		var next []int
		for _, i := range frontier {
			waves[i] = wave
			assigned++
			for _, j := range adj[i] {
				indegree[j]--
				if indegree[j] == 0 {
					next = append(next, j)
				}
			}
		}
		sort.Ints(next)
		frontier = next
		wave++
	}

	if assigned < n {
		return nil, cycleError(items, indegree)
	}
	return waves, nil
}

// buildGraph resolves every edge computeWaves needs into an adjacency list
// (adj[i] is every node that depends directly on i) and each node's
// indegree (how many unresolved dependencies it still has).
func buildGraph(items []plannedItem, serviceDependsOn map[string][]string) (adj [][]int, indegree []int) {
	n := len(items)
	adj = make([][]int, n)
	indegree = make([]int, n)

	// byGroup resolves a DependsOn key to a concrete item index, scoped to
	// the (service, binding) group that produced it — see groupKey's doc
	// comment. byService resolves a manifest depends_on service name to
	// every item that service expanded to.
	byGroup := make(map[groupKey]map[string]int, n)
	byService := make(map[string][]int, n)
	for i, it := range items {
		gk := groupKey{it.ServiceKey, it.Binding}
		byType, ok := byGroup[gk]
		if !ok {
			byType = make(map[string]int)
			byGroup[gk] = byType
		}
		byType[it.ref.Key()] = i
		byService[it.ServiceKey] = append(byService[it.ServiceKey], i)
	}

	addEdge := func(from, to int) {
		if from == to {
			return
		}
		adj[from] = append(adj[from], to)
		indegree[to]++
	}

	for i, it := range items {
		gk := groupKey{it.ServiceKey, it.Binding}
		for _, depKey := range it.dependsOn {
			if depIdx, ok := byGroup[gk][depKey]; ok {
				addEdge(depIdx, i)
			}
		}
	}

	for svc, deps := range serviceDependsOn {
		for _, dep := range deps {
			for _, from := range byService[dep] {
				for _, to := range byService[svc] {
					addEdge(from, to)
				}
			}
		}
	}

	return adj, indegree
}

// cycleError builds the loud, named error a stalled topological sort
// requires: every item still unresolved (indegree > 0, or indegree == 0
// but never reached — which cannot happen once assigned above, since a
// zero-indegree node is always placed in some layer — so in practice
// exactly the indegree > 0 set) is named, sorted for a deterministic
// message across runs.
func cycleError(items []plannedItem, indegree []int) error {
	var names []string
	for i, d := range indegree {
		if d > 0 {
			it := items[i]
			names = append(names, it.ref.Key()+" "+strconv.Quote(it.ref.Name)+
				" (service "+it.ServiceKey+", binding "+it.Binding+")")
		}
	}
	sort.Strings(names)
	return kerrors.Validation(
		"dependency cycle detected among %d resource(s): %s — check resource.Registration.DependsOn "+
			"and any depends_on entries naming these services",
		len(names), strings.Join(names, "; "))
}
