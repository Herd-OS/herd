package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIntegratorCmd_RequiresHerdRunner(t *testing.T) {
	t.Setenv("HERD_RUNNER", "")

	tests := []struct {
		name string
		args []string
	}{
		{"consolidate", []string{"integrator", "consolidate", "--run-id", "123"}},
		{"advance", []string{"integrator", "advance", "--run-id", "123"}},
		{"review", []string{"integrator", "review", "--run-id", "123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRootCmd()
			root.SetArgs(tt.args)
			err := root.Execute()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "HERD_RUNNER")
		})
	}
}

func TestIntegratorReviewCmd_RequiresRunIDOrPR(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "review"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "either --run-id or --pr is required")
}

func TestIntegratorReviewCmd_MutuallyExclusive(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "review", "--run-id", "100", "--pr", "50"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestIntegratorCmd_SubcommandStructure(t *testing.T) {
	cmd := newIntegratorCmd()
	assert.True(t, cmd.Hidden)
	assert.Equal(t, "integrator", cmd.Name())

	names := make([]string, 0)
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	assert.Contains(t, names, "consolidate")
	assert.Contains(t, names, "advance")
	assert.Contains(t, names, "review")
}
