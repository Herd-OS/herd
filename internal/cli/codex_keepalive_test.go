package cli

import (
	"context"
	"errors"
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

func TestEntrypoint_SpawnsKeepaliveWhenSubscriptionEnvSet(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	script := string(data)

	guard := `env | grep -qE '^CODEX_AUTH_JSON=.'`
	assert.Contains(t, script, guard, "entrypoint must guard on a non-empty CODEX_AUTH_JSON env var")
	assert.Contains(t, script, "herd codex keepalive-loop",
		"entrypoint must spawn the keepalive loop")

	guardIdx := strings.Index(script, guard)
	execIdx := strings.Index(script, "exec ./run.sh")
	require.NotEqual(t, -1, guardIdx)
	require.NotEqual(t, -1, execIdx)
	assert.Less(t, guardIdx, execIdx,
		"keepalive guard block must appear before 'exec ./run.sh'")
}

// TestEntrypoint_KeepaliveGuardSemantics exercises the actual grep pattern from
// the entrypoint against representative environments. This catches the
// empty-value case that the compose template always renders (CODEX_AUTH_JSON=)
// which the textual guard-string check cannot detect.
func TestEntrypoint_KeepaliveGuardSemantics(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	guard := extractKeepaliveGuardPattern(t, string(data))

	tests := []struct {
		name      string
		env       []string
		wantMatch bool
	}{
		{
			name:      "unset — var absent entirely",
			env:       []string{"PATH=/usr/bin"},
			wantMatch: false,
		},
		{
			name:      "empty — compose renders CODEX_AUTH_JSON= when unset",
			env:       []string{"CODEX_AUTH_JSON="},
			wantMatch: false,
		},
		{
			name:      "non-empty subscription seed",
			env:       []string{"CODEX_AUTH_JSON=eyAidG9rZW4iOiAiYWJjIiB9"},
			wantMatch: true,
		},
		{
			name:      "non-bare var starting with CODEX_AUTH_JSON does not match",
			env:       []string{"CODEX_AUTH_JSONX=seed"},
			wantMatch: false,
		},
		{
			name:      "only enterprise access token set",
			env:       []string{"CODEX_ACCESS_TOKEN=tok", "CODEX_AUTH_JSON="},
			wantMatch: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := runGuardPattern(t, guard, tt.env)
			assert.Equal(t, tt.wantMatch, matched)
		})
	}
}

// extractKeepaliveGuardPattern pulls the `grep -qE '<pattern>'` argument out of
// the entrypoint's keepalive guard line so the test runs the real pattern.
func extractKeepaliveGuardPattern(t *testing.T, script string) string {
	t.Helper()
	const marker = `grep -qE '`
	start := strings.Index(script, marker)
	require.NotEqual(t, -1, start, "entrypoint must use grep -qE for the keepalive guard")
	rest := script[start+len(marker):]
	end := strings.IndexByte(rest, '\'')
	require.NotEqual(t, -1, end, "keepalive guard pattern must be single-quoted")
	return rest[:end]
}

// runGuardPattern feeds env (one VAR=VALUE per line) into `grep -qE <pattern>`
// and reports whether grep matched (exit 0).
func runGuardPattern(t *testing.T, pattern string, env []string) bool {
	t.Helper()
	cmd := exec.Command("grep", "-qE", pattern)
	cmd.Stdin = strings.NewReader(strings.Join(env, "\n") + "\n")
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// grep exits 1 on no match — a clean "no match", not a test failure.
		require.Equal(t, 1, exitErr.ExitCode(), "grep failed unexpectedly: %v", err)
		return false
	}
	require.NoError(t, err, "running grep guard pattern")
	return false
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
