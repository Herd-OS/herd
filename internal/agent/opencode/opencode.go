// Package opencode implements the agent.Agent interface by shelling out to
// the OpenCode CLI. It reuses the shared prompt templates and helpers from
// internal/agent/prompt so its planning and review prompts are identical to
// the claude provider.
//
// Key differences from the claude provider:
//   - OpenCode's `run` subcommand takes the prompt as a positional argument
//     (not stdin) and has no --system-prompt flag, so the system prompt is
//     folded into the message.
//   - The model flag is --model with provider/model form
//     (e.g. anthropic/claude-sonnet-4).
//   - Permissions are auto-approved with --dangerously-skip-permissions.
package opencode

import "github.com/herd-os/herd/internal/agent"

// OpenCodeAgent implements agent.Agent using the OpenCode CLI.
type OpenCodeAgent struct {
	BinaryPath string // Path to `opencode` CLI (default: "opencode")
	Model      string // Model override in provider/model form (optional)
}

// Compile-time check that OpenCodeAgent implements agent.Agent.
var _ agent.Agent = (*OpenCodeAgent)(nil)

// New creates a new OpenCodeAgent with the given binary path and model.
// If binaryPath is empty, defaults to "opencode".
func New(binaryPath, model string) *OpenCodeAgent {
	if binaryPath == "" {
		binaryPath = "opencode"
	}
	return &OpenCodeAgent{
		BinaryPath: binaryPath,
		Model:      model,
	}
}

// buildRunArgs constructs the argv for a headless `opencode run` invocation.
// The message is appended as the final positional argument because OpenCode's
// run subcommand accepts the prompt only as a positional, not via a flag.
func buildRunArgs(model, message string) []string {
	args := []string{"run", "--dangerously-skip-permissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, message)
	return args
}

// buildInteractiveArgs constructs the argv for an interactive `opencode` TUI
// invocation. There is no `run` subcommand; the combined system+initial
// prompt is passed via --prompt.
func buildInteractiveArgs(model, combinedPrompt string) []string {
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--prompt", combinedPrompt)
	return args
}
