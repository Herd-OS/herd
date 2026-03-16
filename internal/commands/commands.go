package commands

import (
	"context"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/platform"
)

// Command represents a parsed /herd command.
type Command struct {
	Name   string   // e.g., "fix-ci", "retry", "review", "fix"
	Args   []string // positional arguments (e.g., issue number for retry)
	Prompt string   // quoted prompt string, empty if not provided
}

// HandlerContext provides everything a command handler needs to execute.
// Designed for reuse: Phase 1 (slash commands) and Phase 2 (agent tool calls)
// both construct this struct and call the same handler functions.
type HandlerContext struct {
	Ctx      context.Context
	Platform platform.Platform
	Agent    agent.Agent  // may be nil for commands that don't need an agent
	Git      *git.Git     // may be nil for commands that don't need git
	Config   *config.Config
	RepoRoot string

	// Comment context — set by the CLI from workflow event payload
	IssueNumber int    // the issue/PR number where the comment was posted
	CommentID   int64  // for reactions
	IssueBody   string // full issue/PR body for future multi-turn commands
	AuthorLogin string // who posted the command
}

// Result holds the outcome of a command execution.
type Result struct {
	Message string // human-readable summary to post as a reply comment
	Error   error  // non-nil if the command failed
}

// HandlerFunc is the signature for command handler functions.
type HandlerFunc func(ctx *HandlerContext, cmd Command) Result
