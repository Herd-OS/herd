package cli

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Contains(t, err.Error(), "one of --run-id, --pr, or --batch is required")
}

func TestIntegratorReviewCmd_MutuallyExclusive(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "review", "--run-id", "100", "--batch", "1"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestRunWasSuccessful(t *testing.T) {
	tests := []struct {
		name       string
		conclusion string
		want       bool
	}{
		{"success", "success", true},
		{"failure", "failure", false},
		{"cancelled", "cancelled", false},
		{"skipped", "skipped", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockPlatformForDispatch()
			mock.workflows.runs = []*platform.Run{
				{ID: 100, Conclusion: tt.conclusion},
			}
			// Override GetRun to use the runs slice
			mockWf := &mockIntegratorWorkflowService{
				run: &platform.Run{ID: 100, Conclusion: tt.conclusion},
			}
			mock.workflows = &mockDispatchWorkflowService{}
			mockPlatform := &mockIntegratorPlatform{mock, mockWf}

			got, err := runWasSuccessful(context.Background(), mockPlatform, 100)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

type mockIntegratorWorkflowService struct {
	run *platform.Run
}

func (m *mockIntegratorWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockIntegratorWorkflowService) Dispatch(_ context.Context, _, _ string, _ map[string]string) (*platform.Run, error) {
	return nil, nil
}
func (m *mockIntegratorWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return m.run, nil
}
func (m *mockIntegratorWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (m *mockIntegratorWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

type mockIntegratorPlatform struct {
	*mockDispatchPlatform
	wf *mockIntegratorWorkflowService
}

func (m *mockIntegratorPlatform) Workflows() platform.WorkflowService { return m.wf }

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
