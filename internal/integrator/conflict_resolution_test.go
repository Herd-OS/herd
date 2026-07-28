package integrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildConflictResolutionIssueBody_FrontMatter(t *testing.T) {
	ms := &platform.Milestone{Number: 7, Title: "Batch 7"}
	tests := []struct {
		name         string
		params       ConflictResolutionIssueParams
		wantBranches []string
		wantBatchPR  int
	}{
		{
			name: "worker merge",
			params: ConflictResolutionIssueParams{
				Kind:              ConflictResolutionKindWorkerMerge,
				Milestone:         ms,
				SourceIssueNumber: 42,
				WorkerBranch:      "herd/worker/42-task",
				BatchBranch:       "herd/batch/7-batch",
			},
			wantBranches: []string{"herd/worker/42-task", "herd/batch/7-batch"},
		},
		{
			name: "batch rebase",
			params: ConflictResolutionIssueParams{
				Kind:        ConflictResolutionKindBatchRebase,
				Milestone:   ms,
				BatchBranch: "herd/batch/7-batch",
				BaseBranch:  "main",
			},
			wantBranches: []string{"herd/batch/7-batch", "main"},
		},
		{
			name: "PR base",
			params: ConflictResolutionIssueParams{
				Kind:         ConflictResolutionKindPRBase,
				Milestone:    ms,
				BatchPR:      123,
				PRHeadBranch: "feature/refactor",
				BaseBranch:   "main",
			},
			wantBranches: []string{"feature/refactor", "main"},
			wantBatchPR:  123,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := BuildConflictResolutionIssueBody(tt.params)
			parsed, err := issues.ParseBody(body)
			require.NoError(t, err)

			assert.Equal(t, 1, parsed.FrontMatter.Version)
			assert.Equal(t, 7, parsed.FrontMatter.Batch)
			assert.Equal(t, "fix", parsed.FrontMatter.Type)
			assert.True(t, parsed.FrontMatter.ConflictResolution)
			assert.Equal(t, tt.wantBranches, parsed.FrontMatter.ConflictingBranches)
			assert.Equal(t, tt.wantBatchPR, parsed.FrontMatter.BatchPR)
			if tt.params.Kind == ConflictResolutionKindPRBase {
				assert.Equal(t, tt.params.PRHeadSHA, parsed.FrontMatter.PRHeadSHA)
				assert.Equal(t, tt.params.BaseSHA, parsed.FrontMatter.PRBaseSHA)
			}
		})
	}
}

func TestDispatchConflictResolutionIssue_BatchRebaseDispatchInputsUnchanged(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 555}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "trunk"},
	}
	cfg := &config.Config{
		Workers: config.Workers{TimeoutMinutes: 45, RunnerLabel: "large-runner"},
	}

	result, err := DispatchConflictResolutionIssue(context.Background(), mock, cfg, ConflictResolutionIssueParams{
		Kind:        ConflictResolutionKindBatchRebase,
		Milestone:   &platform.Milestone{Number: 7, Title: "Batch 7"},
		BatchBranch: "herd/batch/7-batch",
		BaseBranch:  "main",
	}, []string{issues.TypeFix, issues.StatusInProgress}, "main")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 555, result.IssueNumber)
	assert.False(t, result.Duplicated)
	assert.Equal(t, "Resolve rebase conflict: herd/batch/7-batch onto main", issueSvc.createdTitle)
	assert.Equal(t, []string{issues.TypeFix, issues.StatusInProgress}, issueSvc.createdLabels)
	require.NotNil(t, issueSvc.createdMilestone)
	assert.Equal(t, 7, *issueSvc.createdMilestone)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "herd-worker.yml", wf.dispatchedWorkflows[0])
	assert.Equal(t, "trunk", wf.dispatchedRefs[0])
	assert.Equal(t, map[string]string{
		"issue_number":    "555",
		"batch_branch":    "main",
		"timeout_minutes": "45",
		"runner_label":    "large-runner",
	}, wf.dispatched[0])
}

func TestBuildConflictResolutionIssueBody_PRBaseInstructionsAndContext(t *testing.T) {
	body := BuildConflictResolutionIssueBody(ConflictResolutionIssueParams{
		Kind:           ConflictResolutionKindPRBase,
		Milestone:      &platform.Milestone{Number: 7, Title: "Batch 7"},
		BatchPR:        123,
		PRHeadBranch:   "feature/refactor",
		PRHeadSHA:      "abc123",
		BaseBranch:     "main",
		BaseSHA:        "def456",
		TriggerAuthor:  "octocat",
		TriggerComment: "/herd resolve-conflicts",
		UserContext:    "Prefer merge unless rebase is necessary.",
	})
	parsed, err := issues.ParseBody(body)
	require.NoError(t, err)

	assert.Less(t, strings.Index(body, "1. `git fetch origin`"), strings.Index(body, "## Context"))
	assert.Contains(t, parsed.Task, "3. Run either `git merge origin/main` or `git rebase origin/main` on your current worker branch.")
	assert.Contains(t, parsed.Task, "Do not search for conflict markers before attempting the merge or rebase")
	assert.Contains(t, parsed.Task, "Do not review stale historical findings unless the user explicitly requested an additional code fix")
	assert.Contains(t, parsed.Context, "PR #123")
	assert.Contains(t, parsed.Context, "Head branch: `feature/refactor`")
	assert.Contains(t, parsed.Context, "Head SHA: `abc123`")
	assert.Contains(t, parsed.Context, "Base branch: `main`")
	assert.Contains(t, parsed.Context, "Base SHA: `def456`")
	assert.Contains(t, parsed.Context, "Triggering author: `octocat`")
	assert.Contains(t, parsed.Context, "/herd resolve-conflicts")
	assert.Contains(t, parsed.Context, "Prefer merge unless rebase is necessary.")
	assert.NotContains(t, body, "## Conversation History")
	assert.NotContains(t, body, "sample stale review finding text")
}

