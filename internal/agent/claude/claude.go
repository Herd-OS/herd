package claude

import "github.com/herd-os/herd/internal/agent"


// ClaudeAgent implements agent.Agent using the Claude Code CLI.
type ClaudeAgent struct {
	BinaryPath string // Path to `claude` CLI (default: "claude")
	Model      string // Model override (optional)
}

// Compile-time check that ClaudeAgent implements agent.Agent.
var _ agent.Agent = (*ClaudeAgent)(nil)

// New creates a new ClaudeAgent with the given binary path and model.
// If binaryPath is empty, defaults to "claude".
func New(binaryPath, model string) *ClaudeAgent {
	if binaryPath == "" {
		binaryPath = "claude"
	}
	return &ClaudeAgent{
		BinaryPath: binaryPath,
		Model:      model,
	}
}

// Execute and Review are implemented in execute.go and review.go respectively.
