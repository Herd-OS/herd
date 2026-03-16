package issues

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllLabels(t *testing.T) {
	labels := AllLabels()
	assert.Len(t, labels, 10)
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

func TestParseBodyEmptyString(t *testing.T) {
	parsed, err := ParseBody("")
	require.NoError(t, err)
	assert.Equal(t, "", parsed.Task)
	assert.Nil(t, parsed.Criteria)
	assert.Equal(t, 0, parsed.FrontMatter.Version)
}

func TestParseBodyPlainTextNoSections(t *testing.T) {
	body := "Just a plain text description with no markdown sections."
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, "", parsed.Task) // No ## Task section
}

func TestParseBodyMalformedFrontMatter(t *testing.T) {
	body := "---\n{{not valid yaml\n---\n\n## Task\n\nDo something."
	_, err := ParseBody(body)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing front matter")
}

func TestParseBodyFrontMatterWithoutHerdWrapper(t *testing.T) {
	// Front matter that doesn't use the herd: wrapper
	body := "---\nversion: 1\nbatch: 7\n---\n\n## Task\n\nDo something."
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	// Without the herd: wrapper, fields don't map — version stays 0
	assert.Equal(t, 0, parsed.FrontMatter.Version)
	assert.Equal(t, "Do something.", parsed.Task)
}

func TestParseBodyOnlyFrontMatter(t *testing.T) {
	body := "---\nherd:\n  version: 1\n  batch: 5\n---\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.FrontMatter.Version)
	assert.Equal(t, 5, parsed.FrontMatter.Batch)
	assert.Equal(t, "", parsed.Task)
}

func TestParseBodyUnclosedFrontMatter(t *testing.T) {
	// Only opening --- with no closing ---
	body := "---\nherd:\n  version: 1\n\n## Task\n\nDo something."
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	// Treated as no front matter — entire body is markdown
	assert.Equal(t, 0, parsed.FrontMatter.Version)
}

func TestParseBodyExtraSections(t *testing.T) {
	body := `## Task

Do something.

## Implementation Details

Use the Foo pattern.

## Acceptance Criteria

- [ ] Thing works
- [ ] Tests pass

## Context

Some context here.

## Conventions

Follow the bar style.
`
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, "Do something.", parsed.Task)
	assert.Equal(t, []string{"Thing works", "Tests pass"}, parsed.Criteria)
	assert.Equal(t, "Some context here.", parsed.Context)
}

func TestParseBodyCheckedCriteria(t *testing.T) {
	body := "## Acceptance Criteria\n\n- [x] Already done\n- [ ] Not done yet\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"Already done", "Not done yet"}, parsed.Criteria)
}

func TestParseBodyNoCriteria(t *testing.T) {
	body := "## Task\n\nDo something.\n\n## Context\n\nSome info.\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, "Do something.", parsed.Task)
	assert.Nil(t, parsed.Criteria)
	assert.Equal(t, "Some info.", parsed.Context)
}

func TestParseBodyFilesToModifyVariants(t *testing.T) {
	body := "## Files to Modify\n\n- `src/foo.ts`\n- `src/bar.ts`\n- not a file reference\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	// Only lines matching - `file` pattern (ending with backtick) are extracted
	assert.Equal(t, []string{"src/foo.ts", "src/bar.ts"}, parsed.FilesToModify)
}

func TestParseBodyFilesToModifyWithAnnotation(t *testing.T) {
	// Lines like `- `file` (create)` don't end with backtick, so they're skipped
	body := "## Files to Modify\n\n- `src/foo.ts` (create)\n- `src/bar.ts`\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	// Only the line ending with backtick matches
	assert.Equal(t, []string{"src/bar.ts"}, parsed.FilesToModify)
}

func TestParseBodyFilesToModifyEmpty(t *testing.T) {
	body := "## Files to Modify\n\nNo files listed here.\n"
	parsed, err := ParseBody(body)
	require.NoError(t, err)
	assert.Nil(t, parsed.FilesToModify)
}

func TestRenderBodyMinimal(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{Version: 1},
		Task:        "Do a thing.",
	}
	rendered := RenderBody(body)
	assert.Contains(t, rendered, "version: 1")
	assert.Contains(t, rendered, "## Task")
	assert.Contains(t, rendered, "Do a thing.")
	assert.NotContains(t, rendered, "## Acceptance Criteria")
	assert.NotContains(t, rendered, "## Context")
	assert.NotContains(t, rendered, "## Files to Modify")
}

func TestRenderBodyRunnerLabel(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{
			Version:     1,
			RunnerLabel: "herd-gpu",
		},
		Task: "Train model.",
	}
	rendered := RenderBody(body)
	assert.Contains(t, rendered, "runner_label: herd-gpu")
}

func TestRenderBodyEmptyDependsOn(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{
			Version:   1,
			DependsOn: []int{},
		},
		Task: "Independent task.",
	}
	rendered := RenderBody(body)
	// Empty depends_on should not appear in rendered output
	assert.NotContains(t, rendered, "depends_on")
}

func TestValidateTransitionUnknownStatus(t *testing.T) {
	err := ValidateTransition("herd/status:unknown", StatusReady)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown status")
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
