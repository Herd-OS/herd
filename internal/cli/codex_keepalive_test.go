package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const keepaliveTestInterval = 6 * 24 * time.Hour

// writeAuthJSON writes an auth.json fixture into dir and returns its path.
func writeAuthJSON(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestKeepalive_SkipsWhenAuthJsonMissing(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	assert.False(t, shouldRefresh(authFile, keepaliveTestInterval))
}

func TestKeepalive_SkipsWhenAuthModeApiKey(t *testing.T) {
	authFile := writeAuthJSON(t, t.TempDir(), `{"auth_mode":"apikey"}`)
	assert.False(t, shouldRefresh(authFile, keepaliveTestInterval))
}

func TestKeepalive_SkipsWhenLastRefreshFresh(t *testing.T) {
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	authFile := writeAuthJSON(t, t.TempDir(),
		`{"auth_mode":"chatgpt","last_refresh":"`+fresh+`"}`)
	assert.False(t, shouldRefresh(authFile, keepaliveTestInterval))
}

func TestKeepalive_TriggersWhenLastRefreshStale(t *testing.T) {
	stale := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name     string
		contents string
	}{
		{
			name:     "stale last_refresh",
			contents: `{"auth_mode":"chatgpt","last_refresh":"` + stale + `"}`,
		},
		{
			name:     "nil last_refresh",
			contents: `{"auth_mode":"chatgpt"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authFile := writeAuthJSON(t, t.TempDir(), tt.contents)
			assert.True(t, shouldRefresh(authFile, keepaliveTestInterval))
		})
	}
}

func TestKeepalive_SkipsWhenInvalidJSON(t *testing.T) {
	authFile := writeAuthJSON(t, t.TempDir(), `not json`)
	assert.False(t, shouldRefresh(authFile, keepaliveTestInterval))
}

func TestKeepalive_IntervalOverride(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "default when unset", env: "", want: keepaliveTestInterval},
		{name: "honors valid override", env: "1m", want: time.Minute},
		{name: "falls back on invalid value", env: "nonsense", want: keepaliveTestInterval},
		{name: "falls back on non-positive value", env: "0s", want: keepaliveTestInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERD_CODEX_KEEPALIVE_INTERVAL", tt.env)
			assert.Equal(t, tt.want, keepaliveInterval())
		})
	}
}

func TestKeepalive_CodexHome(t *testing.T) {
	t.Run("honors CODEX_HOME", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "/custom/codex")
		assert.Equal(t, "/custom/codex", keepaliveCodexHome())
	})
	t.Run("falls back to home dir", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".codex"), keepaliveCodexHome())
	})
}

func TestEntrypoint_SpawnsKeepaliveWhenAuthJsonPresent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	script := string(data)

	guard := `[ -f /home/runner/.codex/auth.json ]`
	assert.Contains(t, script, guard, "entrypoint must guard on the presence of ~/.codex/auth.json")
	assert.Contains(t, script, "herd codex keepalive-loop",
		"entrypoint must spawn the keepalive loop")

	guardIdx := strings.Index(script, guard)
	execIdx := strings.Index(script, "exec ./run.sh")
	require.NotEqual(t, -1, guardIdx)
	require.NotEqual(t, -1, execIdx)
	assert.Less(t, guardIdx, execIdx,
		"keepalive guard block must appear before 'exec ./run.sh'")
}

// TestEntrypoint_KeepaliveGuardIsOnDiskCheck asserts the keepalive guard is an
// on-disk file check rather than the old CODEX_AUTH_JSON env grep.
func TestEntrypoint_KeepaliveGuardIsOnDiskCheck(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	script := string(data)
	// New guard is an on-disk file check, not an env grep.
	assert.Contains(t, script, "[ -f /home/runner/.codex/auth.json ]",
		"entrypoint must guard the keepalive on the presence of auth.json")
	assert.NotContains(t, script, "grep -qE '^CODEX_AUTH_JSON",
		"the old CODEX_AUTH_JSON env guard must be gone")
}

// TestEntrypoint_AuthJsonGuardSemantics exercises the actual `[ -f ... ]` shell
// condition for the present and absent cases to confirm both branches behave.
func TestEntrypoint_AuthJsonGuardSemantics(t *testing.T) {
	tests := []struct {
		name      string
		create    bool
		wantMatch bool
	}{
		{name: "auth.json present -> guard matches", create: true, wantMatch: true},
		{name: "auth.json absent -> guard does not match", create: false, wantMatch: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			authFile := filepath.Join(dir, "auth.json")
			if tt.create {
				require.NoError(t, os.WriteFile(authFile, []byte("{}"), 0o600))
			}
			cmd := exec.Command("test", "-f", authFile)
			err := cmd.Run()
			matched := err == nil
			assert.Equal(t, tt.wantMatch, matched)
		})
	}
}

func TestKeepalive_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runKeepaliveLoop(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runKeepaliveLoop did not return after context cancellation")
	}
}
