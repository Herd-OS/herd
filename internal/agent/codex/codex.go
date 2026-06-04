// Package codex implements the agent.Agent interface by shelling out to
// OpenAI's Codex CLI (`codex` / `@openai/codex`). It reuses the shared prompt
// templates and helpers from internal/agent/prompt so its planning and review
// prompts are identical to the claude and opencode providers.
//
// Key differences from the other providers:
//   - The headless mode is the `codex exec` subcommand (the interactive TUI
//     used by Discuss is `codex` with no subcommand).
//   - Structured output for Plan and Review is native: an embedded JSON Schema
//     is materialized to a temp file and passed via --output-schema, and the
//     final agent message is written to a file via --output-last-message.
//   - Auth: Codex reads CODEX_API_KEY (highest precedence). OPENAI_API_KEY is
//     NOT in Codex's main auth path, so childEnv() maps it into CODEX_API_KEY
//     for the child process when CODEX_API_KEY is unset.
package codex

import (
	"os"

	"github.com/herd-os/herd/internal/agent"
)

// CodexAgent implements agent.Agent using the Codex CLI.
type CodexAgent struct {
	BinaryPath      string // path to `codex` CLI (default: "codex")
	Model           string // bare model ID, e.g. "gpt-5-codex" (optional)
	ReasoningEffort string // "minimal"|"low"|"medium"|"high" (default: "medium")
}

// Compile-time check that CodexAgent implements agent.Agent.
var _ agent.Agent = (*CodexAgent)(nil)

// NewAgent creates a new CodexAgent with the given binary path, model, and
// reasoning effort. If binaryPath is empty, defaults to "codex". If
// reasoningEffort is empty, defaults to "medium".
func NewAgent(binaryPath, model, reasoningEffort string) *CodexAgent {
	if binaryPath == "" {
		binaryPath = "codex"
	}
	if reasoningEffort == "" {
		reasoningEffort = "medium"
	}
	return &CodexAgent{
		BinaryPath:      binaryPath,
		Model:           model,
		ReasoningEffort: reasoningEffort,
	}
}

// childEnv returns the parent environment with CODEX_API_KEY populated from
// OPENAI_API_KEY when CODEX_API_KEY is empty and OPENAI_API_KEY is set. An
// explicit CODEX_API_KEY is always preserved (user-explicit wins).
func childEnv() []string {
	env := os.Environ()
	if os.Getenv("CODEX_API_KEY") == "" {
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			env = append(env, "CODEX_API_KEY="+v)
		}
	}
	return env
}

// buildExecBaseArgs returns the common headless flags shared by
// Execute/Plan/Review. The interactive Discuss path does NOT use these.
//
// --full-auto is intentionally avoided (deprecated trap per the Codex source);
// --sandbox workspace-write is the supported way to allow edits in cwd.
func (c *CodexAgent) buildExecBaseArgs() []string {
	args := []string{"exec",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-user-config",
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "-c", "model_reasoning_effort="+c.ReasoningEffort)
	return args
}
