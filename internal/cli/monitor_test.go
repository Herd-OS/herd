package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMonitorCmd_RequiresHerdRunner(t *testing.T) {
	t.Setenv("HERD_RUNNER", "")
	root := NewRootCmd()
	root.SetArgs([]string{"monitor", "patrol"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HERD_RUNNER")
}

func TestMonitorCmd_SubcommandStructure(t *testing.T) {
	cmd := newMonitorCmd()
	assert.True(t, cmd.Hidden)
	assert.Equal(t, "monitor", cmd.Name())

	names := make([]string, 0)
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	assert.Contains(t, names, "patrol")
}