func TestDispatchConflictResolutionIssue_PostsOverflowComments(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 606}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	_, err := DispatchConflictResolutionIssue(context.Background(), mock, &config.Config{}, ConflictResolutionIssueParams{
		Kind:         ConflictResolutionKindPRBase,
		Milestone:    &platform.Milestone{Number: 7, Title: "Batch 7"},
		BatchPR:      123,
		PRHeadBranch: "feature/refactor",
		BaseBranch:   "main",
		UserContext:  strings.Repeat("overflow context\n", 6000),
	}, []string{issues.TypeFix, issues.StatusInProgress}, "main")
	require.NoError(t, err)

	assert.NotEmpty(t, issueSvc.comments[606])
	assert.Contains(t, issueSvc.createdBody, "Body truncated")
	assert.Contains(t, issueSvc.comments[606][0], "Continued from issue body")
}

func TestDispatchConflictResolutionIssue_PropagatesDispatchFailures(t *testing.T) {
	tests := []struct {
		name       string
		repo       *mockRepoService
		workflows  *mockWorkflowService
		wantErrMsg string
	}{
		{
			name:       "default branch lookup fails",
			repo:       &mockRepoService{defaultBranchErr: errors.New("default branch unavailable")},
			workflows:  &mockWorkflowService{},
			wantErrMsg: "getting default branch for conflict-resolution dispatch",
		},
		{
			name:       "workflow dispatch fails",
			repo:       &mockRepoService{defaultBranch: "main"},
			workflows:  &mockWorkflowService{dispatchErr: errors.New("workflow unavailable")},
			wantErrMsg: "dispatching conflict-resolution worker for issue #707",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.createResult = &platform.Issue{Number: 707}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       &mockPRService{},
				workflows: tt.workflows,
				repo:      tt.repo,
			}

			result, err := DispatchConflictResolutionIssue(context.Background(), mock, &config.Config{}, ConflictResolutionIssueParams{
				Kind:         ConflictResolutionKindPRBase,
				Milestone:    &platform.Milestone{Number: 7, Title: "Batch 7"},
				BatchPR:      123,
				PRHeadBranch: "herd/batch/7-batch",
				PRHeadSHA:    "abc123",
				BaseBranch:   "main",
				BaseSHA:      "def456",
			}, []string{issues.TypeFix, issues.StatusInProgress}, "herd/batch/7-batch")

			require.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.wantErrMsg)
			assert.Equal(t, "Resolve PR conflict: #123 (herd/batch/7-batch onto main)", issueSvc.createdTitle)
			assert.Contains(t, issueSvc.removedLabels[707], issues.StatusInProgress)
			assert.Contains(t, issueSvc.addedLabels[707], issues.StatusFailed)
			require.NotEmpty(t, issueSvc.comments[707])
			assert.Contains(t, issueSvc.comments[707][0], "Failed to dispatch conflict-resolution worker")
			assert.Empty(t, tt.workflows.dispatched)
		})
	}
}

func TestFindActivePRConflictResolutionIssue(t *testing.T) {
	body := BuildConflictResolutionIssueBody(ConflictResolutionIssueParams{
		Kind:         ConflictResolutionKindPRBase,
		Milestone:    &platform.Milestone{Number: 7, Title: "Batch 7"},
		BatchPR:      123,
		PRHeadBranch: "feature/refactor",
		PRHeadSHA:    "abc123",
		BaseBranch:   "main",
		BaseSHA:      "def456",
	})
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 100,
			Labels: []string{issues.StatusInProgress},
			Body:   fmt.Sprintf("---\nherd:\n  version: 1\n  batch: 7\n  type: fix\n  batch_pr: 123\n  conflict_resolution: true\n---\n\n## Context\nHead SHA: `%s`\n\nBase SHA: `stale`\n", "abc123"),
		},
		{Number: 101, Labels: []string{issues.StatusFailed}, Body: body},
		{Number: 102, Labels: []string{issues.StatusReady}, Body: body},
	}
	mock := &mockPlatform{issues: issueSvc}

	found, err := FindActivePRConflictResolutionIssue(context.Background(), mock, 7, 123, "abc123", "def456")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, 102, found.Number)

	missing, err := FindActivePRConflictResolutionIssue(context.Background(), mock, 7, 123, "missing", "def456")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestFindActivePRConflictResolutionIssue_IgnoresBodySHAsWhenFrontMatterIsStale(t *testing.T) {
	body := BuildConflictResolutionIssueBody(ConflictResolutionIssueParams{
		Kind:         ConflictResolutionKindPRBase,
		Milestone:    &platform.Milestone{Number: 7, Title: "Batch 7"},
		BatchPR:      123,
		PRHeadBranch: "feature/refactor",
		PRHeadSHA:    "stale-head",
		BaseBranch:   "main",
		BaseSHA:      "stale-base",
		UserContext:  "The current conflict mentions abc123 and def456, but this resolver was created for stale SHAs.",
	})
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 100, Labels: []string{issues.StatusInProgress}, Body: body},
	}
	mock := &mockPlatform{issues: issueSvc}

	found, err := FindActivePRConflictResolutionIssue(context.Background(), mock, 7, 123, "abc123", "def456")

	require.NoError(t, err)
	assert.Nil(t, found)
}
