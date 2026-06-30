package opencode

import (
	"context"
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/process"
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

	// Interactive TUIs must stay in Herd's foreground terminal process group.
	// Do not set ProcessGroup here; headless execute/review paths opt in.
	if err := process.Run(ctx, process.Command{
		Path:         o.BinaryPath,
		Args:         args,
		Dir:          opts.RepoRoot,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		ProcessGroup: false,
	}); err != nil {
		return fmt.Errorf("opencode exited with error: %w", err)
	}
	return nil
}
