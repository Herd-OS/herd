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
