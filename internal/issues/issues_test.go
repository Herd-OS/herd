package issues

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllLabels(t *testing.T) {
	labels := AllLabels()
	assert.Len(t, labels, 8)
}

func TestIsStatusLabel(t *testing.T) {
	assert.True(t, IsStatusLabel(StatusReady))
	assert.True(t, IsStatusLabel(StatusInProgress))
	assert.False(t, IsStatusLabel(TypeFeature))
	assert.False(t, IsStatusLabel("random"))
}

func TestHasLabel(t *testing.T) {
	labels := []string{StatusReady, TypeFeature}
	assert.True(t, HasLabel(labels, StatusReady))
	assert.False(t, HasLabel(labels, StatusDone))
}

func TestStatusLabel(t *testing.T) {
	assert.Equal(t, StatusReady, StatusLabel([]string{TypeFeature, StatusReady}))
	assert.Equal(t, "", StatusLabel([]string{TypeFeature}))
}

func TestRenderAndParseRoundTrip(t *testing.T) {
	original := IssueBody{
		FrontMatter: FrontMatter{
			Version:             1,
			Batch:               7,
			DependsOn:           []int{42, 43},
			Scope:               []string{"src/components/ThemeToggle.tsx", "src/styles/theme.css"},
			EstimatedComplexity: "medium",
		},
		Task:     "Create a theme toggle component.",
		Criteria: []string{"Component renders a toggle button", "Current theme persisted to localStorage"},
		Context:  "Part of dark mode feature.",
		FilesToModify: []string{"src/components/ThemeToggle.tsx", "src/components/index.ts"},
	}

	rendered := RenderBody(original)

	parsed, err := ParseBody(rendered)
	require.NoError(t, err)

	assert.Equal(t, original.FrontMatter.Version, parsed.FrontMatter.Version)
	assert.Equal(t, original.FrontMatter.Batch, parsed.FrontMatter.Batch)
	assert.Equal(t, original.FrontMatter.DependsOn, parsed.FrontMatter.DependsOn)
	assert.Equal(t, original.FrontMatter.Scope, parsed.FrontMatter.Scope)
	assert.Equal(t, original.FrontMatter.EstimatedComplexity, parsed.FrontMatter.EstimatedComplexity)
	assert.Equal(t, original.Task, parsed.Task)
	assert.Equal(t, original.Criteria, parsed.Criteria)
	assert.Equal(t, original.Context, parsed.Context)
	assert.Equal(t, original.FilesToModify, parsed.FilesToModify)
}

func TestParseBodyNoFrontMatter(t *testing.T) {
	body := "## Task\n\nDo something.\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, "Do something.", parsed.Task)
	assert.Equal(t, 0, parsed.FrontMatter.Version)
}

func TestRenderIntegratorFields(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{
			Version:             1,
			Batch:               5,
			Type:                "fix",
			FixCycle:            2,
			BatchPR:             50,
			ConflictResolution:  true,
			ConflictingBranches: []string{"herd/worker/42-foo", "herd/worker/43-bar"},
		},
		Task: "Resolve merge conflict.",
	}
	rendered := RenderBody(body)

	parsed, err := ParseBody(rendered)
	require.NoError(t, err)
	assert.Equal(t, "fix", parsed.FrontMatter.Type)
	assert.Equal(t, 2, parsed.FrontMatter.FixCycle)
	assert.Equal(t, 50, parsed.FrontMatter.BatchPR)
	assert.True(t, parsed.FrontMatter.ConflictResolution)
	assert.Equal(t, []string{"herd/worker/42-foo", "herd/worker/43-bar"}, parsed.FrontMatter.ConflictingBranches)
}

func TestValidateTransition(t *testing.T) {
	valid := []struct{ from, to string }{
		{StatusBlocked, StatusReady},
		{StatusBlocked, StatusFailed},
		{StatusReady, StatusInProgress},
		{StatusReady, StatusFailed},
		{StatusInProgress, StatusDone},
		{StatusInProgress, StatusFailed},
		{StatusFailed, StatusReady},
		{StatusDone, StatusFailed},
	}
	for _, tt := range valid {
		assert.NoError(t, ValidateTransition(tt.from, tt.to), "%s → %s should be valid", tt.from, tt.to)
	}

	invalid := []struct{ from, to string }{
		{StatusBlocked, StatusDone},
		{StatusBlocked, StatusInProgress},
		{StatusReady, StatusDone},
		{StatusReady, StatusBlocked},
		{StatusInProgress, StatusReady},
		{StatusDone, StatusReady},
		{StatusDone, StatusInProgress},
		{StatusFailed, StatusDone},
	}
	for _, tt := range invalid {
		assert.Error(t, ValidateTransition(tt.from, tt.to), "%s → %s should be invalid", tt.from, tt.to)
	}
}
