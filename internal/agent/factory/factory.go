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
	"github.com/herd-os/herd/internal/config"
)

// New constructs an agent.Agent for the given resolved role. Binary may be
// empty (each provider falls back to its default binary name); model may be
// empty. CodexReasoningEffort and CodexSandbox are passed through to the codex
// provider only: CodexReasoningEffort applies a "medium" default when empty;
// CodexSandbox empty preserves the Codex default sandbox (workspace-write).
// An empty provider maps to claude to preserve current default behavior.
func New(role config.AgentRole) (agent.Agent, error) {
	switch role.Provider {
	case "claude", "":
		return claude.New(role.Binary, role.Model), nil
	case "opencode":
		return opencode.New(role.Binary, role.Model), nil
	case "codex":
		return codex.NewAgent(role.Binary, role.Model, role.CodexReasoningEffort, role.CodexSandbox), nil
	default:
		return nil, fmt.Errorf("unknown agent provider %q (supported: claude, opencode, codex)", role.Provider)
	}
}
