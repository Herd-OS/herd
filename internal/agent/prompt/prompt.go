// Package prompt contains the shared prompt templates, rendering logic,
// repository context gathering, output heuristics, and parse/validate
// helpers used by the AI coding agent providers (claude, opencode, …).
//
// Provider packages own subprocess orchestration (argv construction,
// exec.Cmd setup, retry loops). This package owns the inputs and outputs
// of those subprocess calls.
package prompt

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// MinValidOutputLen is the minimum stdout length for agent output to be
	// considered valid. Shorter single-line output is treated as suspicious
	// (e.g., "Execution error" from API failures).
	MinValidOutputLen = 20

	// RetryDelay is the wait time before retrying after suspicious output.
	RetryDelay = 5 * time.Second
)

// IsSuspiciousOutput returns true if the agent's stdout looks like an error
// rather than real work output. This catches cases where the agent's API
// returns exit code 0 with just "Execution error" or similar short error
// messages.
func IsSuspiciousOutput(stdout string) bool {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return true
	}
	if strings.EqualFold(trimmed, "Execution error") {
		return true
	}
	// Short single-line output is suspicious — real agent work produces
	// multi-line summaries or at least a substantive single line.
	if len(trimmed) < MinValidOutputLen && !strings.Contains(trimmed, "\n") {
		return true
	}
	return false
}

// WriteSystemPromptFile writes prompt to a temp file and returns its path.
// The caller is responsible for removing the file (use defer os.Remove).
func WriteSystemPromptFile(prompt string) (string, error) {
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
