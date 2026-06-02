package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authTestHome wires HOME + XDG_DATA_HOME at a fresh temp dir and returns it,
// so provisionOpenCodeAuth's writes (auth.json and opencode.json) all stay
// inside the test sandbox. It also clears every env var provisionOpenCodeAuth
// reads — the runner env (and many dev shells) export CLAUDE_CODE_OAUTH_TOKEN
// for the claude provider, which would otherwise leak into tests that don't
// explicitly set it and silently activate the Claude bridge path. Tests that
// need a specific value override with their own t.Setenv after this call.
// Use t.TempDir() so cleanup is automatic.
func authTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, ".local", "share"))
	t.Setenv("OPENCODE_AUTH_JSON", "")
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	return dir
}

func TestEnsureOpenCodeAuth_WritesFromEnv(t *testing.T) {
	home := authTestHome(t)
	payload := `{"openai":{"type":"oauth","access":"tok"}}`
	t.Setenv("OPENCODE_AUTH_JSON", base64.StdEncoding.EncodeToString([]byte(payload)))
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	got, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got))

	info, err := os.Stat(authPath)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestEnsureOpenCodeAuth_NoEnvNoOp(t *testing.T) {
	home := authTestHome(t)
	t.Setenv("OPENCODE_AUTH_JSON", "")
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	_, err := os.Stat(authPath)
	assert.True(t, os.IsNotExist(err), "auth.json must not be written when env var is empty")

	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	_, err = os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(err), "opencode.json must not be written when env var is empty")
}

func TestEnsureOpenCodeAuth_InvalidBase64(t *testing.T) {
	authTestHome(t)
	t.Setenv("OPENCODE_AUTH_JSON", "!!!not base64!!!")
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	err := provisionOpenCodeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid base64")
}

func TestEnsureOpenCodeAuth_DoesNotClobberExisting(t *testing.T) {
	home := authTestHome(t)
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o755))
	sentinel := []byte(`{"openai":{"type":"oauth","access":"PRE-EXISTING"}}`)
	require.NoError(t, os.WriteFile(authPath, sentinel, 0o600))

	payload := []byte(`{"openai":{"type":"oauth","access":"FROM-ENV"}}`)
	t.Setenv("OPENCODE_AUTH_JSON", base64.StdEncoding.EncodeToString(payload))
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	got, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, sentinel, got, "existing auth.json must be preserved without force-seed")

	// With force-seed, the file should be replaced with the decoded env value.
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "1")
	require.NoError(t, provisionOpenCodeAuth())

	got, err = os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "force-seed must replace existing auth.json content")
}

func TestEnsureOpenCodeAuth_HonorsXdgDataHome(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "custom-xdg")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", xdg)

	payload := []byte(`{"openai":{}}`)
	t.Setenv("OPENCODE_AUTH_JSON", base64.StdEncoding.EncodeToString(payload))
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	authPath := filepath.Join(xdg, "opencode", "auth.json")
	got, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Fallback location must not also be populated.
	fallback := filepath.Join(dir, ".local", "share", "opencode", "auth.json")
	_, err = os.Stat(fallback)
	assert.True(t, os.IsNotExist(err), "fallback ~/.local/share path must not be used when XDG_DATA_HOME is set")
}

func TestEnsureOpenCodeAuth_RegistersPluginConfig(t *testing.T) {
	t.Run("no existing config", func(t *testing.T) {
		home := authTestHome(t)
		require.NoError(t, ensurePluginRegistered([]string{openCodeAuthPluginName}))

		cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
		data, err := os.ReadFile(cfgPath)
		require.NoError(t, err)

		var cfg map[string]any
		require.NoError(t, json.Unmarshal(data, &cfg))
		plugins, ok := cfg["plugin"].([]any)
		require.True(t, ok, "plugin key must be a JSON array")
		assert.Contains(t, plugins, openCodeAuthPluginName)
	})

	t.Run("pre-existing config preserves unrelated keys and entries", func(t *testing.T) {
		home := authTestHome(t)
		cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
		initial := []byte(`{"plugin":["some-other"],"theme":"x"}`)
		require.NoError(t, os.WriteFile(cfgPath, initial, 0o644))

		require.NoError(t, ensurePluginRegistered([]string{openCodeAuthPluginName}))

		data, err := os.ReadFile(cfgPath)
		require.NoError(t, err)

		var cfg map[string]any
		require.NoError(t, json.Unmarshal(data, &cfg))
		plugins, ok := cfg["plugin"].([]any)
		require.True(t, ok)
		assert.Contains(t, plugins, "some-other")
		assert.Contains(t, plugins, openCodeAuthPluginName)
		assert.Equal(t, "x", cfg["theme"], "unrelated keys must be preserved")
	})

	t.Run("idempotent when entry already present", func(t *testing.T) {
		home := authTestHome(t)
		cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
		initial := []byte(fmt.Sprintf(`{"plugin":[%q],"theme":"x"}`, openCodeAuthPluginName))
		require.NoError(t, os.WriteFile(cfgPath, initial, 0o644))

		before, err := os.ReadFile(cfgPath)
		require.NoError(t, err)

		require.NoError(t, ensurePluginRegistered([]string{openCodeAuthPluginName}))

		after, err := os.ReadFile(cfgPath)
		require.NoError(t, err)
		assert.Equal(t, before, after, "file content must be byte-identical when entry already present")
	})
}

