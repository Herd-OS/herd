package claude

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscuss_RequiresSystemPrompt(t *testing.T) {
	c := New("", "")
	err := c.Discuss(context.Background(), agent.DiscussOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system prompt is required")
}

func TestDiscuss_PropagatesAgentExitError(t *testing.T) {
	c := New("/nonexistent/claude-binary-xyz", "")
	err := c.Discuss(context.Background(), agent.DiscussOptions{
		SystemPrompt: "hello",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude exited with error")
}

func TestDiscussOptions_DefaultsAreZero(t *testing.T) {
	opts := agent.DiscussOptions{}
	tests := []struct {
		name string
		got  string
	}{
		{"RepoRoot", opts.RepoRoot},
		{"SystemPrompt", opts.SystemPrompt},
		{"InitialPrompt", opts.InitialPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "", tc.got)
		})
	}
}
