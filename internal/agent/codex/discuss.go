package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
)

// Discuss launches the Codex interactive TUI (`codex` with no subcommand) with
// stdio wired through to the user. Codex's TUI has no system-prompt or
// initial-prompt flag in scope, so there is no clean way to seed the rendered
// system/initial prompt; this implementation passes stdio through directly and
// does no output parsing. The system prompt is still required at the API level
// for parity with the other providers.
func (c *CodexAgent) Discuss(ctx context.Context, opts agent.DiscussOptions) error {
	if opts.SystemPrompt == "" {
		return fmt.Errorf("discuss: system prompt is required")
	}

	args := []string{}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Env = childEnv()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codex exited with error: %w", err)
	}
	return nil
}
