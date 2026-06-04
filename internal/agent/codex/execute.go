package codex

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

// Execute runs Codex in headless mode (`codex exec`) to complete a task. The
// task body (or opts.SystemPrompt, if set) is passed as the positional prompt.
// The final agent message is captured via --output-last-message and returned
// in ExecResult.Summary; the agent commits as it works in the repo.
//
// If the final message looks suspicious (empty, "Execution error", or very
// short), Execute retries once after prompt.RetryDelay before returning an
// error.
//
// Note: Codex has no --max-turns flag; opts.MaxTurns is ignored for this
// provider (mirroring opencode).
func (c *CodexAgent) Execute(ctx context.Context, task agent.TaskSpec, opts agent.ExecOptions) (*agent.ExecResult, error) {
	if err := ensureProvisioned(); err != nil {
		return nil, fmt.Errorf("codex auth provisioning: %w", err)
	}

	taskPrompt := task.Body
	if opts.SystemPrompt != "" {
		taskPrompt = opts.SystemPrompt
	}

	outFile, err := os.CreateTemp("", "codex-exec-*.txt")
	if err != nil {
		return nil, fmt.Errorf("creating output temp file: %w", err)
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer func() { _ = os.Remove(outPath) }()

	args := c.buildExecBaseArgs()
	args = append(args, "--output-last-message", outPath, taskPrompt)

	// runOnce returns the final agent message (file contents, falling back to
	// stdout when the file is empty) along with stdout/stderr for diagnostics.
	runOnce := func() (finalMsg, stdout, stderr string, err error) {
		cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
		cmd.Dir = opts.RepoRoot
		cmd.Env = childEnv()

		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &outBuf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)

		if runErr := cmd.Run(); runErr != nil {
			return "", "", errBuf.String(), fmt.Errorf("agent exited with error: %w\n%s", runErr, errBuf.String())
		}

		msg := outBuf.String()
		if data, readErr := os.ReadFile(outPath); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
			msg = string(data)
		}
		return msg, outBuf.String(), errBuf.String(), nil
	}

	finalMsg, stdout, stderr, err := runOnce()
	if err != nil {
		return nil, err
	}

	if prompt.IsSuspiciousOutput(finalMsg) {
		fmt.Printf("Agent returned suspicious output (len=%d), retrying in %s...\nfinal: %s\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(finalMsg)), prompt.RetryDelay, strings.TrimSpace(finalMsg), strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		time.Sleep(prompt.RetryDelay)

		finalMsg, stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if prompt.IsSuspiciousOutput(finalMsg) {
			return nil, fmt.Errorf("agent returned suspicious output after retry: final=%q stdout=%q stderr=%q",
				strings.TrimSpace(finalMsg), strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	return &agent.ExecResult{
		Summary: finalMsg,
	}, nil
}
