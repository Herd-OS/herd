package commands

import "fmt"

// Registry maps command names to handler functions.
type Registry struct {
	handlers map[string]HandlerFunc
}

// NewRegistry creates an empty command registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]HandlerFunc)}
}

// Register adds a command handler. Panics if the name is already registered
// (programming error, caught at startup).
func (r *Registry) Register(name string, handler HandlerFunc) {
	if _, exists := r.handlers[name]; exists {
		panic(fmt.Sprintf("command %q already registered", name))
	}
	r.handlers[name] = handler
}

// Handle dispatches a parsed command to its registered handler.
// Returns an error result if the command is unknown.
func (r *Registry) Handle(ctx *HandlerContext, cmd Command) Result {
	h, ok := r.handlers[cmd.Name]
	if !ok {
		return Result{Error: fmt.Errorf("unknown command: %s", cmd.Name)}
	}
	return h(ctx, cmd)
}

// Has returns true if the named command is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.handlers[name]
	return ok
}
