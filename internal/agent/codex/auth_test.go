package codex

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setCodexHome points CODEX_HOME at a fresh temp dir for the test and returns
// it. CODEX_AUTH_JSON is cleared so each test sets its own value.
func setCodexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_AUTH_JSON", "")
	return home
}

func TestProvisionCodexAuth_NoEnvNoOp(t *testing.T) {
	home := setCodexHome(t)

	require.NoError(t, provisionCodexAuth())

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "no files should be written when CODEX_AUTH_JSON is empty")
}

func TestProvisionCodexAuth_FirstSeed(t *testing.T) {
	home := setCodexHome(t)
	payload := []byte(`{"tokens":{"access_token":"abc"}}`)
	seed := base64.StdEncoding.EncodeToString(payload)
	t.Setenv("CODEX_AUTH_JSON", seed)

	require.NoError(t, provisionCodexAuth())

	authFile := filepath.Join(home, "auth.json")
	seedFile := filepath.Join(home, ".herd-seed")
	cfgFile := filepath.Join(home, "config.toml")

	gotAuth, err := os.ReadFile(authFile)
	require.NoError(t, err)
	assert.Equal(t, payload, gotAuth)

	gotSeed, err := os.ReadFile(seedFile)
	require.NoError(t, err)
	assert.Equal(t, seed, string(gotSeed))

	gotCfg, err := os.ReadFile(cfgFile)
	require.NoError(t, err)
	assert.Equal(t, "cli_auth_credentials_store = \"file\"\n", string(gotCfg))

	authInfo, err := os.Stat(authFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), authInfo.Mode().Perm())

	seedInfo, err := os.Stat(seedFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), seedInfo.Mode().Perm())
}

func TestProvisionCodexAuth_RestartUnchangedEnv(t *testing.T) {
	home := setCodexHome(t)
	payload := []byte(`{"tokens":{"access_token":"abc"}}`)
	seed := base64.StdEncoding.EncodeToString(payload)
	t.Setenv("CODEX_AUTH_JSON", seed)

	require.NoError(t, provisionCodexAuth())

	authFile := filepath.Join(home, "auth.json")
	// Tamper with auth.json to prove an unchanged env leaves it byte-identical
	// (the helper must not rewrite from the decoded env when the seed matches).
	require.NoError(t, os.WriteFile(authFile, []byte("UNTOUCHED"), 0o600))

	require.NoError(t, provisionCodexAuth())

	got, err := os.ReadFile(authFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("UNTOUCHED"), got)
}

func TestProvisionCodexAuth_DetectsSeedChange(t *testing.T) {
	home := setCodexHome(t)
	seedFile := filepath.Join(home, ".herd-seed")
	authFile := filepath.Join(home, "auth.json")

	// Pre-seed with a stale marker and stale auth.json.
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.WriteFile(seedFile, []byte("STALE"), 0o600))
	require.NoError(t, os.WriteFile(authFile, []byte("old"), 0o600))

	newPayload := []byte(`{"tokens":{"access_token":"new"}}`)
	newSeed := base64.StdEncoding.EncodeToString(newPayload)
	t.Setenv("CODEX_AUTH_JSON", newSeed)

	require.NoError(t, provisionCodexAuth())

	gotAuth, err := os.ReadFile(authFile)
	require.NoError(t, err)
	assert.Equal(t, newPayload, gotAuth)

	gotSeed, err := os.ReadFile(seedFile)
	require.NoError(t, err)
	assert.Equal(t, newSeed, string(gotSeed))
}

func TestProvisionCodexAuth_InvalidBase64(t *testing.T) {
	home := setCodexHome(t)
	t.Setenv("CODEX_AUTH_JSON", "not-valid-base64!!!")

	err := provisionCodexAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base64")

	_, statErr := os.Stat(filepath.Join(home, "auth.json"))
	assert.True(t, os.IsNotExist(statErr), "auth.json must not be written on decode error")
}

func TestProvisionCodexAuth_HonorsCodexHome(t *testing.T) {
	// CODEX_HOME points at a nested path that does not yet exist; the helper
	// must create it and seed there rather than under $HOME/.codex.
	base := t.TempDir()
	home := filepath.Join(base, "custom", "codex")
	t.Setenv("CODEX_HOME", home)

	payload := []byte(`{"tokens":{"access_token":"abc"}}`)
	seed := base64.StdEncoding.EncodeToString(payload)
	t.Setenv("CODEX_AUTH_JSON", seed)

	require.NoError(t, provisionCodexAuth())

	got, err := os.ReadFile(filepath.Join(home, "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestProvisionCodexAuth_ConfigTomlNoClobber(t *testing.T) {
	t.Run("existing preserved", func(t *testing.T) {
		home := setCodexHome(t)
		cfgFile := filepath.Join(home, "config.toml")
		require.NoError(t, os.MkdirAll(home, 0o700))
		existing := []byte("model = \"gpt-5\"\n")
		require.NoError(t, os.WriteFile(cfgFile, existing, 0o600))

		seed := base64.StdEncoding.EncodeToString([]byte("{}"))
		t.Setenv("CODEX_AUTH_JSON", seed)

		require.NoError(t, provisionCodexAuth())

		got, err := os.ReadFile(cfgFile)
		require.NoError(t, err)
		assert.Equal(t, existing, got, "existing config.toml must be preserved byte-for-byte")
	})

	t.Run("missing created", func(t *testing.T) {
		home := setCodexHome(t)
		seed := base64.StdEncoding.EncodeToString([]byte("{}"))
		t.Setenv("CODEX_AUTH_JSON", seed)

		require.NoError(t, provisionCodexAuth())

		got, err := os.ReadFile(filepath.Join(home, "config.toml"))
		require.NoError(t, err)
		assert.Equal(t, "cli_auth_credentials_store = \"file\"\n", string(got))
	})
}

func TestProvisionCodexAuth_PermsAreCorrect(t *testing.T) {
	home := setCodexHome(t)
	seed := base64.StdEncoding.EncodeToString([]byte(`{"tokens":{}}`))
	t.Setenv("CODEX_AUTH_JSON", seed)

	require.NoError(t, provisionCodexAuth())

	authInfo, err := os.Stat(filepath.Join(home, "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), authInfo.Mode().Perm())

	seedInfo, err := os.Stat(filepath.Join(home, ".herd-seed"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), seedInfo.Mode().Perm())

	dirInfo, err := os.Stat(home)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode()&os.ModePerm)
}

func TestResolveCodexHome(t *testing.T) {
	t.Run("uses CODEX_HOME when set", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "/tmp/explicit-codex")
		got, err := resolveCodexHome()
		require.NoError(t, err)
		assert.Equal(t, "/tmp/explicit-codex", got)
	})

	t.Run("falls back to HOME/.codex", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got, err := resolveCodexHome()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".codex"), got)
	})
}
