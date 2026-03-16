package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
)

// ErrUnknownCommand is returned by Handle when the command name is not in the registry.
var ErrUnknownCommand = errors.New("unknown command")

// HandlerContext provides dependencies to command handlers.
type HandlerContext struct {
	Platform    platform.Platform
	Config      *config.Config
	RepoRoot    string
	PRNumber    int    // 0 if triggered from an issue, not a PR
	IssueNumber int    // the issue/PR number where the comment was posted
	CommentID   int64  // for adding reactions
	IssueBody   string // full issue/PR body (needed by future multi-turn commands like plan)
	AuthorLogin string // comment author login (used to detect bot users, e.g. "herd-os[bot]")
}

// HandlerFunc is the signature for command handlers.
type HandlerFunc func(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error)

// Registry maps command names to handlers.
var Registry = map[string]HandlerFunc{}

// Register adds a command handler.
func Register(name string, fn HandlerFunc) {
	Registry[name] = fn
}

// allowedAssociations are the GitHub author associations permitted to run commands.
var allowedAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// isBotUser returns true if the login is a GitHub App bot (ends with "[bot]").
func isBotUser(login string) bool {
	return strings.HasSuffix(login, "[bot]")
}

// Handle parses a comment, validates permissions, and dispatches to the handler.
// Returns the response message to post as a reply comment.
// Returns empty string if the comment doesn't contain a command.
func Handle(ctx context.Context, hctx *HandlerContext, commentBody, authorAssociation string) (string, error) {
	cmd := Parse(commentBody)
	if cmd == nil {
		return "", nil
	}

	// Permission check: must be owner/member/collaborator or a bot user.
	if !allowedAssociations[authorAssociation] && !isBotUser(hctx.AuthorLogin) {
		return "", fmt.Errorf("permission denied: %q is not authorized to run /herd commands", authorAssociation)
	}

	// Add 👀 reaction to signal we saw the command.
	if hctx.Platform != nil && hctx.CommentID != 0 {
		_ = hctx.Platform.Issues().CreateReaction(ctx, hctx.CommentID, "eyes")
	}

	// Look up the command in the registry.
	fn, ok := Registry[cmd.Name]
	if !ok {
		msg := fmt.Sprintf("Unknown command: `%s`. No handler registered for this command.", cmd.Name)
		return msg, fmt.Errorf("%w: %s", ErrUnknownCommand, cmd.Name)
	}

	// Dispatch to the handler.
	response, err := fn(ctx, hctx, cmd)
	if err != nil {
		return fmt.Sprintf("Command `%s` failed: %s", cmd.Name, err.Error()), err
	}
	return response, nil
}
