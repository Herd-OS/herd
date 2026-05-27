package opencode

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecute_CommandArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name           string
		model          string
		systemPrompt   string
		body           string
		wantPrompt     string   // the prompt that should appear as the final positional
		wantArgs       []string // additional flags expected in argv
		wantNotInArgs  []string // flags that must NOT appear in argv
	}{
		{
			name:          "system prompt overrides body",
			body:          "do the thing",
			systemPrompt:  "you are a worker",
			wantPrompt:    "you are a worker",
			wantArgs:      []string{"run", "--dangerously-skip-permissions"},
			wantNotInArgs: []string{"--model"},
		},
		{
			name:          "with model",
			model:         "anthropic/claude-sonnet-4",
			body:          "task body",
			systemPrompt:  "prompt",
			wantPrompt:    "prompt",
			wantArgs:      []string{"run", "--dangerously-skip-permissions", "--model", "anthropic/claude-sonnet-4"},
			wantNotInArgs: nil,
		},
		{
			name:          "no system prompt uses body",
			body:          "task body",
			wantPrompt:    "task body",
			wantArgs:      []string{"run", "--dangerously-skip-permissions"},
			wantNotInArgs: []string{"--model"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Fake binary emits >20 chars of multi-line output so it passes
			// the suspicious-output filter, and records its argv to a file.
			argvDump := dir + "/argv.bin"
			script := dir + "/opencode.sh"
			content := fmt.Sprintf(
				"#!/bin/sh\nprintf '%%s\\0' \"$@\" > '%s'\necho 'task completed successfully with detail'\n",
				argvDump,
			)
			require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

			a := New(script, tc.model)
			task := agent.TaskSpec{Body: tc.body}
			opts := agent.ExecOptions{
				RepoRoot:     dir,
				SystemPrompt: tc.systemPrompt,
			}

			result, err := a.Execute(context.Background(), task, opts)
			require.NoError(t, err)
			require.NotNil(t, result)

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv)

			for _, want := range tc.wantArgs {
				assert.Contains(t, argv, want, "argv missing expected flag %q", want)
			}
			for _, notWant := range tc.wantNotInArgs {
				assert.NotContains(t, argv, notWant)
			}

			// `run` MUST be the first argv element.
			assert.Equal(t, "run", argv[0], "run subcommand must be first")

			// Prompt must be the final positional argument.
			assert.Equal(t, tc.wantPrompt, argv[len(argv)-1],
				"prompt must be the final positional argument")
		})
	}
}

// TestExecute_DoesNotConsumeStdin verifies that Execute does NOT write to
// the child process's stdin (unlike the claude provider).
func TestExecute_DoesNotConsumeStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	stdinDump := dir + "/stdin.txt"
	script := dir + "/opencode.sh"
	// Script captures whatever (if anything) it reads from stdin, but
	// only with a short timeout — if Execute does not write stdin and we
	// try to `cat` it the script will block forever, so we use head -c with
	// a non-blocking approach: read from /dev/stdin with a redirect that
	// returns immediately if the pipe is closed.
	content := fmt.Sprintf(
		"#!/bin/sh\n# If stdin is a pipe and producer doesn't write, read returns EOF.\n"+
			"cat > '%s' 2>/dev/null || true\n"+
			"echo 'execution succeeded with multiline output\\nmore content here'\n",
		stdinDump,
	)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do the thing"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)
	require.NotNil(t, result)

	data, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Empty(t, string(data),
		"Execute must NOT write to stdin; cat should have read EOF immediately")
}

func TestExecute_MaxTurnsIsIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	argvDump := dir + "/argv.bin"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\0' \"$@\" > '%s'\necho 'done with enough output here'\n",
		argvDump,
	)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{
		RepoRoot: dir,
		MaxTurns: 200,
	}

	_, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)
	// OpenCode has no --max-turns flag; this provider must not pass it.
	assert.NotContains(t, argv, "--max-turns",
		"opencode has no --max-turns flag; opts.MaxTurns must be ignored")
	for _, a := range argv {
		assert.NotEqual(t, "200", a, "MaxTurns numeric value must not appear in argv")
	}
}

func TestExecute_FailingCommand(t *testing.T) {
	a := New("false", "") // "false" always exits with code 1
	task := agent.TaskSpec{Body: "test"}
	opts := agent.ExecOptions{RepoRoot: t.TempDir()}

	_, err := a.Execute(context.Background(), task, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent exited with error")
}

func TestExecute_MissingBinary(t *testing.T) {
	a := New("nonexistent-opencode-binary-xyz", "")
	task := agent.TaskSpec{Body: "test"}
	opts := agent.ExecOptions{RepoRoot: t.TempDir()}

	_, err := a.Execute(context.Background(), task, opts)
	assert.Error(t, err)
}

func TestExecute_CapturesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'task completed successfully'\n"), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "task completed successfully")
}

func TestExecute_SuspiciousOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'Execution error'\n"), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	_, execErr := a.Execute(context.Background(), task, opts)
	assert.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "suspicious output after retry")
}

func TestExecute_EmptyOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	_, execErr := a.Execute(context.Background(), task, opts)
	assert.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "suspicious output after retry")
}

func TestExecute_RetrySucceedsOnSecondAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	marker := dir + "/attempt"
	content := fmt.Sprintf(`#!/bin/sh
if [ -f "%s" ]; then
  echo "Task completed successfully with detailed output"
else
  touch "%s"
  echo "Execution error"
fi
`, marker, marker)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, execErr := a.Execute(context.Background(), task, opts)
	require.NoError(t, execErr)
	assert.Contains(t, result.Summary, "Task completed successfully")
}
