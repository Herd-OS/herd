package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, cmdNames["codex"], "codex command should be registered")
}

func TestRootCmd_InternalCommandsHidden(t *testing.T) {
	root := NewRootCmd()

	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "worker", "integrator", "monitor":
			assert.True(t, cmd.Hidden, "%s should be hidden", cmd.Name())
		case "codex":
			assert.False(t, cmd.Hidden, "%s should be visible", cmd.Name())
		}
	}
}

func TestRootCmd_CodexHelpShowsDoctorOnly(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"codex", "--help"})

	err := root.Execute()

	require.NoError(t, err)
	assert.Contains(t, out.String(), "doctor")
	assert.NotContains(t, out.String(), "keepalive-loop")
}
