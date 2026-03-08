package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkerCmd_RequiresHerdRunner(t *testing.T) {
	t.Setenv("HERD_RUNNER", "")
	root := NewRootCmd()
	root.SetArgs([]string{"worker", "exec", "42"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HERD_RUNNER")
}

func TestWorkerCmd_SubcommandStructure(t *testing.T) {
	cmd := newWorkerCmd()
	assert.True(t, cmd.Hidden)
	assert.Equal(t, "worker", cmd.Name())

	names := make([]string, 0)
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	assert.Contains(t, names, "exec")
}
