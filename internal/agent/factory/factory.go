// Package factory constructs agent.Agent implementations from a provider
// name. It lives in its own package because internal/agent cannot import the
// provider packages (claude, opencode) without an import cycle.
package factory

import (
	"fmt"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/agent/codex"
	"github.com/herd-os/herd/internal/agent/opencode"
)

// New constructs an agent.Agent for the given provider. binary may be empty
// (each provider falls back to its default binary name); model may be empty.
// codexReasoningEffort and codexSandbox are passed through to the codex
// provider only — codexReasoningEffort applies a "medium" default when empty;
// codexSandbox empty preserves the Codex default sandbox (workspace-write).
// An empty provider maps to claude to preserve current default behavior.
func New(provider, binary, model, codexReasoningEffort, codexSandbox string) (agent.Agent, error) {
	switch provider {
	case "claude", "":
		return claude.New(binary, model), nil
	case "opencode":
		return opencode.New(binary, model), nil
	case "codex":
		return codex.NewAgent(binary, model, codexReasoningEffort, codexSandbox), nil
	default:
		return nil, fmt.Errorf("unknown agent provider %q (supported: claude, opencode, codex)", provider)
	}
}
