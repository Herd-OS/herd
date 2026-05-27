package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/agent/opencode"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantErr  bool
		wantType any
	}{
		{
			name:     "claude provider",
			provider: "claude",
			wantErr:  false,
			wantType: (*claude.ClaudeAgent)(nil),
		},
		{
			name:     "empty provider defaults to claude",
			provider: "",
			wantErr:  false,
			wantType: (*claude.ClaudeAgent)(nil),
		},
		{
			name:     "opencode provider",
			provider: "opencode",
			wantErr:  false,
			wantType: (*opencode.OpenCodeAgent)(nil),
		},
		{
			name:     "unknown provider returns error",
			provider: "gpt",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag, err := New(tt.provider, "", "")
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, ag)
				assert.Contains(t, err.Error(), "unknown agent provider")
				assert.Contains(t, err.Error(), "claude, opencode")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, ag)
			assert.IsType(t, tt.wantType, ag)
		})
	}
}

func TestNewPassesBinaryAndModel(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		ag, err := New("claude", "/custom/claude", "opus")
		require.NoError(t, err)
		ca, ok := ag.(*claude.ClaudeAgent)
		require.True(t, ok)
		assert.Equal(t, "/custom/claude", ca.BinaryPath)
		assert.Equal(t, "opus", ca.Model)
	})

	t.Run("opencode", func(t *testing.T) {
		ag, err := New("opencode", "/custom/opencode", "anthropic/claude-sonnet-4")
		require.NoError(t, err)
		oa, ok := ag.(*opencode.OpenCodeAgent)
		require.True(t, ok)
		assert.Equal(t, "/custom/opencode", oa.BinaryPath)
		assert.Equal(t, "anthropic/claude-sonnet-4", oa.Model)
	})
}
