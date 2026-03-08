package dag

import (
	"fmt"
	"strings"
)

// DAG represents a directed acyclic graph of tasks.
type DAG struct {
	nodes map[int]bool
	edges map[int][]int // node -> dependencies (nodes it depends on)
}

// New creates an empty DAG.
func New() *DAG {
	return &DAG{
		nodes: make(map[int]bool),
		edges: make(map[int][]int),
	}
}

// AddNode adds a node to the graph.
func (d *DAG) AddNode(id int) {
	d.nodes[id] = true
}

// AddEdge adds a dependency: `from` depends on `to`.
func (d *DAG) AddEdge(from, to int) {
	d.edges[from] = append(d.edges[from], to)
}

// Tiers returns nodes grouped into execution tiers using Kahn's algorithm.
// Tier 0 = nodes with no dependencies, Tier N+1 = nodes whose deps are all in tiers <= N.
// Returns an error if the graph contains a cycle.
func (d *DAG) Tiers() ([][]int, error) {
	// Build in-degree map (counting only edges within the graph)
	inDegree := make(map[int]int)
	// Reverse adjacency: who depends on me
	dependents := make(map[int][]int)

	for id := range d.nodes {
		inDegree[id] = 0
	}
	for from, deps := range d.edges {
		if !d.nodes[from] {
			continue
		}
		for _, to := range deps {
			if !d.nodes[to] {
				continue
			}
			inDegree[from]++
			dependents[to] = append(dependents[to], from)
		}
	}

	var tiers [][]int
	remaining := len(d.nodes)

	for remaining > 0 {
		// Collect nodes with zero in-degree
		var tier []int
		for id, deg := range inDegree {
			if deg == 0 {
				tier = append(tier, id)
			}
		}

		if len(tier) == 0 {
			return nil, &CycleError{Nodes: d.findCycle()}
		}

		tiers = append(tiers, tier)

		// Remove processed nodes and update in-degrees
		for _, id := range tier {
			delete(inDegree, id)
			remaining--
			for _, dep := range dependents[id] {
				inDegree[dep]--
			}
		}
	}

	return tiers, nil
}

// findCycle finds and returns nodes involved in a cycle (for error reporting).
func (d *DAG) findCycle() []int {
	visited := make(map[int]int) // 0=unvisited, 1=in-stack, 2=done
	parent := make(map[int]int)
	var cycle []int

	var dfs func(node int) bool
	dfs = func(node int) bool {
		visited[node] = 1
		for _, dep := range d.edges[node] {
			if !d.nodes[dep] {
				continue
			}
			if visited[dep] == 1 {
				// Found cycle — trace back
				cycle = []int{dep, node}
				for cur := node; cur != dep; {
					cur = parent[cur]
					if cur == dep {
						break
					}
					cycle = append(cycle, cur)
				}
				return true
			}
			if visited[dep] == 0 {
				parent[dep] = node
				if dfs(dep) {
					return true
				}
			}
		}
		visited[node] = 2
		return false
	}

	for id := range d.nodes {
		if visited[id] == 0 {
			if dfs(id) {
				return cycle
			}
		}
	}
	return nil
}

// CycleError indicates a cycle was detected in the DAG.
type CycleError struct {
	Nodes []int
}

func (e *CycleError) Error() string {
	parts := make([]string, len(e.Nodes))
	for i, n := range e.Nodes {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("cycle detected: %s", strings.Join(parts, " → "))
}
