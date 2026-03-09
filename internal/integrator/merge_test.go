package integrator

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeApproved(t *testing.T) {
	tests := []struct {
		name           string
		pr             *platform.PullRequest
		expectMerged   bool
		expectSkipped  bool
		expectReason   string
		expectCleanup  bool
	}{
		{
			name: "success — merges and cleans up",
			pr: &platform.PullRequest{
				Number: 100, Title: "[herd] Add auth (3 tasks)",
				State: "open", Head: "herd/batch/5-add-auth",
			},
			expectMerged:  true,
			expectCleanup: true,
		},
		{
			name: "skip — non-herd PR",
			pr: &platform.PullRequest{
				Number: 100, Title: "Fix typo in README",
				State: "open", Head: "fix-typo",
			},
			expectSkipped: true,
			expectReason:  "not a herd batch PR",
		},
		{
			name: "skip — already closed",
			pr: &platform.PullRequest{
				Number: 100, Title: "[herd] Add auth (3 tasks)",
				State: "closed", Head: "herd/batch/5-add-auth",
			},
			expectSkipped: true,
			expectReason:  "PR is closed",
		},
		{
			name: "skip — already merged",
			pr: &platform.PullRequest{
				Number: 100, Title: "[herd] Add auth (3 tasks)",
				State: "merged", Head: "herd/batch/5-add-auth",
			},
			expectSkipped: true,
			expectReason:  "PR is merged",
		},
		{
			name: "success — unparseable batch branch still merges",
			pr: &platform.PullRequest{
				Number: 100, Title: "[herd] Something",
				State: "open", Head: "some-random-branch",
			},
			expectMerged:  true,
			expectCleanup: false, // can't parse milestone, but merge still succeeds
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prSvc := &mockPRService{
				getResult: map[int]*platform.PullRequest{tt.pr.Number: tt.pr},
			}
			issueSvc := newMockIssueService()
			issueSvc.listResult = []*platform.Issue{
				{Number: 10, Title: "Task", Labels: []string{issues.StatusDone}},
			}
			msSvc := &mockMilestoneService{
				getResult: map[int]*platform.Milestone{5: {Number: 5, Title: "Add auth"}},
			}

			mock := &mockPlatform{
				issues:     issueSvc,
				prs:        prSvc,
				repo:       &mockRepoService{defaultBranch: "main"},
				milestones: msSvc,
			}

			cfg := &config.Config{
				Integrator: config.Integrator{Strategy: "squash"},
			}

			result, err := MergeApproved(context.Background(), mock, cfg, MergeApprovedParams{PRNumber: tt.pr.Number})
			require.NoError(t, err)

			assert.Equal(t, tt.expectMerged, result.Merged)
			assert.Equal(t, tt.expectSkipped, result.Skipped)
			if tt.expectReason != "" {
				assert.Equal(t, tt.expectReason, result.Reason)
			}
			if tt.expectMerged && !tt.expectSkipped {
				assert.True(t, prSvc.merged)
			}
			if tt.expectCleanup {
				// Milestone should have been closed
				assert.Contains(t, msSvc.updatedStates, "closed")
			}
		})
	}
}

func TestMergeApproved_MergeFailure(t *testing.T) {
	prSvc := &mockPRServiceWithMergeErr{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				100: {Number: 100, Title: "[herd] Batch", State: "open", Head: "herd/batch/1-batch"},
			},
		},
		mergeErr: assert.AnError,
	}

	mock := &mockPlatform{
		prs:  prSvc,
		repo: &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Integrator: config.Integrator{Strategy: "squash"}}

	_, err := MergeApproved(context.Background(), mock, cfg, MergeApprovedParams{PRNumber: 100})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "merging batch PR")
}
