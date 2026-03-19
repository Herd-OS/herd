package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecute_CommandArgs(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		systemPrompt string
		body         string
		wantArgs     []string // expected in the args output line
		wantStdin    string   // expected prompt on stdin
	}{
		{
			name:         "system prompt replaces body",
			body:         "do the thing",
			systemPrompt: "you are a worker",
			wantArgs:     []string{"-p"},
			wantStdin:    "you are a worker",
		},
		{
			name:         "with model",
			model:        "opus",
			body:         "task body",
			systemPrompt: "prompt",
			wantArgs:     []string{"-p", "--model", "opus"},
			wantStdin:    "prompt",
		},
		{
			name:      "no system prompt uses body",
			body:      "task body",
			wantArgs:  []string{"-p"},
			wantStdin: "task body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := dir + "/test-agent.sh"
			// Print args on first line, then stdin content
			err := os.WriteFile(script, []byte("#!/bin/sh\necho \"ARGS:$@\"\necho \"STDIN:$(cat)\""), 0755)
			require.NoError(t, err)

			a := New(script, tt.model)
			task := agent.TaskSpec{Body: tt.body}
			opts := agent.ExecOptions{
				RepoRoot:     dir,
				SystemPrompt: tt.systemPrompt,
			}

			result, err := a.Execute(context.Background(), task, opts)
			require.NoError(t, err)
			for _, want := range tt.wantArgs {
				assert.Contains(t, result.Summary, want)
			}
			assert.Contains(t, result.Summary, "STDIN:"+tt.wantStdin)
		})
	}
}

func TestExecute_YAMLFrontmatterPrompt(t *testing.T) {
	tests := []struct {
		name  string
		body  string
	}{
		{
			name: "body starts with YAML frontmatter delimiters",
			body: "---\nbatch: 1\ndepends_on: []\n---\n\n## Task\nBuild the login page",
		},
		{
			name: "body starts with triple dash and spaces",
			body: "---  \nkey: value\n---\nDo the work",
		},
		{
			name: "body with multiple YAML documents",
			body: "---\nfirst: doc\n---\ncontent\n---\nsecond: doc\n---\nmore content",
		},
		{
			name: "body starts with dashes that look like flags",
			body: "---dangerously-skip-permissions\n--model opus\n-p something",
		},
		{
			name: "body is just triple dashes",
			body: "---",
		},
		{
			name: "body with leading newline then frontmatter",
			body: "\n---\nbatch: 1\n---\nTask content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := dir + "/test-agent.sh"
			// Capture stdin verbatim with a prefix to avoid triggering suspicious output detection
			err := os.WriteFile(script, []byte("#!/bin/sh\necho -n 'STDIN_CONTENT_RECEIVED:'\ncat"), 0755)
			require.NoError(t, err)

			a := New(script, "")
			task := agent.TaskSpec{Body: tt.body}
			opts := agent.ExecOptions{RepoRoot: dir}

			result, err := a.Execute(context.Background(), task, opts)
			require.NoError(t, err, "prompt starting with %q should not cause a CLI error", tt.body[:min(len(tt.body), 20)])
			assert.Equal(t, "STDIN_CONTENT_RECEIVED:"+tt.body, result.Summary, "prompt should arrive via stdin verbatim")
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestExecute_MaxTurns(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\ncat > /dev/null"), 0755)
	require.NoError(t, err)

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{
		RepoRoot: dir,
		MaxTurns: 200,
	}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "--max-turns 200")
	assert.Contains(t, result.Summary, "--dangerously-skip-permissions")
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
	a := New("nonexistent-binary-xyz", "")
	task := agent.TaskSpec{Body: "test"}
	opts := agent.ExecOptions{RepoRoot: t.TempDir()}

	_, err := a.Execute(context.Background(), task, opts)
	assert.Error(t, err)
}

func TestExecute_CapturesOutput(t *testing.T) {
	// Create a script that outputs to stdout
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'task completed successfully'"), 0755)
	require.NoError(t, err)

	// Verify sh is available
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, err := a.Execute(context.Background(), task, opts)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "task completed successfully")
}

func TestIsSuspiciousOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty string", "", true},
		{"whitespace only", "   \n  ", true},
		{"execution error", "Execution error", true},
		{"execution error mixed case", "execution error", true},
		{"execution error with whitespace", "  Execution error  \n", true},
		{"short single line", "Error", true},
		{"short no newline under threshold", "Something bad", true},
		{"valid short with newline", "line1\nline2", false},
		{"valid long output", "This is a real agent summary that describes work done on the task", false},
		{"exactly at threshold single line", strings.Repeat("x", minValidOutputLen), false},
		{"below threshold single line", strings.Repeat("x", minValidOutputLen-1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSuspiciousOutput(tt.output))
		})
	}
}

func TestExecute_SuspiciousOutputReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'Execution error'"), 0755)
	require.NoError(t, err)

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	_, execErr := a.Execute(context.Background(), task, opts)
	assert.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "suspicious output after retry")
}

func TestExecute_EmptyOutputReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null"), 0755)
	require.NoError(t, err)

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	_, execErr := a.Execute(context.Background(), task, opts)
	assert.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "suspicious output after retry")
}

func TestExecute_RetrySucceedsOnSecondAttempt(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	marker := dir + "/attempt"
	err := os.WriteFile(script, []byte(fmt.Sprintf(`#!/bin/sh
cat > /dev/null
if [ -f "%s" ]; then
  echo "Task completed successfully with detailed output"
else
  touch "%s"
  echo "Execution error"
fi
`, marker, marker)), 0755)
	require.NoError(t, err)

	a := New(script, "")
	task := agent.TaskSpec{Body: "do work"}
	opts := agent.ExecOptions{RepoRoot: dir}

	result, execErr := a.Execute(context.Background(), task, opts)
	require.NoError(t, execErr)
	assert.Contains(t, result.Summary, "Task completed successfully")
}
