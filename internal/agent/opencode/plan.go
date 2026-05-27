package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Plan launches OpenCode in interactive TUI mode for a planning session.
// Because OpenCode has no --system-prompt flag, the rendered planning system
// prompt and the optional initialPrompt are folded together and passed via
// --prompt. After the agent exits, the plan JSON is read from opts.OutputPath.
func (o *OpenCodeAgent) Plan(ctx context.Context, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	systemPrompt, err := prompt.RenderPlanningPrompt(opts)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}

	combined := systemPrompt
	if initialPrompt != "" {
		combined += "\n\n" + initialPrompt
	}

	args := buildInteractiveArgs(o.Model, combined)

	cmd := exec.CommandContext(ctx, o.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("opencode exited with error: %w", err)
	}

	plan, err := prompt.ReadPlanFile(opts.OutputPath)
	if err != nil {
		return nil, err
	}
	return plan, nil
}
