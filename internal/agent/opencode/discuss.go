package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/herd-os/herd/internal/agent"
)

// Discuss launches OpenCode in interactive TUI mode with a caller-supplied
// system prompt. Because OpenCode has no --system-prompt flag, the system
// prompt and the optional initial prompt are folded together and passed via
// --prompt. Returns an error only if the agent process fails to start or
// exits non-zero.
//
// OpenCode's TUI cannot accept a piped stdin (its stdin is reserved for
// interactive input), so the combined prompt is passed in argv. To prevent
// an opaque "argument list too long" exec failure, Discuss rejects combined
// prompts larger than maxArgvPromptBytes with a clear error.
func (o *OpenCodeAgent) Discuss(ctx context.Context, opts agent.DiscussOptions) error {
	if opts.SystemPrompt == "" {
		return fmt.Errorf("discuss: system prompt is required")
	}

	combined := opts.SystemPrompt
	if opts.InitialPrompt != "" {
		combined += "\n\n" + opts.InitialPrompt
	}

	if len(combined) > maxArgvPromptBytes {
		return fmt.Errorf("discuss: combined prompt is %d bytes which exceeds the safe argv limit of %d bytes; opencode's TUI cannot accept a piped prompt, so reduce the system or initial prompt",
			len(combined), maxArgvPromptBytes)
	}

	args := buildInteractiveArgs(o.Model, combined)

	cmd := exec.CommandContext(ctx, o.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("opencode exited with error: %w", err)
	}
	return nil
}
