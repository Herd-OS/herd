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

func TestValidate_CodexReplicasMinimum(t *testing.T) {
	tests := []struct {
		name      string
		replicas  int
		wantError bool
	}{
		{"one is valid", 1, false},
		{"two is valid", 2, false},
		{"zero is invalid", 0, true},
		{"negative is invalid", -3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.CodexReplicas = tt.replicas

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), "agent.codex_replicas must be >= 1")
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_MaxConcurrentBoundByReplicas(t *testing.T) {
	t.Run("error when subscription env set and max_concurrent > replicas", func(t *testing.T) {
		t.Setenv("CODEX_AUTH_JSON", `{"token":"abc"}`)
		cfg := Default()
		cfg.Platform.Owner = "org"
		cfg.Platform.Repo = "repo"
		cfg.Agent.Provider = "codex"
		cfg.Agent.CodexReplicas = 2
		cfg.Workers.MaxConcurrent = 3

		ve := Validate(cfg)
		require.NotNil(t, ve)
		assert.Contains(t, ve.Error(), "workers.max_concurrent")
		assert.Contains(t, ve.Error(), "agent.codex_replicas")
	})

	t.Run("clean when subscription env unset", func(t *testing.T) {
		// Ensure no CODEX_AUTH_JSON leaks in from the environment.
		t.Setenv("CODEX_AUTH_JSON", "")
		cfg := Default()
		cfg.Platform.Owner = "org"
		cfg.Platform.Repo = "repo"
		cfg.Agent.Provider = "codex"
		cfg.Agent.CodexReplicas = 2
		cfg.Workers.MaxConcurrent = 3

		assert.Nil(t, Validate(cfg))
	})

	t.Run("clean when max_concurrent <= replicas", func(t *testing.T) {
		t.Setenv("CODEX_AUTH_JSON", `{"token":"abc"}`)
		cfg := Default()
		cfg.Platform.Owner = "org"
		cfg.Platform.Repo = "repo"
		cfg.Agent.Provider = "codex"
		cfg.Agent.CodexReplicas = 3
		cfg.Workers.MaxConcurrent = 3

		assert.Nil(t, Validate(cfg))
	})
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
