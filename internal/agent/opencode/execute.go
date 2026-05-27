package opencode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Execute runs the OpenCode CLI in headless mode to complete a task. The
// task body (or opts.SystemPrompt, if set) is passed as the final positional
// argument to `opencode run`. The agent commits as it works in the repo.
//
// If the agent returns suspicious output (empty, "Execution error", or very
// short), Execute retries once after prompt.RetryDelay before returning an
// error.
//
// Note: OpenCode has no --max-turns flag; opts.MaxTurns is ignored for this
// provider.
func (o *OpenCodeAgent) Execute(ctx context.Context, task agent.TaskSpec, opts agent.ExecOptions) (*agent.ExecResult, error) {
	taskPrompt := task.Body
	if opts.SystemPrompt != "" {
		taskPrompt = opts.SystemPrompt
	}

	args := buildRunArgs(o.Model, taskPrompt)

	runOnce := func() (string, string, error) {
		cmd := exec.CommandContext(ctx, o.BinaryPath, args...)
		cmd.Dir = opts.RepoRoot
		// Do not set cmd.Stdin — OpenCode `run` takes the prompt as a
		// positional argument, not on stdin.

		var stdout, stderr bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

		if err := cmd.Run(); err != nil {
			return "", stderr.String(), fmt.Errorf("agent exited with error: %w\n%s", err, stderr.String())
		}
		return stdout.String(), stderr.String(), nil
	}

	stdout, stderr, err := runOnce()
	if err != nil {
		return nil, err
	}

	if prompt.IsSuspiciousOutput(stdout) {
		fmt.Printf("Agent returned suspicious output (len=%d), retrying in %s...\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(stdout)), prompt.RetryDelay, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		time.Sleep(prompt.RetryDelay)

		stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if prompt.IsSuspiciousOutput(stdout) {
			return nil, fmt.Errorf("agent returned suspicious output after retry: stdout=%q stderr=%q",
				strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	return &agent.ExecResult{
		Summary: stdout,
	}, nil
}
