package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Plan launches Claude Code in interactive mode for a planning session.
// After the agent exits, it reads and parses the plan JSON from opts.OutputPath.
func (c *ClaudeAgent) Plan(ctx context.Context, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	systemPrompt, err := prompt.RenderPlanningPrompt(opts)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}

	promptFile, err := prompt.WriteSystemPromptFile(systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("plan: writing system prompt file: %w", err)
	}
	defer func() { _ = os.Remove(promptFile) }()

	args := buildPlanArgs(c, initialPrompt, promptFile)

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exited with error: %w", err)
	}

	plan, err := prompt.ReadPlanFile(opts.OutputPath)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// buildPlanArgs constructs the argv passed to the Claude Code binary for a
// Plan session. The initial prompt is appended as a positional argument after
// all flag args because Claude Code accepts the prompt only as a positional,
// not via a flag.
func buildPlanArgs(c *ClaudeAgent, initialPrompt, promptFile string) []string {
	args := []string{"--system-prompt-file", promptFile}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if initialPrompt != "" {
		args = append(args, initialPrompt)
	}
	return args
}
