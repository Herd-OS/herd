package commands

import (
	"context"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
)

// Handler is the function signature for a command handler.
type Handler func(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error)

// HandlerContext holds dependencies injected into command handlers.
type HandlerContext struct {
	Platform  platform.Platform
	Config    *config.Config
	PRNumber  int
}

// Command holds the parsed command data from a user comment.
type Command struct {
	Name   string
	Prompt string
}

// Registry maps command names to their handlers.
var Registry = map[string]Handler{}

// Register adds a handler to the global Registry.
func Register(name string, h Handler) {
	Registry[name] = h
}