// TestEnsureOpenCodeAuth_CalledByAgentMethods asserts that invoking Execute
// with OPENCODE_AUTH_JSON set causes auth.json to be provisioned at the XDG
// location. Because ensureOpenCodeAuth is guarded by a process-global
// sync.Once, only the FIRST agent method call within this test binary will
// actually run provisionOpenCodeAuth; subsequent calls hit the cached result.
// This single-method assertion is therefore the simplest reliable form per
// the task spec.
func TestEnsureOpenCodeAuth_CalledByAgentMethods(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	home := authTestHome(t)
	payload := []byte(`{"openai":{"type":"oauth","access":"AGENT-PATH"}}`)
	t.Setenv("OPENCODE_AUTH_JSON", base64.StdEncoding.EncodeToString(payload))
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "1") // ensure write even if sync.Once already fired

	// Reset sync.Once so this test independently exercises provisioning.
	// Tests run sequentially within a single binary; this is safe.
	ensureAuthOnce = sync.Once{}
	ensureAuthErr = nil

	dir := t.TempDir()
	stdinDump := filepath.Join(dir, "stdin.txt")
	script := filepath.Join(dir, "opencode.sh")
	scriptBody := fmt.Sprintf(
		"#!/bin/sh\ncat > '%s'\necho 'task completed successfully with detailed output'\n",
		stdinDump,
	)
	require.NoError(t, os.WriteFile(script, []byte(scriptBody), 0o755))

	a := New(script, "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: dir})
	require.NoError(t, err)

	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	got, err := os.ReadFile(authPath)
	require.NoError(t, err, "Execute must trigger ensureOpenCodeAuth and write auth.json")
	assert.Equal(t, payload, got)
}

func TestEnsurePluginRegistered_RegistersMultiplePlugins(t *testing.T) {
	home := authTestHome(t)
	require.NoError(t, ensurePluginRegistered([]string{openCodeAuthPluginName, openCodeClaudeAuthPluginName}))

	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	plugins, ok := cfg["plugin"].([]any)
	require.True(t, ok)
	assert.Contains(t, plugins, openCodeAuthPluginName)
	assert.Contains(t, plugins, openCodeClaudeAuthPluginName)

	// Idempotent on re-run: byte-identical file.
	before, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.NoError(t, ensurePluginRegistered([]string{openCodeAuthPluginName, openCodeClaudeAuthPluginName}))
	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "file must be byte-identical when all plugins already present")
}

func TestEnsurePluginRegistered_EmptyIsNoOp(t *testing.T) {
	home := authTestHome(t)
	require.NoError(t, ensurePluginRegistered(nil))
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	_, err := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(err), "empty plugin list must not create opencode.json")
}

func TestProvisionOpenCodeAuth_GatesPluginsOnEnvVars(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"openai":{"type":"oauth","access":"tok"}}`))
	cases := []struct {
		name        string
		openaiJSON  string
		claudeToken string
		wantPlugins []string
		wantNoFile  bool // opencode.json should not exist
	}{
		{"only openai", payload, "", []string{openCodeAuthPluginName}, false},
		{"only claude", "", "sk-ant-oat01-xyz", []string{openCodeClaudeAuthPluginName}, false},
		{"both", payload, "sk-ant-oat01-xyz", []string{openCodeAuthPluginName, openCodeClaudeAuthPluginName}, false},
		{"neither", "", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := authTestHome(t)
			t.Setenv("OPENCODE_AUTH_JSON", tc.openaiJSON)
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", tc.claudeToken)
			t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

			require.NoError(t, provisionOpenCodeAuth())

			cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
			if tc.wantNoFile {
				_, err := os.Stat(cfgPath)
				assert.True(t, os.IsNotExist(err), "opencode.json must not be written when no subscription env vars are set")
				return
			}
			data, err := os.ReadFile(cfgPath)
			require.NoError(t, err)
			var cfg map[string]any
			require.NoError(t, json.Unmarshal(data, &cfg))
			plugins, _ := cfg["plugin"].([]any)
			for _, want := range tc.wantPlugins {
				assert.Contains(t, plugins, want)
			}
			// Exactly the expected plugins, nothing extra.
			assert.Len(t, plugins, len(tc.wantPlugins))
		})
	}
}

func TestProvisionOpenCodeAuth_ClaudeOnlyNoOpenAIConfig(t *testing.T) {
	home := authTestHome(t)
	t.Setenv("OPENCODE_AUTH_JSON", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-xyz")
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	// No auth.json must be written for the Claude (env-var-only) path.
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	_, err := os.Stat(authPath)
	assert.True(t, os.IsNotExist(err), "Claude env-var-only path must not write auth.json")

	// Only the claude plugin registered.
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	plugins, _ := cfg["plugin"].([]any)
	assert.Contains(t, plugins, openCodeClaudeAuthPluginName)
	assert.NotContains(t, plugins, openCodeAuthPluginName)
}

func TestProvisionOpenCodeAuth_ClaudeDoesNotClobberExistingAuthJSON(t *testing.T) {
	home := authTestHome(t)
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o755))
	sentinel := []byte(`{"openai":{"access":"PRE-EXISTING"}}`)
	require.NoError(t, os.WriteFile(authPath, sentinel, 0o600))

	// Only the Claude path active; auth.json must be left untouched.
	t.Setenv("OPENCODE_AUTH_JSON", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-xyz")
	t.Setenv("OPENCODE_AUTH_FORCE_SEED", "")

	require.NoError(t, provisionOpenCodeAuth())

	got, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, sentinel, got, "Claude-only provisioning must not touch an existing auth.json")
}
