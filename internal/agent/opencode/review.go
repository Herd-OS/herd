package opencode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/process"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Review runs the OpenCode CLI in headless mode to review a diff. Because
// OpenCode has no --system-prompt flag, the review system prompt is
// prepended to the rendered review message; the combined message is piped
// via stdin so large diffs do not trip the OS ARG_MAX limit.
//
// Returns a structured review result parsed from the agent's JSON output;
// unparseable output yields ReviewResult{Approved:false, IsUnparseable:true}
// with a Summary beginning "Failed to parse".
func (o *OpenCodeAgent) Review(ctx context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	reviewPrompt, err := prompt.RenderReviewPrompt(diff, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering review prompt: %w", err)
	}

	message := prompt.ReviewSystemPrompt + "\n\n" + reviewPrompt
	args := buildRunArgs(o.Model)

	runOnce := func() (string, string, error) {
		// Pipe the combined message via stdin to avoid "argument list too
		// long" on large diffs. OpenCode `run` reads stdin when it is not
		// a TTY.
		var stdout, stderr bytes.Buffer
		if err := process.Run(ctx, process.Command{
			Path:         o.BinaryPath,
			Args:         args,
			Dir:          opts.RepoRoot,
			Stdin:        strings.NewReader(message),
			Stdout:       io.MultiWriter(os.Stdout, &stdout),
			Stderr:       io.MultiWriter(os.Stderr, &stderr),
			ProcessGroup: true,
		}); err != nil {
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
		return &agent.ReviewResult{
			Approved:      false,
			IsUnparseable: true,
			Summary:       fmt.Sprintf("Failed to parse agent output as JSON: %s\nRaw output: %s", err, stdout),
		}, nil
	}

	return result, nil
}
