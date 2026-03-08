package cli

import (
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/planner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestComputeTiers(t *testing.T) {
	plan := &agent.Plan{
		BatchName: "Test",
		Tasks: []agent.PlannedTask{
			{Title: "A", DependsOn: []int{}},
			{Title: "B", DependsOn: []int{}},
			{Title: "C", DependsOn: []int{0, 1}},
		},
	}

	tiers, err := computeTiers(plan)
	require.NoError(t, err)
	assert.Len(t, tiers, 2)
	assert.Len(t, tiers[0], 2) // A and B in Tier 0
	assert.Len(t, tiers[1], 1) // C in Tier 1
}

func TestComputeTiers_Cycle(t *testing.T) {
	plan := &agent.Plan{
		BatchName: "Cyclic",
		Tasks: []agent.PlannedTask{
			{Title: "A", DependsOn: []int{1}},
			{Title: "B", DependsOn: []int{0}},
		},
	}

	_, err := computeTiers(plan)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestEditPlanYAMLRoundTrip(t *testing.T) {
	plan := agent.Plan{
		BatchName: "Test Feature",
		Tasks: []agent.PlannedTask{
			{
				Title:                   "Task 1",
				Description:             "Do something",
				ImplementationDetails:   "Build it like this",
				AcceptanceCriteria:      []string{"It works"},
				Scope:                   []string{"file.go"},
				Conventions:             []string{"Use testify"},
				ContextFromDependencies: []string{"Dep provides X"},
				Complexity:              "medium",
				Type:                    "feature",
				DependsOn:               []int{},
			},
		},
	}

	data, err := yaml.Marshal(plan)
	require.NoError(t, err)

	var roundTripped agent.Plan
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	assert.Equal(t, plan.BatchName, roundTripped.BatchName)
	assert.Equal(t, plan.Tasks[0].Title, roundTripped.Tasks[0].Title)
	assert.Equal(t, plan.Tasks[0].ImplementationDetails, roundTripped.Tasks[0].ImplementationDetails)
	assert.Equal(t, plan.Tasks[0].Conventions, roundTripped.Tasks[0].Conventions)
	assert.Equal(t, plan.Tasks[0].ContextFromDependencies, roundTripped.Tasks[0].ContextFromDependencies)
}

func TestPrintDryRun(t *testing.T) {
	// Just verify it doesn't panic
	plan := &agent.Plan{
		BatchName: "Test",
		Tasks: []agent.PlannedTask{
			{Title: "A", Complexity: "low", DependsOn: []int{}},
			{Title: "B", Complexity: "high", DependsOn: []int{0}},
		},
	}
	tiers := [][]int{{0}, {1}}
	printDryRun(plan, tiers)
}

func TestSlugifyUsedInBatchBranch(t *testing.T) {
	// Verify the batch branch format matches expectations
	slug := planner.Slugify("Add JWT authentication")
	branch := "herd/batch/5-" + slug
	assert.Equal(t, "herd/batch/5-add-jwt-authentication", branch)
}
