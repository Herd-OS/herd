// Package opencode implements the agent.Agent interface by shelling out to
// the OpenCode CLI. It reuses the shared prompt templates and helpers from
// internal/agent/prompt so its planning and review prompts are identical to
// the claude provider.
//
// Key differences from the claude provider:
//   - OpenCode's `run` subcommand reads the prompt from stdin when stdin is
//     not a TTY (and concatenates a positional message + stdin when both are
//     present). Execute and Review pipe the prompt via stdin to avoid the
//     OS ARG_MAX limit for large prompts (e.g. PR diffs in Review).
//   - There is no headless --system-prompt flag, so the review system prompt
//     is prepended to the user message before being piped to stdin.
//   - The TUI (`opencode --prompt`) used by Plan and Discuss cannot accept
//     a piped prompt (its stdin is reserved for interactive input), so the
//     prompt is passed via the --prompt flag. To prevent an opaque
//     "argument list too long" failure, both Plan and Discuss guard against
//     prompts larger than maxArgvPromptBytes with a clear error.
//   - The model flag is --model with provider/model form
//     (e.g. anthropic/claude-sonnet-4).
//   - Permissions are auto-approved with --dangerously-skip-permissions.
package opencode

import "github.com/herd-os/herd/internal/agent"

// maxArgvPromptBytes is the safe upper bound for prompts passed via argv to
// the OpenCode TUI (`opencode --prompt`). Linux ARG_MAX is at least 128 KiB
// on most systems and macOS sets it to 256 KiB; 100 KiB leaves generous
// headroom for the rest of the argv (env, flags) before the kernel rejects
// the exec. This guard only applies to Plan and Discuss; Execute and Review
// pipe their prompts via stdin and have no such limit.
const maxArgvPromptBytes = 100 * 1024

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
// The prompt is NOT included in argv — callers must pipe it via the child
// process's stdin to avoid the OS ARG_MAX limit on large prompts.
func buildRunArgs(model string) []string {
	args := []string{"run", "--dangerously-skip-permissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// buildInteractiveArgs constructs the argv for an interactive `opencode` TUI
// invocation. There is no `run` subcommand; the combined system+initial
// prompt is passed via --prompt. Callers should guard combinedPrompt against
// maxArgvPromptBytes before calling this helper.
func buildInteractiveArgs(model, combinedPrompt string) []string {
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--prompt", combinedPrompt)
	return args
}
