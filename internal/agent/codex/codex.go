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
//     for the child process when CODEX_API_KEY is unset AND no subscription
//     auth.json is present. When auth.json exists the mapping is skipped so the
//     API key does not clobber the subscription.
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
	// Sandbox is the `--sandbox` policy passed to every `codex exec`
	// invocation. Empty falls back to "workspace-write" (Codex's default,
	// preserved for backward compatibility on hosts where bubblewrap works).
	// Set to "danger-full-access" for workers running inside Docker
	// containers — Codex's bubblewrap-based sandboxes need an
	// unprivileged user namespace which most container hosts (notably
	// TrueNAS SCALE Apps) do not grant. The container is already the
	// security boundary in that case, so disabling Codex's inner sandbox
	// is safe. See internal/config/config.go Agent.CodexSandbox for the
	// user-facing config knob.
	Sandbox string
}

// Compile-time check that CodexAgent implements agent.Agent.
var _ agent.Agent = (*CodexAgent)(nil)

// NewAgent creates a new CodexAgent with the given binary path, model,
// reasoning effort, and sandbox policy. If binaryPath is empty, defaults to
// "codex". If reasoningEffort is empty, defaults to "medium". If sandbox is
// empty, defaults to "workspace-write" (Codex's own default).
func NewAgent(binaryPath, model, reasoningEffort, sandbox string) *CodexAgent {
	if binaryPath == "" {
		binaryPath = "codex"
	}
	if reasoningEffort == "" {
		reasoningEffort = "medium"
	}
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	return &CodexAgent{
		BinaryPath:      binaryPath,
		Model:           model,
		ReasoningEffort: reasoningEffort,
		Sandbox:         sandbox,
	}
}

// childEnv returns the parent environment with CODEX_API_KEY populated from
// OPENAI_API_KEY when CODEX_API_KEY is empty, OPENAI_API_KEY is set, and no
// subscription auth.json is present. An explicit CODEX_API_KEY is always
// preserved (user-explicit wins). When a subscription auth.json exists, the
// OPENAI_API_KEY mapping is skipped so it does not clobber the subscription
// (Codex precedence: CODEX_API_KEY > ephemeral > CODEX_ACCESS_TOKEN >
// auth.json).
func childEnv() []string {
	env := os.Environ()
	if os.Getenv("CODEX_API_KEY") == "" && !AuthJSONPresent() {
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			env = append(env, "CODEX_API_KEY="+v)
		}
	}
	return env
}

// buildExecBaseArgs returns the common headless flags shared by
// Execute/Plan/Review. The interactive Discuss path does NOT use these.
//
// --full-auto is intentionally avoided (deprecated trap per the Codex source).
// --sandbox carries c.Sandbox (defaulted to "workspace-write" in NewAgent for
// backward compatibility on hosts where bubblewrap works). For workers running
// inside a Docker container, set agent.codex_sandbox: danger-full-access in
// .herdos.yml — the container is the security boundary and Codex's
// bubblewrap-based sandboxes fail with "No permissions to create a new
// namespace" on most container hosts.
func (c *CodexAgent) buildExecBaseArgs() []string {
	args := []string{"exec",
		"--sandbox", c.Sandbox,
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
