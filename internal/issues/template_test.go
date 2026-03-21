package issues

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderBody_Basic(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{
			Version:             1,
			Batch:               3,
			DependsOn:           []int{10, 11},
			Scope:               []string{"src/auth.go"},
			EstimatedComplexity: "medium",
		},
		Task:     "Build the auth module",
		Criteria: []string{"Login works", "Tests pass"},
		FilesToModify: []string{"src/auth.go"},
	}

	rendered := RenderBody(body)
	assert.Contains(t, rendered, "version: 1")
	assert.Contains(t, rendered, "batch: 3")
	assert.Contains(t, rendered, "depends_on: [10, 11]")
	assert.Contains(t, rendered, "estimated_complexity: medium")
	assert.Contains(t, rendered, "## Task")
	assert.Contains(t, rendered, "Build the auth module")
	assert.Contains(t, rendered, "- [ ] Login works")
	assert.Contains(t, rendered, "- `src/auth.go`")
}

func TestRenderBody_WithNewFields(t *testing.T) {
	body := IssueBody{
		FrontMatter:           FrontMatter{Version: 1},
		Task:                  "Create model",
		ImplementationDetails: "Use bcrypt with 12 rounds",
		Conventions:           []string{"Follow existing pattern", "Use testify"},
		ContextFromDeps:       []string{"Auth package available"},
		Criteria:              []string{"Model exists"},
	}

	rendered := RenderBody(body)
	assert.Contains(t, rendered, "## Implementation Details")
	assert.Contains(t, rendered, "Use bcrypt with 12 rounds")
	assert.Contains(t, rendered, "## Conventions")
	assert.Contains(t, rendered, "- Follow existing pattern")
	assert.Contains(t, rendered, "- Use testify")
	assert.Contains(t, rendered, "## Context from Dependencies")
	assert.Contains(t, rendered, "- Auth package available")
}

func TestRenderBody_OmitsEmptyOptionalSections(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{Version: 1},
		Task:        "Simple task",
	}

	rendered := RenderBody(body)
	assert.Contains(t, rendered, "## Task")
	assert.NotContains(t, rendered, "## Implementation Details")
	assert.NotContains(t, rendered, "## Conventions")
	assert.NotContains(t, rendered, "## Context from Dependencies")
	assert.NotContains(t, rendered, "## Acceptance Criteria")
	assert.NotContains(t, rendered, "## Files to Modify")
	assert.NotContains(t, rendered, "## Context")
	assert.NotContains(t, rendered, "## Conversation History")
}

func TestParseBody_RoundTrip(t *testing.T) {
	original := IssueBody{
		FrontMatter: FrontMatter{
			Version:             1,
			Batch:               5,
			DependsOn:           []int{42},
			Scope:               []string{"model.go", "model_test.go"},
			EstimatedComplexity: "high",
		},
		Task:                  "Build the user model",
		ImplementationDetails: "Use bcrypt with 12 salt rounds",
		Conventions:           []string{"Use testify", "Table-driven tests"},
		ContextFromDeps:       []string{"Auth package from task 0"},
		Criteria:              []string{"Model exists", "Tests pass"},
		ConversationHistory:   "User asked for bcrypt.\nAgent confirmed 12 rounds.",
		FilesToModify:         []string{"model.go", "model_test.go"},
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
	assert.Equal(t, original.ImplementationDetails, parsed.ImplementationDetails)
	assert.Equal(t, original.Conventions, parsed.Conventions)
	assert.Equal(t, original.ContextFromDeps, parsed.ContextFromDeps)
	assert.Equal(t, original.Criteria, parsed.Criteria)
	assert.Equal(t, original.ConversationHistory, parsed.ConversationHistory)
	assert.Equal(t, original.FilesToModify, parsed.FilesToModify)
}

func TestParseBody_NoNewFields(t *testing.T) {
	body := IssueBody{
		FrontMatter: FrontMatter{Version: 1},
		Task:        "Simple task",
		Criteria:    []string{"Done"},
	}

	rendered := RenderBody(body)
	parsed, err := ParseBody(rendered)
	require.NoError(t, err)

	assert.Equal(t, "Simple task", parsed.Task)
	assert.Equal(t, "", parsed.ImplementationDetails)
	assert.Nil(t, parsed.Conventions)
	assert.Nil(t, parsed.ContextFromDeps)
}

func TestRenderBody_WithConversationHistory(t *testing.T) {
	tests := []struct {
		name                string
		conversationHistory string
		wantSection         bool
	}{
		{
			name:                "non-empty conversation history is rendered",
			conversationHistory: "User asked about auth.\nAgent proposed bcrypt.",
			wantSection:         true,
		},
		{
			name:                "empty conversation history is omitted",
			conversationHistory: "",
			wantSection:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := IssueBody{
				FrontMatter:         FrontMatter{Version: 1},
				Task:                "Some task",
				ConversationHistory: tt.conversationHistory,
			}

			rendered := RenderBody(body)

			if tt.wantSection {
				assert.Contains(t, rendered, "## Conversation History")
				assert.Contains(t, rendered, tt.conversationHistory)
			} else {
				assert.NotContains(t, rendered, "## Conversation History")
			}
		})
	}
}

func TestFormatIntSlice(t *testing.T) {
	assert.Equal(t, "[1, 2, 3]", formatIntSlice([]int{1, 2, 3}))
	assert.Equal(t, "[42]", formatIntSlice([]int{42}))
}
