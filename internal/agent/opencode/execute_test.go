package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecute_CommandArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name          string
		model         string
		systemPrompt  string
		body          string
		wantPrompt    string   // the prompt that should arrive on stdin
		wantArgs      []string // flags expected in argv
		wantNotInArgs []string // flags that must NOT appear in argv
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
			// Fake binary captures argv and stdin separately, then emits
			// >20 chars of multi-line output so it passes the
			// suspicious-output filter.
			argvDump := dir + "/argv.bin"
			stdinDump := dir + "/stdin.txt"
			script := dir + "/opencode.sh"
			content := fmt.Sprintf(
				"#!/bin/sh\nprintf '%%s\\0' \"$@\" > '%s'\ncat > '%s'\necho 'task completed successfully with detail'\n",
				argvDump, stdinDump,
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

			// The prompt must NOT appear in argv at all — it goes via stdin
			// to avoid OS ARG_MAX limits.
			for _, a := range argv {
				assert.NotEqual(t, tc.wantPrompt, a,
					"prompt must not appear in argv; got argv element %q", a)
			}

			// The prompt MUST appear on stdin.
			stdinBytes, err := os.ReadFile(stdinDump)
			require.NoError(t, err)
			assert.Equal(t, tc.wantPrompt, string(stdinBytes),
				"prompt must be piped via stdin")
		})
	}
}

// TestExecute_PassesPromptViaStdin verifies that Execute pipes the prompt
// via the child process's stdin instead of argv — matching the claude
// provider and avoiding OS ARG_MAX limits on large prompts.
func TestExecute_PassesPromptViaStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	stdinDump := dir + "/stdin.txt"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(
		"#!/bin/sh\ncat > '%s'\necho 'execution succeeded with multiline output\\nmore content here'\n",
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
	assert.Equal(t, "do the thing", string(data),
		"Execute must pipe the prompt to the child's stdin")
}

// TestExecute_LargePromptDoesNotExceedArgMax verifies that a ~200KB prompt
// does not produce an "argument list too long" exec error — the prompt is
// piped via stdin, so argv stays small regardless of prompt size.
func TestExecute_LargePromptDoesNotExceedArgMax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	stdinDump := dir + "/stdin.txt"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(
		"#!/bin/sh\ncat > '%s'\necho 'task completed successfully with detailed output'\n",
		stdinDump,
	)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	// ~200KB prompt — well above typical ARG_MAX limits when passed via argv.
	largePrompt := strings.Repeat("This is one long line of prompt content. ", 5000)
	require.Greater(t, len(largePrompt), 200_000,
		"sanity check: large prompt must exceed 200KB to exercise the ARG_MAX guard")

	a := New(script, "")
	task := agent.TaskSpec{Body: largePrompt}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err, "Execute must succeed with a 200KB+ prompt")
	require.NotNil(t, result)

	// Stub received the full prompt via stdin.
	data, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Equal(t, largePrompt, string(data),
		"stub must have received the full prompt via stdin")
}

func TestExecute_MaxTurnsIsIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	argvDump := dir + "/argv.bin"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\0' \"$@\" > '%s'\ncat > /dev/null\necho 'done with enough output here'\n",
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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'task completed successfully'\n"), 0o755))

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "task completed successfully")
}

func TestExecute_UnixContextCancellationTerminatesDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group termination is Unix-only")
	}

	dir := t.TempDir()
	pidFile := dir + "/child.pid"
	readyFile := dir + "/ready"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
cat > /dev/null
(sleep 60) &
child=$!
printf '%%s' "$child" > %s
touch %s
wait "$child"
`, shellQuote(pidFile), shellQuote(readyFile))
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	a := New(script, "")
	_, err := a.Execute(ctx, agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: dir})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.FileExists(t, readyFile)
	pid := readPIDFile(t, pidFile)
	require.Eventually(t, func() bool {
		return !processAlive(pid)
	}, 3*time.Second, 25*time.Millisecond)
}

func TestExecute_SuspiciousOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'Execution error'\n"), 0o755))

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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\nexit 0\n"), 0o755))

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
cat > /dev/null
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
