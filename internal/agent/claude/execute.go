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
)

const (
	// minValidOutputLen is the minimum stdout length for agent output to be
	// considered valid. Shorter single-line output is treated as suspicious
	// (e.g., "Execution error" from API failures).
	minValidOutputLen = 20

	// retryDelay is the wait time before retrying after suspicious output.
	retryDelay = 5 * time.Second
)

// isSuspiciousOutput returns true if the agent's stdout looks like an error
// rather than real work output. This catches cases where Claude's API returns
// exit code 0 with just "Execution error" or similar short error messages.
func isSuspiciousOutput(stdout string) bool {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return true
	}
	if strings.EqualFold(trimmed, "Execution error") {
		return true
	}
	// Short single-line output is suspicious — real agent work produces
	// multi-line summaries or at least a substantive single line.
	if len(trimmed) < minValidOutputLen && !strings.Contains(trimmed, "\n") {
		return true
	}
	return false
}

// Execute runs the configured agent in headless mode to complete a task.
// The task body is passed as the prompt (-p), and the system prompt provides
// worker instructions. The agent commits as it works in the repo.
// If the agent returns suspicious output (empty, "Execution error", or very
// short), Execute retries once after a 5s delay before returning an error.
func (c *ClaudeAgent) Execute(ctx context.Context, task agent.TaskSpec, opts agent.ExecOptions) (*agent.ExecResult, error) {
	prompt := task.Body
	if opts.SystemPrompt != "" {
		prompt = opts.SystemPrompt
	}

	debugFile := filepath.Join(opts.RepoRoot, "claude-debug.log")
	args := []string{"--dangerously-skip-permissions", "--debug", "--debug-file", debugFile}
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
		cmd.Stdin = strings.NewReader(prompt)

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

	if isSuspiciousOutput(stdout) {
		// Dump debug log if available
		if debugData, readErr := os.ReadFile(debugFile); readErr == nil && len(debugData) > 0 {
			fmt.Printf("=== Claude debug log (attempt 1) ===\n%s\n=== End debug log ===\n", string(debugData))
		}
		fmt.Printf("Agent returned suspicious output (len=%d), retrying in %s...\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(stdout)), retryDelay, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		_ = os.Remove(debugFile) // clear for retry
		time.Sleep(retryDelay)

		stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if isSuspiciousOutput(stdout) {
			// Dump debug log for retry attempt
			if debugData, readErr := os.ReadFile(debugFile); readErr == nil && len(debugData) > 0 {
				fmt.Printf("=== Claude debug log (attempt 2) ===\n%s\n=== End debug log ===\n", string(debugData))
			}
			return nil, fmt.Errorf("agent returned suspicious output after retry: stdout=%q stderr=%q",
				strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	return &agent.ExecResult{
		Summary: stdout,
	}, nil
}
