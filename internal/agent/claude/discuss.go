package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
)

// Discuss launches Claude Code in interactive mode with a caller-supplied
// system prompt. There is no structured output — the agent runs to natural
// exit (user closes the session). Returns an error only if the agent process
// itself fails to start or exits non-zero.
func (c *ClaudeAgent) Discuss(ctx context.Context, opts agent.DiscussOptions) error {
	if opts.SystemPrompt == "" {
		return fmt.Errorf("discuss: system prompt is required")
	}

	args := []string{"--system-prompt", opts.SystemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if opts.InitialPrompt != "" {
		args = append(args, "--initial-prompt", opts.InitialPrompt)
	}

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude exited with error: %w", err)
	}
	return nil
}
