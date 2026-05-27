package claude

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Execute runs the configured agent in headless mode to complete a task.
// The task body is passed as the prompt (-p), and the system prompt provides
// worker instructions. The agent commits as it works in the repo.
// If the agent returns suspicious output (empty, "Execution error", or very
// short), Execute retries once after a 5s delay before returning an error.
func (c *ClaudeAgent) Execute(ctx context.Context, task agent.TaskSpec, opts agent.ExecOptions) (*agent.ExecResult, error) {
	taskPrompt := task.Body
	if opts.SystemPrompt != "" {
		taskPrompt = opts.SystemPrompt
	}

	args := []string{"--dangerously-skip-permissions"}
	debugFile := ""
	if os.Getenv("HERD_AGENT_DEBUG") == "true" {
		debugFile = filepath.Join(opts.RepoRoot, "claude-debug.log")
		args = append(args, "--debug", "--debug-file", debugFile)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	args = append(args, "-p")

	runOnce := func() (string, string, error) {
		cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
		cmd.Dir = opts.RepoRoot
		cmd.Stdin = strings.NewReader(taskPrompt)

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
		// Dump debug log if available
		if debugFile != "" {
			if debugData, readErr := os.ReadFile(debugFile); readErr == nil && len(debugData) > 0 {
				fmt.Printf("=== Claude debug log (attempt 1) ===\n%s\n=== End debug log ===\n", string(debugData))
			}
		}
		fmt.Printf("Agent returned suspicious output (len=%d), retrying in %s...\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(stdout)), prompt.RetryDelay, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		if debugFile != "" {
			_ = os.Remove(debugFile) // clear for retry
		}
		time.Sleep(prompt.RetryDelay)

		stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if prompt.IsSuspiciousOutput(stdout) {
			// Dump debug log for retry attempt
			if debugFile != "" {
				if debugData, readErr := os.ReadFile(debugFile); readErr == nil && len(debugData) > 0 {
					fmt.Printf("=== Claude debug log (attempt 2) ===\n%s\n=== End debug log ===\n", string(debugData))
				}
			}
			return nil, fmt.Errorf("agent returned suspicious output after retry: stdout=%q stderr=%q",
				strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	return &agent.ExecResult{
		Summary: stdout,
	}, nil
}
