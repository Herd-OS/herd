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
		rerunErr       error
		ciMaxCycles    int
		existingCycles int
		requireCI      bool
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
			name:          "skipped — require_ci false",
			ciStatus:      "failure",
			requireCI:     false,
			expectSkipped: true,
		},
		{
			name:         "failure — re-run succeeds, returns pending",
			ciStatus:     "failure",
			requireCI:    true,
			ciMaxCycles:  2,
			expectStatus: "pending",
		},
		{
			name:           "failure — re-run fails, dispatches fix worker",
			ciStatus:       "failure",
			rerunErr:       fmt.Errorf("re-run failed"),
			requireCI:      true,
			ciMaxCycles:    2,
			expectStatus:   "failure",
			expectFixCount: 1,
		},
		{
			name:         "failure — re-run fails, max cycles reached",
			ciStatus:     "failure",
			rerunErr:     fmt.Errorf("re-run failed"),
			requireCI:    true,
			ciMaxCycles:  1,
			existingCycles: 1,
			expectStatus: "failure",
			expectMaxHit: true,
		},
		{
			name:         "failure — re-run fails, zero cycles (notify only)",
			ciStatus:     "failure",
			rerunErr:     fmt.Errorf("re-run failed"),
			requireCI:    true,
			ciMaxCycles:  0,
			expectStatus: "failure",
			expectMaxHit: true,
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

			checkSvc := &mockCheckService{status: tt.ciStatus, rerunErr: tt.rerunErr}

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

			result, err := CheckCI(context.Background(), mock, cfg, CheckCIParams{RunID: 100})
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
	checkSvc := &mockCheckService{status: "failure", rerunErr: fmt.Errorf("re-run failed")}

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
