package codex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	agentprocess "github.com/herd-os/herd/internal/agent/process"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Review runs Codex in headless mode (`codex exec`) to review a diff using
// native structured output. Because Codex exec has no --system-prompt flag, the
// review system prompt is folded into the positional message (like opencode).
// An embedded JSON Schema is passed via --output-schema and the final JSON
// message is written via --output-last-message.
//
// Returns a structured review result parsed from the agent's JSON output;
// unparseable output yields ReviewResult{Approved:false, IsUnparseable:true}
// with a Summary beginning "Failed to parse".
func (c *CodexAgent) Review(ctx context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	reviewPrompt, err := prompt.RenderReviewPrompt(diff, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering review prompt: %w", err)
	}

	message := prompt.ReviewSystemPrompt + "\n\n" + reviewPrompt

	schemaFile, err := writeSchemaFile("review.json")
	if err != nil {
		return nil, fmt.Errorf("review: writing schema file: %w", err)
	}
	defer func() { _ = os.Remove(schemaFile) }()

	outFile, err := os.CreateTemp("", "codex-review-*.json")
	if err != nil {
		return nil, fmt.Errorf("review: creating output temp file: %w", err)
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer func() { _ = os.Remove(outPath) }()

	args := c.buildExecBaseArgs()
	args = append(args, "--output-schema", schemaFile, "--output-last-message", outPath, message)

	// runOnce returns the final agent message (file contents, falling back to
	// stdout when the file is empty) along with stdout/stderr for diagnostics.
	runOnce := func() (finalMsg, stdout, stderr string, err error) {
		var outBuf, errBuf bytes.Buffer
		runErr := agentprocess.Run(ctx, agentprocess.Command{
			Path:   c.BinaryPath,
			Args:   args,
			Dir:    opts.RepoRoot,
			Env:    childEnv(),
			Stdout: io.MultiWriter(os.Stdout, &outBuf),
			Stderr: io.MultiWriter(os.Stderr, &errBuf),
		})
		if runErr != nil {
			return "", "", errBuf.String(), fmt.Errorf("agent review exited with error: %w\n%s", runErr, errBuf.String())
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
		fmt.Printf("Review agent returned suspicious output (len=%d), retrying in %s...\nfinal: %s\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(finalMsg)), prompt.RetryDelay, strings.TrimSpace(finalMsg), strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		time.Sleep(prompt.RetryDelay)

		finalMsg, stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if prompt.IsSuspiciousOutput(finalMsg) {
			return nil, fmt.Errorf("review agent returned suspicious output after retry: final=%q stdout=%q stderr=%q",
				strings.TrimSpace(finalMsg), strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	result, err := prompt.ParseReviewOutput(finalMsg)
	if err != nil {
		// If JSON parsing fails, treat as non-approved with raw output.
		return &agent.ReviewResult{
			Approved:      false,
			IsUnparseable: true,
			Summary:       fmt.Sprintf("Failed to parse agent output as JSON: %s\nRaw output: %s", err, finalMsg),
		}, nil
	}

	return result, nil
}
