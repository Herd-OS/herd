package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardCmd_RefreshFlagDefault(t *testing.T) {
	cmd := newDashboardCmd()
	require.NotNil(t, cmd)
	assert.Equal(t, "dashboard", cmd.Use)

	flag := cmd.Flag("refresh-seconds")
	require.NotNil(t, flag, "refresh-seconds flag must be registered")
	assert.Equal(t, "15", flag.DefValue)
}

func TestDashboardCmd_HasShortDescription(t *testing.T) {
	cmd := newDashboardCmd()
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
}

func TestDashboardCmd_RegisteredOnRoot(t *testing.T) {
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "dashboard" {
			return
		}
	}
	t.Fatalf("dashboard command not registered on root")
}
