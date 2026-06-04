package codex

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscuss_RequiresSystemPrompt(t *testing.T) {
	a := NewAgent("codex", "", "")
	err := a.Discuss(context.Background(), agent.DiscussOptions{RepoRoot: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system prompt is required")
}

func TestDiscuss_Args(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name          string
		model         string
		wantArgs      []string
		wantNotInArgs []string
	}{
		{
			name:          "no model",
			model:         "",
			wantArgs:      nil,
			wantNotInArgs: []string{"--model", "exec"},
		},
		{
			name:          "with model",
			model:         "gpt-5-codex",
			wantArgs:      []string{"--model", "gpt-5-codex"},
			wantNotInArgs: []string{"exec"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary, argvDump, _ := writeFakeCodex(t, "", "", 0)

			a := NewAgent(binary, tc.model, "")
			err := a.Discuss(context.Background(), agent.DiscussOptions{
				RepoRoot:     t.TempDir(),
				SystemPrompt: "you are a helpful assistant",
			})
			require.NoError(t, err)

			argv := readArgvDump(t, argvDump)
			for _, want := range tc.wantArgs {
				assert.Contains(t, argv, want)
			}
			for _, notWant := range tc.wantNotInArgs {
				assert.NotContains(t, argv, notWant,
					"interactive Discuss must NOT use %q", notWant)
			}
			if tc.model != "" {
				assert.True(t, argvHasFlagValue(argv, "--model", tc.model))
			}
		})
	}
}

// TestDiscuss_WiresStdio verifies that Discuss connects the child process's
// stdin/stdout/stderr to the parent's, with no output parsing.
func TestDiscuss_WiresStdio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	stdinCapture := filepath.Join(dir, "stdin.txt")
	binary := filepath.Join(dir, "codex.sh")
	// Fake codex copies its stdin to a file and writes a line to stdout.
	script := "#!/bin/sh\ncat > '" + stdinCapture + "'\nprintf '%s\\n' 'codex-tui-output-marker'\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))

	// Swap os.Stdin to feed input and os.Stdout to capture output.
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = inR
	os.Stdout = outW

	go func() {
		_, _ = inW.WriteString("user typed this\n")
		_ = inW.Close()
	}()

	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(outR)
		captured <- string(data)
	}()

	a := NewAgent(binary, "", "")
	derr := a.Discuss(context.Background(), agent.DiscussOptions{
		RepoRoot:     dir,
		SystemPrompt: "system",
	})
	_ = outW.Close()
	os.Stdin, os.Stdout = origStdin, origStdout

	require.NoError(t, derr)

	// Child's stdin came from the parent's (swapped) os.Stdin.
	stdinData, readErr := os.ReadFile(stdinCapture)
	require.NoError(t, readErr)
	assert.Equal(t, "user typed this\n", string(stdinData),
		"Discuss must wire the child's stdin to os.Stdin")

	// Child's stdout reached the parent's (swapped) os.Stdout.
	out := <-captured
	assert.Contains(t, out, "codex-tui-output-marker",
		"Discuss must wire the child's stdout to os.Stdout")
}

func TestDiscuss_FailingCommand(t *testing.T) {
	a := NewAgent("false", "", "")
	err := a.Discuss(context.Background(), agent.DiscussOptions{
		RepoRoot:     t.TempDir(),
		SystemPrompt: "system",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex exited with error")
}
