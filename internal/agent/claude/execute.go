package claude

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
)

// Execute runs the configured agent in headless mode to complete a task.
// The task body is passed as the prompt (-p), and the system prompt provides
// worker instructions. The agent commits as it works in the repo.
func (c *ClaudeAgent) Execute(ctx context.Context, task agent.TaskSpec, opts agent.ExecOptions) (*agent.ExecResult, error) {
	// Build the prompt: system prompt wraps the task body, so we pass
	// the full rendered prompt via -p. The task body may start with ---
	// (YAML front matter) which some CLI parsers misinterpret as a flag.
	prompt := task.Body
	if opts.SystemPrompt != "" {
		prompt = opts.SystemPrompt
	}

	args := []string{"--dangerously-skip-permissions"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	// Place -p last so the prompt value (which may start with ---) is not
	// misinterpreted as a flag by the argument parser.
	args = append(args, "-p", prompt)

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agent exited with error: %w\n%s", err, stderr.String())
	}

	return &agent.ExecResult{
		Summary: stdout.String(),
	}, nil
}
