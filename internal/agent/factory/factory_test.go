package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/agent/codex"
	"github.com/herd-os/herd/internal/agent/opencode"
	"github.com/herd-os/herd/internal/config"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		role     config.AgentRole
		wantErr  bool
		wantType any
	}{
		{
			name:     "claude provider",
			role:     config.AgentRole{Provider: "claude"},
			wantErr:  false,
			wantType: (*claude.ClaudeAgent)(nil),
		},
		{
			name:     "empty provider defaults to claude",
			role:     config.AgentRole{Provider: ""},
			wantErr:  false,
			wantType: (*claude.ClaudeAgent)(nil),
		},
		{
			name:     "opencode provider",
			role:     config.AgentRole{Provider: "opencode"},
			wantErr:  false,
			wantType: (*opencode.OpenCodeAgent)(nil),
		},
		{
			name:     "codex provider",
			role:     config.AgentRole{Provider: "codex"},
			wantErr:  false,
			wantType: (*codex.CodexAgent)(nil),
		},
		{
			name:    "unknown provider returns error",
			role:    config.AgentRole{Provider: "gpt"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag, err := New(tt.role)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, ag)
				assert.Contains(t, err.Error(), "unknown agent provider")
				assert.Contains(t, err.Error(), "claude, opencode, codex")
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
		ag, err := New(config.AgentRole{Provider: "claude", Binary: "/custom/claude", Model: "opus"})
		require.NoError(t, err)
		ca, ok := ag.(*claude.ClaudeAgent)
		require.True(t, ok)
		assert.Equal(t, "/custom/claude", ca.BinaryPath)
		assert.Equal(t, "opus", ca.Model)
	})

	t.Run("opencode", func(t *testing.T) {
		ag, err := New(config.AgentRole{Provider: "opencode", Binary: "/custom/opencode", Model: "anthropic/claude-sonnet-4"})
		require.NoError(t, err)
		oa, ok := ag.(*opencode.OpenCodeAgent)
		require.True(t, ok)
		assert.Equal(t, "/custom/opencode", oa.BinaryPath)
		assert.Equal(t, "anthropic/claude-sonnet-4", oa.Model)
	})

	t.Run("codex", func(t *testing.T) {
		ag, err := New(config.AgentRole{
			Provider:             "codex",
			Binary:               "/custom/codex",
			Model:                "gpt-5-codex",
			CodexReasoningEffort: "high",
			CodexSandbox:         "danger-full-access",
		})
		require.NoError(t, err)
		ca, ok := ag.(*codex.CodexAgent)
		require.True(t, ok)
		assert.Equal(t, "/custom/codex", ca.BinaryPath)
		assert.Equal(t, "gpt-5-codex", ca.Model)
		assert.Equal(t, "high", ca.ReasoningEffort)
		assert.Equal(t, "danger-full-access", ca.Sandbox)
	})
}

func TestNewWithResolvedPlannerAndWorkersRoles(t *testing.T) {
	agentConfig := config.Agent{
		AgentRole: config.AgentRole{Provider: "claude", Model: "sonnet"},
		Workers:   &config.AgentRole{Provider: "codex", Model: "gpt-5-codex", CodexReasoningEffort: "high"},
	}

	plannerAgent, err := New(agentConfig.Resolve(config.AgentRolePlanner))
	require.NoError(t, err)
	assert.IsType(t, (*claude.ClaudeAgent)(nil), plannerAgent)

	workersAgent, err := New(agentConfig.Resolve(config.AgentRoleWorkers))
	require.NoError(t, err)
	assert.IsType(t, (*codex.CodexAgent)(nil), workersAgent)
	ca, ok := workersAgent.(*codex.CodexAgent)
	require.True(t, ok)
	assert.Equal(t, "danger-full-access", ca.Sandbox)
}
