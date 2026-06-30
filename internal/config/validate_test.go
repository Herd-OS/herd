package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_AgentExec(t *testing.T) {
	tests := []struct {
		name      string
		exec      string
		wantError bool
		errSubstr string
	}{
		{"empty is valid", "", false, ""},
		{"local is valid", "local", false, ""},
		{"docker is valid", "docker", false, ""},
		{"bogus is invalid", "bogus", true, "agent.exec must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.Exec = tt.exec

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentExecImageFreeForm(t *testing.T) {
	cfg := Default()
	cfg.Platform.Owner = "org"
	cfg.Platform.Repo = "repo"
	cfg.Agent.ExecImage = "example/foo:bar"

	assert.Nil(t, Validate(cfg))
}

func TestValidate_IntegratorCIWorkflows(t *testing.T) {
	tests := []struct {
		name      string
		workflows []string
		wantError bool
		errSubstr string
	}{
		{"missing is valid", nil, false, ""},
		{"empty list is valid", []string{}, false, ""},
		{"one valid workflow is valid", []string{"CI"}, false, ""},
		{"multiple valid workflows are valid", []string{"CI - ServiceKit Ruby", "CI — Accounts"}, false, ""},
		{"blank entry is invalid", []string{""}, true, "integrator.ci_workflows[0] must not be blank"},
		{"whitespace-only entry is invalid", []string{" \t\n"}, true, "integrator.ci_workflows[0] must not be blank"},
		{"duplicate entry is invalid", []string{"CI", "CI"}, true, "integrator.ci_workflows[1] duplicates workflow name \"CI\""},
		{"ascii hyphen and unicode dash are distinct", []string{"CI - Accounts", "CI — Accounts"}, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Integrator.CIWorkflows = tt.workflows

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		wantError bool
		errSubstr string
	}{
		{"claude is valid", "claude", false, ""},
		{"opencode is valid", "opencode", false, ""},
		{"codex is valid", "codex", false, ""},
		{"case mismatch is invalid", "codeX", true, "agent.provider must be one of: claude, opencode, codex"},
		{"empty is invalid", "", true, "agent.provider must be one of: claude, opencode, codex"},
		{"unknown provider is invalid", "gpt", true, "agent.provider must be one of: claude, opencode, codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.Provider = tt.provider

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleProviders(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "base empty provider is invalid",
			modify: func(cfg *Config) {
				cfg.Agent.Provider = ""
			},
			wantError: true,
			errSubstr: "agent.provider must be one of: claude, opencode, codex",
		},
		{
			name: "planner sparse provider is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers sparse provider is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{Model: "worker-model"}
			},
		},
		{
			name: "planner invalid provider has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{Provider: "gpt"}
			},
			wantError: true,
			errSubstr: "agent.planner.provider must be one of: claude, opencode, codex",
		},
		{
			name: "workers invalid provider has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{Provider: "gpt"}
			},
			wantError: true,
			errSubstr: "agent.workers.provider must be one of: claude, opencode, codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentCodexReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		effort    string
		wantError bool
		errSubstr string
	}{
		{"empty is valid", "", false, ""},
		{"minimal is valid", "minimal", false, ""},
		{"low is valid", "low", false, ""},
		{"medium is valid", "medium", false, ""},
		{"high is valid", "high", false, ""},
		{"unknown is invalid", "extreme", true, "agent.codex_reasoning_effort must be one of: minimal, low, medium, high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.CodexReasoningEffort = tt.effort

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleCodexReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "planner empty effort is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers high effort is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexReasoningEffort: "high"}
			},
		},
		{
			name: "planner invalid effort has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{CodexReasoningEffort: "extreme"}
			},
			wantError: true,
			errSubstr: "agent.planner.codex_reasoning_effort must be one of: minimal, low, medium, high",
		},
		{
			name: "workers invalid effort has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexReasoningEffort: "extreme"}
			},
			wantError: true,
			errSubstr: "agent.workers.codex_reasoning_effort must be one of: minimal, low, medium, high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentCodexSandbox(t *testing.T) {
	tests := []struct {
		name      string
		sandbox   string
		wantError bool
		errSubstr string
	}{
		{"empty is valid (uses Codex default)", "", false, ""},
		{"read-only is valid", "read-only", false, ""},
		{"workspace-write is valid", "workspace-write", false, ""},
		{"danger-full-access is valid (container workers)", "danger-full-access", false, ""},
		{"unknown is invalid", "off", true, "agent.codex_sandbox must be one of: read-only, workspace-write, danger-full-access"},
		{"typo'd dangerous mode is invalid", "danger", true, "agent.codex_sandbox must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.CodexSandbox = tt.sandbox

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleCodexSandbox(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "planner empty sandbox is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers explicit read-only is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexSandbox: "read-only"}
			},
		},
		{
			name: "planner invalid sandbox has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{CodexSandbox: "off"}
			},
			wantError: true,
			errSubstr: `agent.planner.codex_sandbox must be one of: read-only, workspace-write, danger-full-access (or empty) — got "off"`,
		},
		{
			name: "workers invalid sandbox has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexSandbox: "off"}
			},
			wantError: true,
			errSubstr: `agent.workers.codex_sandbox must be one of: read-only, workspace-write, danger-full-access (or empty) — got "off"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}
