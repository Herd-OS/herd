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

func TestFlattenConfig(t *testing.T) {
	cfg := config.Default()
	kvs := flattenConfig(cfg)
	assert.True(t, len(kvs) > 20, "should have many keys, got %d", len(kvs))

	// Verify all keys are dotted
	for _, kv := range kvs {
		assert.Contains(t, kv.key, ".", "key %q should contain a dot", kv.key)
	}
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
