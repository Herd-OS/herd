package claude

import (
	"context"
	"os"
	"os/exec"
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
		wantContains []string
	}{
		{
			name:         "system prompt replaces body as -p value",
			body:         "do the thing",
			systemPrompt: "you are a worker",
			wantContains: []string{"-p", "you are a worker"},
		},
		{
			name:         "with model",
			model:        "opus",
			body:         "task body",
			systemPrompt: "prompt",
			wantContains: []string{"-p", "prompt", "--model", "opus"},
		},
		{
			name:         "no system prompt uses body",
			body:         "task body",
			wantContains: []string{"-p", "task body"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a script that prints all args so we can verify
			dir := t.TempDir()
			script := dir + "/test-agent.sh"
			err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\""), 0755)
			require.NoError(t, err)

			a := New(script, tt.model)
			task := agent.TaskSpec{Body: tt.body}
			opts := agent.ExecOptions{
				RepoRoot:     dir,
				SystemPrompt: tt.systemPrompt,
			}

			result, err := a.Execute(context.Background(), task, opts)
			require.NoError(t, err)
			for _, want := range tt.wantContains {
				assert.Contains(t, result.Summary, want)
			}
		})
	}
}

func TestExecute_MaxTurns(t *testing.T) {
	// Use a script that prints all args so we can verify --max-turns is passed
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\""), 0755)
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
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'task completed successfully'"), 0755)
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
