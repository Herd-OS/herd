package cli

import (
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetConfigValue(t *testing.T) {
	cfg := config.Default()
	cfg.Platform.Owner = "my-org"

	val, err := getConfigValue(cfg, "platform.owner")
	require.NoError(t, err)
	assert.Equal(t, "my-org", val)

	val, err = getConfigValue(cfg, "workers.max_concurrent")
	require.NoError(t, err)
	assert.Equal(t, "3", val)

	val, err = getConfigValue(cfg, "pull_requests.auto_merge")
	require.NoError(t, err)
	assert.Equal(t, "false", val)
}

func TestGetConfigValueUnknownKey(t *testing.T) {
	cfg := config.Default()
	_, err := getConfigValue(cfg, "nonexistent.key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestGetConfigValueEmptyString(t *testing.T) {
	cfg := config.Default()
	val, err := getConfigValue(cfg, "agent.binary")
	require.NoError(t, err)
	assert.Equal(t, "(not set)", val)
}

func TestGetConfigValueAgentRoleOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Planner = &config.AgentRole{Provider: "codex"}

	val, err := getConfigValue(cfg, "agent.planner.provider")
	require.NoError(t, err)
	assert.Equal(t, "codex", val)

	_, err = getConfigValue(cfg, "agent.planner.model")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestSetConfigValueString(t *testing.T) {
	cfg := config.Default()
	require.NoError(t, setConfigValue(cfg, "platform.owner", "new-org"))
	assert.Equal(t, "new-org", cfg.Platform.Owner)
}

func TestSetConfigValueInt(t *testing.T) {
	cfg := config.Default()
	require.NoError(t, setConfigValue(cfg, "workers.max_concurrent", "10"))
	assert.Equal(t, 10, cfg.Workers.MaxConcurrent)
}

func TestSetConfigValueBool(t *testing.T) {
	cfg := config.Default()
	require.NoError(t, setConfigValue(cfg, "pull_requests.auto_merge", "true"))
	assert.Equal(t, true, cfg.PullRequests.AutoMerge)
}

func TestSetConfigValueInvalidInt(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "workers.max_concurrent", "abc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be a number")
}

func TestSetConfigValueInvalidBool(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "pull_requests.auto_merge", "maybe")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be true or false")
}

func TestSetConfigValueUnknownSection(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "nonexistent.field", "value")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config section")
}

func TestSetConfigValueUnknownField(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "workers.nonexistent", "value")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestSetConfigValueNoSection(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "nodot", "value")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key format")
}

func TestSetConfigValueSliceField(t *testing.T) {
	cfg := config.Default()
	err := setConfigValue(cfg, "monitor.notify_users", "alice")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot set")
}

func TestSetConfigValueAgentPlannerRoleOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		check func(*testing.T, *config.Config)
	}{
		{
			name:  "provider",
			key:   "agent.planner.provider",
			value: "codex",
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "codex", cfg.Agent.Planner.Provider)
			},
		},
		{
			name:  "model",
			key:   "agent.planner.model",
			value: "gpt-5-codex",
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "gpt-5-codex", cfg.Agent.Planner.Model)
			},
		},
		{
			name:  "max turns",
			key:   "agent.planner.max_turns",
			value: "12",
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, 12, cfg.Agent.Planner.MaxTurns)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			require.Nil(t, cfg.Agent.Planner)

			require.NoError(t, setConfigValue(cfg, tt.key, tt.value))

			require.NotNil(t, cfg.Agent.Planner)
			tt.check(t, cfg)
			val, err := getConfigValue(cfg, tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.value, val)
		})
	}
}

func TestSetConfigValueAgentWorkersRoleOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		check func(*testing.T, *config.Config)
	}{
		{
			name:  "codex sandbox",
			key:   "agent.workers.codex_sandbox",
			value: "danger-full-access",
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "danger-full-access", cfg.Agent.Workers.CodexSandbox)
			},
		},
		{
			name:  "codex reasoning effort",
			key:   "agent.workers.codex_reasoning_effort",
			value: "high",
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "high", cfg.Agent.Workers.CodexReasoningEffort)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			require.Nil(t, cfg.Agent.Workers)

			require.NoError(t, setConfigValue(cfg, tt.key, tt.value))

			require.NotNil(t, cfg.Agent.Workers)
			tt.check(t, cfg)
			val, err := getConfigValue(cfg, tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.value, val)
		})
	}
}

