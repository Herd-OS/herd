package integrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCheckService implements platform.CheckService for testing.
type mockCheckService struct {
	status      string
	rerunErr    error
	rerunCalled bool
}

func (m *mockCheckService) GetCombinedStatus(_ context.Context, _ string) (string, error) {
	return m.status, nil
}

func (m *mockCheckService) RerunFailedChecks(_ context.Context, _ string) error {
	m.rerunCalled = true
	return m.rerunErr
}

// mockPlatformWithChecks embeds mockPlatform and adds CheckService.
type mockPlatformWithChecks struct {
	*mockPlatform
	checks *mockCheckService
}

func (m *mockPlatformWithChecks) Checks() platform.CheckService { return m.checks }

func baseCIMocks() (*mockIssueService, *mockWorkflowService, *mockPRService) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "10"}},
		},
	}

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch"},
		},
	}

	return issueSvc, wf, prSvc
}

func TestCheckCI(t *testing.T) {
	tests := []struct {
		name           string
		ciStatus       string
		ciMaxCycles    int
		existingCycles int
		requireCI      bool
		force          bool
		expectStatus   string
		expectSkipped  bool
		expectMaxHit   bool
		expectFixCount int
	}{
		{
			name:         "success — CI passes",
			ciStatus:     "success",
			requireCI:    true,
			ciMaxCycles:  2,
			expectStatus: "success",
		},
		{
			name:         "pending — CI still running",
			ciStatus:     "pending",
			requireCI:    true,
			ciMaxCycles:  2,
			expectStatus: "pending",
		},
		{
			name:           "pending with force — treats as failure",
			ciStatus:       "pending",
			requireCI:      true,
			ciMaxCycles:    2,
			force:          true,
			expectStatus:   "failure",
			expectFixCount: 1,
		},
		{
			name:          "skipped — require_ci false",
			ciStatus:      "failure",
			requireCI:     false,
			expectSkipped: true,
		},
		{
			name:           "failure — creates fix issue on first call",
			ciStatus:       "failure",
			requireCI:      true,
			ciMaxCycles:    2,
			expectStatus:   "failure",
			expectFixCount: 1,
		},
		{
			name:           "failure — dispatches fix worker",
			ciStatus:       "failure",
			requireCI:      true,
			ciMaxCycles:    2,
			expectStatus:   "failure",
			expectFixCount: 1,
		},
		{
			name:           "failure — max cycles reached",
			ciStatus:       "failure",
			requireCI:      true,
			ciMaxCycles:    1,
			existingCycles: 1,
			expectStatus:   "failure",
			expectMaxHit:   true,
		},
		{
			name:           "failure — zero cycles (unlimited)",
			ciStatus:       "failure",
			requireCI:      true,
			ciMaxCycles:    0,
			expectStatus:   "failure",
			expectFixCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc, wf, prSvc := baseCIMocks()

			// Add existing CI fix cycle issues if needed
			if tt.existingCycles > 0 {
				for i := 1; i <= tt.existingCycles; i++ {
					issueSvc.listResult = append(issueSvc.listResult, &platform.Issue{
						Number: 80 + i,
						Body:   fmt.Sprintf("---\nherd:\n  version: 1\n  ci_fix_cycle: %d\n---\n\n## Task\nFix CI\n", i),
					})
				}
			}

			createdIssues := []*platform.Issue{}
			mockCreate := &mockIssueServiceWithCreate{
				mockIssueService: issueSvc,
				onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
					iss := &platform.Issue{Number: 99, Title: title}
					createdIssues = append(createdIssues, iss)
					return iss, nil
				},
			}

			checkSvc := &mockCheckService{status: tt.ciStatus}

			mock := &mockPlatformWithChecks{
				mockPlatform: &mockPlatform{
					issues:     mockCreate,
					prs:        prSvc,
					workflows:  wf,
					repo:       &mockRepoService{defaultBranch: "main"},
					milestones: &mockMilestoneService{},
				},
				checks: checkSvc,
			}

			cfg := &config.Config{
				Integrator: config.Integrator{
					RequireCI:      tt.requireCI,
					CIMaxFixCycles: tt.ciMaxCycles,
				},
				Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
			}

			result, err := CheckCI(context.Background(), mock, cfg, CheckCIParams{RunID: 100, Force: tt.force})
			require.NoError(t, err)

			if tt.expectSkipped {
				assert.True(t, result.Skipped)
				return
			}

			assert.Equal(t, tt.expectStatus, result.Status)
			assert.Equal(t, tt.expectMaxHit, result.MaxCyclesHit)
			assert.Len(t, createdIssues, tt.expectFixCount)

			if tt.expectFixCount > 0 {
				assert.Len(t, result.FixIssues, tt.expectFixCount)
				assert.Len(t, wf.dispatched, tt.expectFixCount)
			}
		})
	}
}

