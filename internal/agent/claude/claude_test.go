package claude

import (
	"testing"

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