func TestSetConfigValueAgentRoleOverrideUnknownField(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		value       string
		errContains string
	}{
		{
			name:        "unknown planner field",
			key:         "agent.planner.nonexistent",
			value:       "value",
			errContains: "unknown config key: agent.planner.nonexistent",
		},
		{
			name:        "unknown workers field",
			key:         "agent.workers.nonexistent",
			value:       "value",
			errContains: "unknown config key: agent.workers.nonexistent",
		},
		{
			name:        "extra path under planner field",
			key:         "agent.planner.provider.extra",
			value:       "value",
			errContains: "unknown config key: agent.planner.provider.extra",
		},
		{
			name:        "invalid planner int value",
			key:         "agent.planner.max_turns",
			value:       "abc",
			errContains: `agent.planner.max_turns must be a number, got "abc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()

			err := setConfigValue(cfg, tt.key, tt.value)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
			assert.Nil(t, cfg.Agent.Planner)
			assert.Nil(t, cfg.Agent.Workers)
		})
	}
}

func TestFlattenConfig(t *testing.T) {
	cfg := config.Default()
	kvs := flattenConfig(cfg)
	assert.True(t, len(kvs) > 20, "should have many keys, got %d", len(kvs))

	// Verify all keys are dotted
	for _, kv := range kvs {
		assert.Contains(t, kv.key, ".", "key %q should contain a dot", kv.key)
	}
}

func TestFlattenConfigAgentRoleOverrides(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*config.Config)
		want     map[string]string
		notWant  []string
		wantKeys []string
	}{
		{
			name: "nil role blocks skipped",
			mutate: func(cfg *config.Config) {
				cfg.Agent.Planner = nil
				cfg.Agent.Workers = nil
			},
			notWant: []string{
				"agent.planner.provider",
				"agent.workers.codex_sandbox",
			},
		},
		{
			name: "sparse role blocks include only explicit fields",
			mutate: func(cfg *config.Config) {
				cfg.Agent.Planner = &config.AgentRole{Provider: "codex", Model: "gpt-5-codex"}
				cfg.Agent.Workers = &config.AgentRole{CodexSandbox: "danger-full-access"}
			},
			want: map[string]string{
				"agent.planner.provider":      "codex",
				"agent.planner.model":         "gpt-5-codex",
				"agent.workers.codex_sandbox": "danger-full-access",
			},
			notWant: []string{
				"agent.planner.binary",
				"agent.planner.max_turns",
				"agent.workers.provider",
				"agent.workers.codex_reasoning_effort",
			},
		},
		{
			name: "all role fields keep configured order",
			mutate: func(cfg *config.Config) {
				cfg.Agent.Planner = &config.AgentRole{
					Provider:             "codex",
					Binary:               "codex",
					Model:                "gpt-5-codex",
					MaxTurns:             7,
					CodexReasoningEffort: "high",
					CodexSandbox:         "workspace-write",
				}
			},
			wantKeys: []string{
				"agent.planner.provider",
				"agent.planner.binary",
				"agent.planner.model",
				"agent.planner.max_turns",
				"agent.planner.codex_reasoning_effort",
				"agent.planner.codex_sandbox",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(cfg)

			kvs := flattenConfig(cfg)
			got := keyValueMap(kvs)

			for key, want := range tt.want {
				assert.Equal(t, want, got[key])
			}
			for _, key := range tt.notWant {
				assert.NotContains(t, got, key)
			}
			if len(tt.wantKeys) > 0 {
				assertRoleOverrideOrder(t, kvs, tt.wantKeys)
			}
		})
	}
}

func TestFlattenConfig_CoAuthorEmail(t *testing.T) {
	cfg := config.Default()
	kvs := flattenConfig(cfg)

	found := false
	for _, kv := range kvs {
		if kv.key == "pull_requests.co_author_email" {
			found = true
			assert.Equal(t, "(not set)", kv.value)
		}
	}
	assert.True(t, found, "pull_requests.co_author_email should be in config list")
}

func TestFlattenConfig_AgentExec(t *testing.T) {
	cfg := config.Default()
	kvs := flattenConfig(cfg)

	foundExec := false
	foundExecImage := false
	for _, kv := range kvs {
		switch kv.key {
		case "agent.exec":
			foundExec = true
			assert.Equal(t, "(not set)", kv.value)
		case "agent.exec_image":
			foundExecImage = true
			assert.Equal(t, "(not set)", kv.value)
		}
	}
	assert.True(t, foundExec, "agent.exec should be in config list")
	assert.True(t, foundExecImage, "agent.exec_image should be in config list")
}

func TestSetGetConfigValue_AgentExecRoundTrip(t *testing.T) {
	cfg := config.Default()

	require.NoError(t, setConfigValue(cfg, "agent.exec", "docker"))
	assert.Equal(t, "docker", cfg.Agent.Exec)

	val, err := getConfigValue(cfg, "agent.exec")
	require.NoError(t, err)
	assert.Equal(t, "docker", val)

	require.NoError(t, setConfigValue(cfg, "agent.exec_image", "example/foo:bar"))
	val, err = getConfigValue(cfg, "agent.exec_image")
	require.NoError(t, err)
	assert.Equal(t, "example/foo:bar", val)
}

func TestSetGetConfigValue_AgentEmbeddedRoleRoundTrip(t *testing.T) {
	cfg := config.Default()

	require.NoError(t, setConfigValue(cfg, "agent.provider", "codex"))
	assert.Equal(t, "codex", cfg.Agent.Provider)

	val, err := getConfigValue(cfg, "agent.provider")
	require.NoError(t, err)
	assert.Equal(t, "codex", val)
}

func TestDisplayValue(t *testing.T) {
	assert.Equal(t, "(not set)", displayValue(""))
	assert.Equal(t, "claude", displayValue("claude"))
}

func TestFormatStringSlice(t *testing.T) {
	assert.Equal(t, "[]", formatStringSlice([]string{}))
	assert.Equal(t, "[alice]", formatStringSlice([]string{"alice"}))
	assert.Equal(t, "[alice, bob]", formatStringSlice([]string{"alice", "bob"}))
}

func keyValueMap(kvs []keyValue) map[string]string {
	got := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		got[kv.key] = kv.value
	}
	return got
}

func assertRoleOverrideOrder(t *testing.T, kvs []keyValue, wantKeys []string) {
	t.Helper()

	var gotKeys []string
	for _, kv := range kvs {
		for _, wantKey := range wantKeys {
			if kv.key == wantKey {
				gotKeys = append(gotKeys, kv.key)
			}
		}
	}

	assert.Equal(t, wantKeys, gotKeys)
}
