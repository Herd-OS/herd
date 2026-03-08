package claude

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	a := New("", "")
	assert.Equal(t, "claude", a.BinaryPath)
	assert.Equal(t, "", a.Model)

	a = New("/usr/local/bin/claude", "opus")
	assert.Equal(t, "/usr/local/bin/claude", a.BinaryPath)
	assert.Equal(t, "opus", a.Model)
}

func TestExecuteNotImplemented(t *testing.T) {
	a := New("", "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{})
	assert.ErrorIs(t, err, errNotImpl)
}

func TestReviewNotImplemented(t *testing.T) {
	a := New("", "")
	_, err := a.Review(context.Background(), "", agent.ReviewOptions{})
	assert.ErrorIs(t, err, errNotImpl)
}