func TestCheckCI_BeforeDispatch(t *testing.T) {
	issueSvc, wf, prSvc := baseCIMocks()
	issueSvc.listResult = []*platform.Issue{}

	created := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			created = append(created, iss)
			return iss, nil
		},
	}

	checkSvc := &mockCheckService{status: "failure"}

	var callOrder []string
	mock := &mockPlatformWithChecks{
		mockPlatform: &mockPlatform{
			issues:     mockCreate,
			prs:        prSvc,
			workflows:  wf,
			repo:       &mockRepoService{defaultBranch: "main"},
			milestones: &mockMilestoneService{},
		},
		checks: checkSvc,
	}
	// Wrap workflows to record dispatch order
	wf.onDispatch = func() { callOrder = append(callOrder, "dispatch") }

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true, CIMaxFixCycles: 2},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := CheckCI(context.Background(), mock, cfg, CheckCIParams{
		RunID: 100,
		BeforeDispatch: func() {
			callOrder = append(callOrder, "before-dispatch")
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "failure", result.Status)
	assert.Len(t, created, 1)
	require.Len(t, callOrder, 2)
	assert.Equal(t, "before-dispatch", callOrder[0], "BeforeDispatch must be called before dispatch")
	assert.Equal(t, "dispatch", callOrder[1])
}

func TestCheckCI_BatchLookup(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch"},
		},
	}

	checkSvc := &mockCheckService{status: "success"}

	mock := &mockPlatformWithChecks{
		mockPlatform: &mockPlatform{
			issues:    issueSvc,
			prs:       prSvc,
			workflows: &mockWorkflowService{},
			repo:      &mockRepoService{defaultBranch: "main"},
			milestones: &mockMilestoneService{
				getResult: map[int]*platform.Milestone{
					1: {Number: 1, Title: "Batch"},
				},
			},
		},
		checks: checkSvc,
	}

	result, err := CheckCI(context.Background(), mock, &config.Config{
		Integrator: config.Integrator{RequireCI: true, CIMaxFixCycles: 2},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}, CheckCIParams{BatchNumber: 1})

	require.NoError(t, err)
	assert.Equal(t, "success", result.Status)
}

func TestCheckCI_BatchLookup_Failure(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{}

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch"},
		},
	}

	wf := &mockWorkflowService{}
	checkSvc := &mockCheckService{status: "failure"}

	mock := &mockPlatformWithChecks{
		mockPlatform: &mockPlatform{
			issues:    mockCreate,
			prs:       prSvc,
			workflows: wf,
			repo:      &mockRepoService{defaultBranch: "main"},
			milestones: &mockMilestoneService{
				getResult: map[int]*platform.Milestone{
					1: {Number: 1, Title: "Batch"},
				},
			},
		},
		checks: checkSvc,
	}

	result, err := CheckCI(context.Background(), mock, &config.Config{
		Integrator: config.Integrator{RequireCI: true, CIMaxFixCycles: 2},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}, CheckCIParams{BatchNumber: 1})

	require.NoError(t, err)
	assert.Equal(t, "failure", result.Status)
	assert.Len(t, createdIssues, 1)
	assert.Len(t, result.FixIssues, 1)
}

func TestCheckCI_FixIssueOnFirstFailure(t *testing.T) {
	issueSvc, wf, prSvc := baseCIMocks()
	issueSvc.listResult = []*platform.Issue{}

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	checkSvc := &mockCheckService{status: "failure"}

	mock := &mockPlatformWithChecks{
		mockPlatform: &mockPlatform{
			issues:     mockCreate,
			prs:        prSvc,
			workflows:  wf,
			repo:       &mockRepoService{defaultBranch: "main"},
			milestones: &mockMilestoneService{},
		},
		checks: checkSvc,
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true, CIMaxFixCycles: 2},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := CheckCI(context.Background(), mock, cfg, CheckCIParams{RunID: 100})
	require.NoError(t, err)

	assert.Equal(t, "failure", result.Status)
	assert.Len(t, createdIssues, 1, "fix issue must be created on first call")
	assert.Len(t, result.FixIssues, 1)
	assert.Equal(t, 1, result.FixCycle)
	assert.False(t, checkSvc.rerunCalled, "RerunFailedChecks must not be called")
}

func TestCheckCI_SkipsWhenCIFixInProgress(t *testing.T) {
	issueSvc, wf, prSvc := baseCIMocks()
	// Existing CI fix issue that's still in progress
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 80,
			Labels: []string{issues.StatusInProgress},
			Body:   "---\nherd:\n  version: 1\n  ci_fix_cycle: 1\n---\n\n## Task\nFix CI\n",
		},
	}

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	checkSvc := &mockCheckService{status: "failure"}
	mock := &mockPlatformWithChecks{
		mockPlatform: &mockPlatform{
			issues:     mockCreate,
			prs:        prSvc,
			workflows:  wf,
			repo:       &mockRepoService{defaultBranch: "main"},
			milestones: &mockMilestoneService{},
		},
		checks: checkSvc,
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true, CIMaxFixCycles: 0},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := CheckCI(context.Background(), mock, cfg, CheckCIParams{RunID: 100})
	require.NoError(t, err)
	assert.Equal(t, "failure", result.Status)
	assert.Empty(t, createdIssues, "should not create fix issue when one is already in progress")
	assert.Empty(t, wf.dispatched, "should not dispatch when fix is in progress")
}
