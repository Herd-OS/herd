package claude

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

// Review runs the configured agent in headless mode to review a diff.
// The agent checks acceptance criteria and looks for issues.
// Returns a structured review result parsed from the agent's JSON output.
func (c *ClaudeAgent) Review(ctx context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	reviewPrompt, err := prompt.RenderReviewPrompt(diff, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering review prompt: %w", err)
	}

	// Pass prompt via stdin to avoid "argument list too long" on large diffs.
	// NOTE: Claude Code's headless `-p` mode does not currently accept a
	// --disallowed-tools flag. Until it does, we rely on the strict output
	// contract in prompt.ReviewSystemPrompt and the self-check section in
	// the user prompt to prevent the agent from taking action during review.
	// If a future Claude Code version exposes such a flag, pass it here
	// (e.g. --disallowed-tools=Bash,gh,Write,Edit) for defense in depth.
	args := []string{"--dangerously-skip-permissions", "--system-prompt", prompt.ReviewSystemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "-p")

	runOnce := func() (string, string, error) {
		cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
		cmd.Dir = opts.RepoRoot
		cmd.Stdin = strings.NewReader(reviewPrompt)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

		if err := cmd.Run(); err != nil {
			return "", stderr.String(), fmt.Errorf("agent review exited with error: %w\n%s", err, stderr.String())
		}
		return stdout.String(), stderr.String(), nil
	}

	stdout, stderr, err := runOnce()
	if err != nil {
		return nil, err
	}

	if prompt.IsSuspiciousOutput(stdout) {
		fmt.Printf("Review agent returned suspicious output (len=%d), retrying in %s...\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(stdout)), prompt.RetryDelay, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		time.Sleep(prompt.RetryDelay)

		stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if prompt.IsSuspiciousOutput(stdout) {
			return nil, fmt.Errorf("review agent returned suspicious output after retry: stdout=%q stderr=%q",
				strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	result, err := prompt.ParseReviewOutput(stdout)
	if err != nil {
		// If JSON parsing fails, treat as non-approved with raw output
		return &agent.ReviewResult{
			Approved:      false,
			IsUnparseable: true,
			Summary:       fmt.Sprintf("Failed to parse agent output as JSON: %s\nRaw output: %s", err, stdout),
		}, nil
	}

	return result, nil
}
