package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootCmd_InternalCommandsRegistered(t *testing.T) {
	root := NewRootCmd()

	cmdNames := make(map[string]bool)
	for _, cmd := range root.Commands() {
		cmdNames[cmd.Name()] = true
	}

	assert.True(t, cmdNames["worker"], "worker command should be registered")
	assert.True(t, cmdNames["integrator"], "integrator command should be registered")
	assert.True(t, cmdNames["monitor"], "monitor command should be registered")
}

func TestRootCmd_InternalCommandsHidden(t *testing.T) {
	root := NewRootCmd()

	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "worker", "integrator", "monitor":
			assert.True(t, cmd.Hidden, "%s should be hidden", cmd.Name())
		}
	}
}
