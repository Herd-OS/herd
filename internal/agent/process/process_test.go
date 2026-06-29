package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_SuccessStreamsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is Unix-only")
	}

	script := writeShellScript(t, "io.sh", "printf 'hello stdout'\nprintf 'hello stderr' >&2\n")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Command{
		Path:   script,
		Stdout: &stdout,
		Stderr: &stderr,
	})

	require.NoError(t, err)
	assert.Equal(t, "hello stdout", stdout.String())
	assert.Equal(t, "hello stderr", stderr.String())
}

func TestRun_NonZeroExitReturnsProcessError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is Unix-only")
	}

	tests := []struct {
		name string
		code int
	}{
		{name: "exit one", code: 1},
		{name: "exit forty two", code: 42},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := writeShellScript(t, "fail.sh", fmt.Sprintf("exit %d\n", tc.code))

			err := Run(context.Background(), Command{Path: script})

			require.Error(t, err)
			var exitErr *exec.ExitError
			require.True(t, errors.As(err, &exitErr), "got %T: %v", err, err)
			assert.Equal(t, tc.code, exitErr.ExitCode())
			assert.False(t, errors.Is(err, context.Canceled))
		})
	}
}

func TestRun_PreCancelledContextDoesNotLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is Unix-only")
	}

	marker := filepath.Join(t.TempDir(), "marker")
	script := writeShellScript(t, "marker.sh", fmt.Sprintf("printf x > %s\n", shellQuote(marker)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, Command{Path: script})

	require.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "pre-cancelled Run must not launch the child")
}

func TestRun_UnixContextCancellationTerminatesDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group termination is Unix-only")
	}

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	readyFile := filepath.Join(dir, "ready")
	script := writeShellScript(t, "wrapper.sh", fmt.Sprintf(`
(sleep 60) &
child=$!
printf '%%s' "$child" > %s
touch %s
wait "$child"
`, shellQuote(pidFile), shellQuote(readyFile)))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := Run(ctx, Command{Path: script})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	pid := readPIDFile(t, pidFile)
	require.Eventually(t, func() bool {
		return !processAlive(pid)
	}, 3*time.Second, 25*time.Millisecond)
	assert.FileExists(t, readyFile)
}

func writeShellScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	content := "#!/bin/sh\n" + body
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	return pid
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run()
	return err == nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
