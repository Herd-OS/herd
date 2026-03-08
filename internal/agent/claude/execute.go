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
	args := []string{"-p", task.Body}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

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
