package dag

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sortTiers(tiers [][]int) {
	for _, tier := range tiers {
		sort.Ints(tier)
	}
}

func TestParallelNoDeps(t *testing.T) {
	d := New()
	d.AddNode(1)
	d.AddNode(2)
	d.AddNode(3)

	tiers, err := d.Tiers()
	require.NoError(t, err)
	require.Len(t, tiers, 1)
	sortTiers(tiers)
	assert.Equal(t, []int{1, 2, 3}, tiers[0])
}

func TestLinearChain(t *testing.T) {
	d := New()
	d.AddNode(1)
	d.AddNode(2)
	d.AddNode(3)
	d.AddEdge(2, 1) // 2 depends on 1
	d.AddEdge(3, 2) // 3 depends on 2

	tiers, err := d.Tiers()
	require.NoError(t, err)
	require.Len(t, tiers, 3)
	assert.Equal(t, []int{1}, tiers[0])
	assert.Equal(t, []int{2}, tiers[1])
	assert.Equal(t, []int{3}, tiers[2])
}

func TestDiamond(t *testing.T) {
	// A → B, A → C, B → D, C → D
	d := New()
	d.AddNode(1) // A
	d.AddNode(2) // B
	d.AddNode(3) // C
	d.AddNode(4) // D
	d.AddEdge(2, 1) // B depends on A
	d.AddEdge(3, 1) // C depends on A
	d.AddEdge(4, 2) // D depends on B
	d.AddEdge(4, 3) // D depends on C

	tiers, err := d.Tiers()
	require.NoError(t, err)
	require.Len(t, tiers, 3)
	sortTiers(tiers)
	assert.Equal(t, []int{1}, tiers[0])
	assert.Equal(t, []int{2, 3}, tiers[1])
	assert.Equal(t, []int{4}, tiers[2])
}

func TestCycleDetection(t *testing.T) {
	d := New()
	d.AddNode(1)
	d.AddNode(2)
	d.AddEdge(1, 2)
	d.AddEdge(2, 1)

	_, err := d.Tiers()
	require.Error(t, err)

	var cycleErr *CycleError
	assert.ErrorAs(t, err, &cycleErr)
	assert.Contains(t, err.Error(), "cycle detected")
}

func TestSingleNode(t *testing.T) {
	d := New()
	d.AddNode(42)

	tiers, err := d.Tiers()
	require.NoError(t, err)
	require.Len(t, tiers, 1)
	assert.Equal(t, []int{42}, tiers[0])
}

func TestEmptyGraph(t *testing.T) {
	d := New()
	tiers, err := d.Tiers()
	require.NoError(t, err)
	assert.Len(t, tiers, 0)
}

func TestMixedTiers(t *testing.T) {
	// 1, 2 independent; 3 depends on 1; 4 depends on 1 and 2; 5 depends on 3 and 4
	d := New()
	for i := 1; i <= 5; i++ {
		d.AddNode(i)
	}
	d.AddEdge(3, 1)
	d.AddEdge(4, 1)
	d.AddEdge(4, 2)
	d.AddEdge(5, 3)
	d.AddEdge(5, 4)

	tiers, err := d.Tiers()
	require.NoError(t, err)
	require.Len(t, tiers, 3)
	sortTiers(tiers)
	assert.Equal(t, []int{1, 2}, tiers[0])
	assert.Equal(t, []int{3, 4}, tiers[1])
	assert.Equal(t, []int{5}, tiers[2])
}
