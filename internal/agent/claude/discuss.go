package claude

import (
	"context"
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/process"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Discuss launches Claude Code in interactive mode with a caller-supplied
// system prompt. There is no structured output — the agent runs to natural
// exit (user closes the session). Returns an error only if the agent process
// itself fails to start or exits non-zero.
func (c *ClaudeAgent) Discuss(ctx context.Context, opts agent.DiscussOptions) error {
	if opts.SystemPrompt == "" {
		return fmt.Errorf("discuss: system prompt is required")
	}

	promptFile, err := prompt.WriteSystemPromptFile(opts.SystemPrompt)
	if err != nil {
		return fmt.Errorf("discuss: writing system prompt file: %w", err)
	}
	defer func() { _ = os.Remove(promptFile) }()

	args := buildDiscussArgs(c, opts, promptFile)

	if err := process.Run(ctx, process.Command{
		Path:   c.BinaryPath,
		Args:   args,
		Dir:    opts.RepoRoot,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}); err != nil {
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
