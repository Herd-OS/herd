package claude

import (
	"fmt"
	"os"

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

// writeSystemPromptFile writes prompt to a temp file and returns its path.
// The caller is responsible for removing the file (use defer os.Remove).
func writeSystemPromptFile(prompt string) (string, error) {
	f, err := os.CreateTemp("", "herd-system-prompt-*.txt")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return f.Name(), nil
}

// Execute and Review are implemented in execute.go and review.go respectively.
