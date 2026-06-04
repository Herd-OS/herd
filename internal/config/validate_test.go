package config

import (
	"fmt"
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
		// Multi-replica subscription requires a per-replica seed for each slot;
		// set them so this case isolates the max_concurrent bound check.
		t.Setenv("CODEX_AUTH_JSON_1", `{"token":"a"}`)
		t.Setenv("CODEX_AUTH_JSON_2", `{"token":"b"}`)
		t.Setenv("CODEX_AUTH_JSON_3", `{"token":"c"}`)
		cfg := Default()
		cfg.Platform.Owner = "org"
		cfg.Platform.Repo = "repo"
		cfg.Agent.Provider = "codex"
		cfg.Agent.CodexReplicas = 3
		cfg.Workers.MaxConcurrent = 3

		assert.Nil(t, Validate(cfg))
	})
}

// clearCodexAuthEnv unsets bare CODEX_AUTH_JSON and CODEX_AUTH_JSON_1..max so a
// test starts from a known-empty baseline regardless of the host environment.
func clearCodexAuthEnv(t *testing.T, max int) {
	t.Helper()
	t.Setenv("CODEX_AUTH_JSON", "")
	for i := 1; i <= max; i++ {
		t.Setenv(fmt.Sprintf("CODEX_AUTH_JSON_%d", i), "")
	}
}

func TestValidate_CodexMultiReplicaSeedCompleteness(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		replicas   int
		setSlots   map[int]string // index -> value to set (others left empty)
		setBare    string         // bare CODEX_AUTH_JSON value (empty = unset)
		wantError  bool
		wantSubstr []string
	}{
		{
			name:      "all slots set passes",
			provider:  "codex",
			replicas:  3,
			setSlots:  map[int]string{1: `{"t":"a"}`, 2: `{"t":"b"}`, 3: `{"t":"c"}`},
			wantError: false,
		},
		{
			name:       "some slots missing errors",
			provider:   "codex",
			replicas:   3,
			setSlots:   map[int]string{1: `{"t":"a"}`},
			wantError:  true,
			wantSubstr: []string{"CODEX_AUTH_JSON_2", "CODEX_AUTH_JSON_3"},
		},
		{
			name:       "whitespace-only slots treated as missing",
			provider:   "codex",
			replicas:   3,
			setSlots:   map[int]string{1: `{"t":"a"}`, 2: "   ", 3: "\t\n"},
			wantError:  true,
			wantSubstr: []string{"CODEX_AUTH_JSON_2", "CODEX_AUTH_JSON_3"},
		},
		{
			name:      "single replica with bare auth no error",
			provider:  "codex",
			replicas:  1,
			setBare:   `{"t":"a"}`,
			wantError: false,
		},
		{
			name:      "non-codex provider no error",
			provider:  "claude",
			replicas:  3,
			setSlots:  map[int]string{1: `{"t":"a"}`},
			setBare:   `{"t":"a"}`,
			wantError: false,
		},
		{
			name:      "codex multi-replica without subscription env no error",
			provider:  "codex",
			replicas:  3,
			wantError: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCodexAuthEnv(t, tt.replicas)
			if tt.setBare != "" {
				t.Setenv("CODEX_AUTH_JSON", tt.setBare)
			}
			for i, v := range tt.setSlots {
				t.Setenv(fmt.Sprintf("CODEX_AUTH_JSON_%d", i), v)
			}

			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.Provider = tt.provider
			cfg.Agent.CodexReplicas = tt.replicas
			// Keep max_concurrent <= replicas so the unrelated bound check stays clean.
			cfg.Workers.MaxConcurrent = 1

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), "requires CODEX_AUTH_JSON_1")
				for _, sub := range tt.wantSubstr {
					assert.Contains(t, ve.Error(), sub)
				}
			} else if ve != nil {
				assert.NotContains(t, ve.Error(), "requires CODEX_AUTH_JSON_1",
					"multi-replica seed-completeness rule must not fire: %s", ve.Error())
			}
		})
	}
}

func TestMissingCodexAuthJSONSlots(t *testing.T) {
	tests := []struct {
		name     string
		n        int
		setSlots map[int]string
		want     []string
	}{
		{
			name: "no env returns all",
			n:    3,
			want: []string{"CODEX_AUTH_JSON_1", "CODEX_AUTH_JSON_2", "CODEX_AUTH_JSON_3"},
		},
		{
			name:     "partial env returns only missing",
			n:        3,
			setSlots: map[int]string{1: `{"t":"a"}`, 3: `{"t":"c"}`},
			want:     []string{"CODEX_AUTH_JSON_2"},
		},
		{
			name:     "full env returns empty",
			n:        2,
			setSlots: map[int]string{1: `{"t":"a"}`, 2: `{"t":"b"}`},
			want:     []string{},
		},
		{
			name:     "whitespace-only treated as missing",
			n:        2,
			setSlots: map[int]string{1: `{"t":"a"}`, 2: "   "},
			want:     []string{"CODEX_AUTH_JSON_2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCodexAuthEnv(t, tt.n)
			for i, v := range tt.setSlots {
				t.Setenv(fmt.Sprintf("CODEX_AUTH_JSON_%d", i), v)
			}
			assert.Equal(t, tt.want, MissingCodexAuthJSONSlots(tt.n))
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
