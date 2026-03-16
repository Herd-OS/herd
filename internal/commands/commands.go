package commands

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
)

// Command represents a parsed /herd command from an issue comment.
type Command struct {
	Name string
	Args string
}

// HandlerContext provides platform and configuration to command handlers.
type HandlerContext struct {
	Platform platform.Platform
	Cfg      *config.Config
}

// Handler is the function signature for command handlers.
type Handler func(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error)

var registry = map[string]Handler{}

// Register registers a command handler by name.
func Register(name string, h Handler) {
	registry[name] = h
}

// Dispatch looks up and calls the registered handler for the command.
func Dispatch(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error) {
	h, ok := registry[cmd.Name]
	if !ok {
		return "", fmt.Errorf("unknown command: %s", cmd.Name)
	}
	return h(ctx, hctx, cmd)
}
