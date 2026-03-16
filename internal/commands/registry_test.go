package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_RegisterAndHandle(t *testing.T) {
	reg := NewRegistry()
	called := false
	reg.Register("test", func(ctx *HandlerContext, cmd Command) Result {
		called = true
		return Result{Message: "ok"}
	})

	result := reg.Handle(&HandlerContext{}, Command{Name: "test"})
	assert.True(t, called)
	assert.Equal(t, "ok", result.Message)
	assert.NoError(t, result.Error)
}

func TestRegistry_UnknownCommand(t *testing.T) {
	reg := NewRegistry()
	result := reg.Handle(&HandlerContext{}, Command{Name: "nope"})
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "unknown command")
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	reg := NewRegistry()
	reg.Register("dup", func(ctx *HandlerContext, cmd Command) Result { return Result{} })
	assert.Panics(t, func() {
		reg.Register("dup", func(ctx *HandlerContext, cmd Command) Result { return Result{} })
	})
}

func TestRegistry_Has(t *testing.T) {
	reg := NewRegistry()
	assert.False(t, reg.Has("foo"))
	reg.Register("foo", func(ctx *HandlerContext, cmd Command) Result { return Result{} })
	assert.True(t, reg.Has("foo"))
}
