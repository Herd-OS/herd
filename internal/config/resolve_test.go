package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentResolve(t *testing.T) {
	tests := []struct {
		name  string
		role  string
		agent Agent
		want  AgentRole
	}{
		{
			name: "no role blocks with claude base returns base without smart defaults",
			role: AgentRolePlanner,
			agent: Agent{AgentRole: AgentRole{
				Provider:             "claude",
				Binary:               "claude",
				Model:                "opus",
				MaxTurns:             5,
				CodexReasoningEffort: "medium",
			}},
			want: AgentRole{
				Provider:             "claude",
				Binary:               "claude",
				Model:                "opus",
				MaxTurns:             5,
				CodexReasoningEffort: "medium",
			},
		},
		{
			name: "nil planner block behaves like empty planner block",
			role: AgentRolePlanner,
			agent: Agent{
				AgentRole: AgentRole{Provider: "claude", Model: "opus"},
				Planner:   nil,
			},
			want: AgentRole{Provider: "claude", Model: "opus"},
		},
		{
			name: "empty planner block behaves like nil planner block",
			role: AgentRolePlanner,
			agent: Agent{
				AgentRole: AgentRole{Provider: "claude", Model: "opus"},
				Planner:   &AgentRole{},
			},
			want: AgentRole{Provider: "claude", Model: "opus"},
		},
		{
			name: "planner override of one field keeps base fields",
			role: AgentRolePlanner,
			agent: Agent{
				AgentRole: AgentRole{Provider: "claude", Binary: "claude", Model: "sonnet", MaxTurns: 3},
				Planner:   &AgentRole{Model: "opus"},
			},
			want: AgentRole{Provider: "claude", Binary: "claude", Model: "opus", MaxTurns: 3},
		},
		{
			name: "role wins over base",
			role: AgentRoleWorkers,
			agent: Agent{
				AgentRole: AgentRole{
					Provider:             "claude",
					Binary:               "claude",
					Model:                "sonnet",
					MaxTurns:             3,
					CodexReasoningEffort: "medium",
					CodexSandbox:         "workspace-write",
				},
				Workers: &AgentRole{
					Provider:             "opencode",
					Binary:               "opencode",
					Model:                "kimi",
					MaxTurns:             7,
					CodexReasoningEffort: "high",
					CodexSandbox:         "read-only",
				},
			},
			want: AgentRole{
				Provider:             "opencode",
				Binary:               "opencode",
				Model:                "kimi",
				MaxTurns:             7,
				CodexReasoningEffort: "high",
				CodexSandbox:         "read-only",
			},
		},
		{
			name: "non-codex leaves sandbox empty",
			role: AgentRoleWorkers,
			agent: Agent{
				AgentRole: AgentRole{Provider: "claude"},
				Exec:      "docker",
			},
			want: AgentRole{Provider: "claude"},
		},
		{
			name: "explicit base workspace-write beats planner docker smart default",
			role: AgentRolePlanner,
			agent: Agent{
				AgentRole: AgentRole{Provider: "codex", CodexSandbox: "workspace-write"},
				Exec:      "docker",
			},
			want: AgentRole{Provider: "codex", CodexSandbox: "workspace-write"},
		},
		{
			name: "explicit workers read-only remains read-only",
			role: AgentRoleWorkers,
			agent: Agent{
				AgentRole: AgentRole{Provider: "codex"},
				Workers:   &AgentRole{CodexSandbox: "read-only"},
			},
			want: AgentRole{Provider: "codex", CodexSandbox: "read-only"},
		},
		{
			name: "role provider codex with base claude applies planner docker smart default",
			role: AgentRolePlanner,
			agent: Agent{
				AgentRole: AgentRole{Provider: "claude"},
				Exec:      "docker",
				Planner:   &AgentRole{Provider: "codex"},
			},
			want: AgentRole{Provider: "codex", CodexSandbox: "danger-full-access"},
		},
		{
			name: "unknown role returns base only",
			role: "reviewer",
			agent: Agent{
				AgentRole: AgentRole{Provider: "codex"},
				Exec:      "docker",
			},
			want: AgentRole{Provider: "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.agent.Resolve(tt.role))
		})
	}
}

func TestAgentResolveCodexSandboxSmartDefaults(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		exec       string
		roleConfig *AgentRole
		want       string
	}{
		{"planner nil block with empty exec leaves empty", AgentRolePlanner, "", nil, ""},
		{"planner empty block with empty exec leaves empty", AgentRolePlanner, "", &AgentRole{}, ""},
		{"planner nil block with local exec leaves empty", AgentRolePlanner, "local", nil, ""},
		{"planner empty block with local exec leaves empty", AgentRolePlanner, "local", &AgentRole{}, ""},
		{"planner nil block with docker exec uses danger", AgentRolePlanner, "docker", nil, "danger-full-access"},
		{"planner empty block with docker exec uses danger", AgentRolePlanner, "docker", &AgentRole{}, "danger-full-access"},
		{"workers nil block with empty exec uses danger", AgentRoleWorkers, "", nil, "danger-full-access"},
		{"workers empty block with empty exec uses danger", AgentRoleWorkers, "", &AgentRole{}, "danger-full-access"},
		{"workers nil block with local exec uses danger", AgentRoleWorkers, "local", nil, "danger-full-access"},
		{"workers empty block with local exec uses danger", AgentRoleWorkers, "local", &AgentRole{}, "danger-full-access"},
		{"workers nil block with docker exec uses danger", AgentRoleWorkers, "docker", nil, "danger-full-access"},
		{"workers empty block with docker exec uses danger", AgentRoleWorkers, "docker", &AgentRole{}, "danger-full-access"},
		{"workers explicit empty sandbox block uses danger", AgentRoleWorkers, "docker", &AgentRole{CodexSandbox: ""}, "danger-full-access"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := Agent{
				AgentRole: AgentRole{Provider: "codex"},
				Exec:      tt.exec,
			}
			switch tt.role {
			case AgentRolePlanner:
				agent.Planner = tt.roleConfig
			case AgentRoleWorkers:
				agent.Workers = tt.roleConfig
			}

			resolved := agent.Resolve(tt.role)
			assert.Equal(t, tt.want, resolved.CodexSandbox)
		})
	}
}

func TestAgentResolveOrDefaultRejectsUnknownRole(t *testing.T) {
	agent := Agent{AgentRole: AgentRole{Provider: "claude"}}

	resolved, err := agent.ResolveOrDefault(AgentRolePlanner)
	require.NoError(t, err)
	assert.Equal(t, AgentRole{Provider: "claude"}, resolved)

	_, err = agent.ResolveOrDefault("reviewer")
	require.EqualError(t, err, `unknown agent role "reviewer" (supported: planner, workers)`)
}
