package claude

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/agent"
)

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

var errNotImpl = fmt.Errorf("not implemented")

func (c *ClaudeAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, errNotImpl
}

func (c *ClaudeAgent) Execute(_ context.Context, _ agent.TaskSpec) (*agent.ExecResult, error) {
	return nil, errNotImpl
}

func (c *ClaudeAgent) Review(_ context.Context, _ string, _ agent.ReviewOptions) (*agent.ReviewResult, error) {
	return nil, errNotImpl
}
