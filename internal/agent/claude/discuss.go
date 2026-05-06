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

	promptFile, err := writeSystemPromptFile(opts.SystemPrompt)
	if err != nil {
		return fmt.Errorf("discuss: writing system prompt file: %w", err)
	}
	defer func() { _ = os.Remove(promptFile) }()

	args := buildDiscussArgs(c, opts, promptFile)

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

// buildDiscussArgs constructs the argv passed to the Claude Code binary for a
// Discuss session. The initial prompt is appended as a positional argument
// after all flag args because Claude Code accepts the prompt only as a
// positional, not via a flag.
func buildDiscussArgs(c *ClaudeAgent, opts agent.DiscussOptions, promptFile string) []string {
	args := []string{"--system-prompt-file", promptFile}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if opts.InitialPrompt != "" {
		args = append(args, opts.InitialPrompt)
	}
	return args
}
