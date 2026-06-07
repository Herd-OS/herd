package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
)

// Plan runs Codex to produce a structured plan. It operates in two modes,
// branching on whether initialPrompt is empty:
//
//   - Non-empty initialPrompt (headless): like before, the Codex `codex exec`
//     mode is used with native structured output. An embedded JSON Schema is
//     passed via --output-schema and the final JSON message is written via
//     --output-last-message, then parsed with prompt.ReadPlanFile. The rendered
//     planning system prompt and the initialPrompt are folded together into the
//     positional prompt (Codex exec has no --system-prompt flag).
//
//   - Empty initialPrompt (interactive): mirroring the claude and opencode
//     providers, the user is dropped into a conversational Codex session (the
//     default `codex` invocation, no `exec` subcommand) to refine the brief.
//     Codex's interactive TUI has no --system-prompt or --output-schema flag, so
//     the structured plan is collected out-of-band: the seed prompt instructs the
//     agent to write the final plan JSON to the path in the HERD_PLAN_OUT env var
//     (conforming to the schema at HERD_PLAN_SCHEMA). After the session exits the
//     plan is read back with prompt.ReadPlanFile.
//
// Both modes return the same *agent.Plan struct; only how the plan is collected
// differs.
func (c *CodexAgent) Plan(ctx context.Context, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	if err := ensureProvisioned(); err != nil {
		return nil, fmt.Errorf("codex auth provisioning: %w", err)
	}

	systemPrompt, err := prompt.RenderPlanningPrompt(opts)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}

	if initialPrompt == "" {
		return c.planInteractive(ctx, systemPrompt, opts)
	}
	return c.planHeadless(ctx, systemPrompt, initialPrompt, opts)
}

// planHeadless runs `codex exec` with native structured output. systemPrompt and
// initialPrompt are folded into a single positional prompt; the plan is written
// to an output file (opts.OutputPath or a temp file) via --output-last-message
// and parsed with prompt.ReadPlanFile.
func (c *CodexAgent) planHeadless(ctx context.Context, systemPrompt, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	combined := systemPrompt
	if initialPrompt != "" {
		combined += "\n\n" + initialPrompt
	}

	schemaFile, outFile, cleanup, err := c.allocatePlanArtifacts(opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()

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

// planInteractive drops the user into a conversational Codex session (the
// default `codex` invocation, no `exec` subcommand) with stdio wired to the
// user's TTY, then collects the structured plan from an out-of-band file.
//
// Collection approach (Approach 1): the seed positional prompt embeds the
// planning system prompt and instructs the agent to write the final plan JSON to
// the file named by the HERD_PLAN_OUT env var (conforming to the schema at
// HERD_PLAN_SCHEMA) once the user agrees on the plan, then exit. Both paths are
// passed to the child via the environment. After the session exits the plan is
// read back with prompt.ReadPlanFile; a missing/empty file surfaces as an error
// (the user likely exited without finalizing).
func (c *CodexAgent) planInteractive(ctx context.Context, systemPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	schemaFile, outFile, cleanup, err := c.allocatePlanArtifacts(opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	seed := systemPrompt + "\n\n" +
		"You are starting an interactive planning session. Converse with the user to " +
		"refine the feature brief into a concrete plan. When the user agrees on the " +
		"final plan, write the plan JSON to the file path given by the HERD_PLAN_OUT " +
		"environment variable, conforming exactly to the JSON Schema at the path given " +
		"by the HERD_PLAN_SCHEMA environment variable, then exit."

	args := []string{}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, seed)

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Env = append(childEnv(),
		"HERD_PLAN_OUT="+outFile,
		"HERD_PLAN_SCHEMA="+schemaFile,
	)
	// Interactive mode: wire stdio through to the user's TTY.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("codex exited with error: %w", err)
	}

	plan, err := prompt.ReadPlanFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("interactive plan did not produce a structured plan at %s — "+
			"did you ask codex to finalize before exiting? underlying: %w", outFile, err)
	}
	return plan, nil
}

// allocatePlanArtifacts materializes the JSON Schema file and resolves the plan
// output path shared by both plan modes. The output path is opts.OutputPath when
// set, otherwise a freshly created temp file. The returned cleanup removes the
// schema file and, when a temp output file was allocated, that file too; it is
// always safe to defer.
func (c *CodexAgent) allocatePlanArtifacts(opts agent.PlanOptions) (schemaFile, outFile string, cleanup func(), err error) {
	schemaFile, err = writeSchemaFile("plan.json")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("plan: writing schema file: %w", err)
	}
	cleanup = func() { _ = os.Remove(schemaFile) }

	outFile = opts.OutputPath
	if outFile == "" {
		f, ferr := os.CreateTemp("", "codex-plan-*.json")
		if ferr != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("plan: creating output temp file: %w", ferr)
		}
		outFile = f.Name()
		_ = f.Close()
		schemaCleanup := cleanup
		cleanup = func() {
			schemaCleanup()
			_ = os.Remove(outFile)
		}
	}

	return schemaFile, outFile, cleanup, nil
}
