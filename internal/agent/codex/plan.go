package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Plan runs Codex to produce a structured plan. Unlike the claude and opencode
// providers (which drive an interactive TUI), the Codex Plan uses the headless
// `codex exec` mode with native structured output: an embedded JSON Schema is
// passed via --output-schema and the final JSON message is written via
// --output-last-message, then parsed with prompt.ReadPlanFile.
//
// The rendered planning system prompt and the optional initialPrompt are folded
// together into the positional prompt (Codex exec has no --system-prompt flag).
func (c *CodexAgent) Plan(ctx context.Context, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	if err := ensureProvisioned(); err != nil {
		return nil, fmt.Errorf("codex auth provisioning: %w", err)
	}

	systemPrompt, err := prompt.RenderPlanningPrompt(opts)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}

	combined := systemPrompt
	if initialPrompt != "" {
		combined += "\n\n" + initialPrompt
	}

	schemaFile, err := writeSchemaFile("plan.json")
	if err != nil {
		return nil, fmt.Errorf("plan: writing schema file: %w", err)
	}
	defer func() { _ = os.Remove(schemaFile) }()

	outFile := opts.OutputPath
	if outFile == "" {
		f, ferr := os.CreateTemp("", "codex-plan-*.json")
		if ferr != nil {
			return nil, fmt.Errorf("plan: creating output temp file: %w", ferr)
		}
		outFile = f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(outFile) }()
	}

	args := c.buildExecBaseArgs()
	args = append(args, "--output-schema", schemaFile, "--output-last-message", outFile, combined)

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Env = childEnv()
	// Headless mode: stdin is not a TTY. The structured plan is read from the
	// output file, so just stream child stdout/stderr to the parent.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("codex exited with error: %w", err)
	}

	plan, err := prompt.ReadPlanFile(outFile)
	if err != nil {
		return nil, err
	}
	return plan, nil
}
