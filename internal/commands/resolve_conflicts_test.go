package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleResolveConflicts_ConflictingBatchPRCreatesFocusedIssue(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{}
	cfg := baseConfig()
	cfg.Workers.TimeoutMinutes = 45
	cfg.Workers.RunnerLabel = "large-runner"
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
	}
	hctx := resolveConflictsHandlerContext(p, cfg)

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts", Prompt: "Optional context for this resolver."})

	require.NoError(t, result.Error)
	assert.Equal(t, "🔧 Created conflict-resolution issue #200 and dispatched worker.", result.Message)
	require.Len(t, issueSvc.createdIssues, 1)
	created := issueSvc.createdIssues[0]
	assert.Contains(t, created.Labels, issues.TypeFix)
	assert.Contains(t, created.Labels, issues.StatusInProgress)
	require.Len(t, issueSvc.createdMilestones, 1)
	require.NotNil(t, issueSvc.createdMilestones[0])
	assert.Equal(t, 111, *issueSvc.createdMilestones[0])

	parsed, err := issues.ParseBody(created.Body)
	require.NoError(t, err)
	assert.Equal(t, "fix", parsed.FrontMatter.Type)
	assert.Equal(t, 111, parsed.FrontMatter.Batch)
	assert.Equal(t, 849, parsed.FrontMatter.BatchPR)
	assert.Equal(t, "head123", parsed.FrontMatter.PRHeadSHA)
	assert.Equal(t, "base456", parsed.FrontMatter.PRBaseSHA)
	assert.True(t, parsed.FrontMatter.ConflictResolution)
	assert.ElementsMatch(t, []string{
		"herd/batch/111-review-cycle-non-convergence-synthesis",
		"main",
	}, parsed.FrontMatter.ConflictingBranches)

	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "herd-worker.yml", wf.dispatchedWorkflows[0])
	assert.Equal(t, "main", wf.dispatchedRefs[0])
	assert.Equal(t, map[string]string{
		"issue_number":    "200",
		"batch_branch":    "herd/batch/111-review-cycle-non-convergence-synthesis",
		"timeout_minutes": "45",
		"runner_label":    "large-runner",
	}, wf.dispatched[0])
}

func TestHandleResolveConflicts_DispatchesUsingLivePRHeadBranch(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{}
	cfg := baseConfig()
	pr := conflictingResolveConflictsPR()
	pr.Head = "herd/batch/111-original-title"
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: pr,
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Renamed Milestone Title"}}},
	}
	hctx := resolveConflictsHandlerContext(p, cfg)

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.NoError(t, result.Error)
	assert.Equal(t, "🔧 Created conflict-resolution issue #200 and dispatched worker.", result.Message)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "herd/batch/111-original-title", wf.dispatched[0]["batch_branch"])
	require.Len(t, issueSvc.createdIssues, 1)
	assert.Contains(t, issueSvc.createdIssues[0].Body, "Head branch: `herd/batch/111-original-title`")
}

func TestHandleResolveConflicts_DispatchErrorReturnsError(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{dispatchErr: errors.New("workflow dispatch failed")}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.Error(t, result.Error)
	assert.Empty(t, result.Message)
	assert.Contains(t, result.Error.Error(), "dispatching conflict-resolution worker for issue #200")
	assert.Contains(t, result.Error.Error(), "workflow dispatch failed")
	require.Len(t, issueSvc.createdIssues, 1)
	assert.Empty(t, wf.dispatched)
	assert.Contains(t, issueSvc.removedLabels[200], issues.StatusInProgress)
	assert.Contains(t, issueSvc.addedLabels[200], issues.StatusFailed)
}

func TestHandleResolveConflicts_DoesNotIncludeHistoricalReviewFindings(t *testing.T) {
	const staleFinding = "OLD HERD REVIEW FINDING: stale no-op should not appear"
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	issueSvc.listCommentsResult = []*platform.Comment{{AuthorLogin: "herd", Body: staleFinding}}
	issueSvc.storedComments[849] = []*platform.Comment{{ID: 99, AuthorLogin: "herd", Body: staleFinding}}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts", Prompt: "Use the generated conflict state only."})

	require.NoError(t, result.Error)
	require.Len(t, issueSvc.createdIssues, 1)
	body := issueSvc.createdIssues[0].Body
	assert.NotContains(t, body, staleFinding)
	assert.NotContains(t, body, "## Conversation History")
	assert.Contains(t, body, "/herd resolve-conflicts")
	assert.Contains(t, body, "Use the generated conflict state only.")
	assert.Equal(t, 0, issueSvc.listCommentsCalls)

	contextIndex := strings.Index(body, "## Context")
	require.NotEqual(t, -1, contextIndex)
	fetchIndex := strings.Index(body, "`git fetch origin`")
	mergeIndex := strings.Index(body, "`git merge origin/main`")
	rebaseIndex := strings.Index(body, "`git rebase origin/main`")
	require.NotEqual(t, -1, fetchIndex)
	require.NotEqual(t, -1, mergeIndex)
	require.NotEqual(t, -1, rebaseIndex)
	assert.Less(t, fetchIndex, contextIndex)
	assert.Less(t, mergeIndex, contextIndex)
	assert.Less(t, rebaseIndex, contextIndex)
}

func TestHandleResolveConflicts_DuplicateActiveIssue(t *testing.T) {
	ms := &platform.Milestone{Number: 111, Title: "Review Cycle Non Convergence Synthesis"}
	existingBody := integrator.BuildConflictResolutionIssueBody(integrator.ConflictResolutionIssueParams{
		Kind:         integrator.ConflictResolutionKindPRBase,
		Milestone:    ms,
		BatchPR:      849,
		PRHeadBranch: "herd/batch/111-review-cycle-non-convergence-synthesis",
		PRHeadSHA:    "head123",
		BaseBranch:   "main",
		BaseSHA:      "base456",
	})
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 701, State: "open", Labels: []string{issues.TypeFix, issues.StatusInProgress}, Body: existingBody},
	}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: ms}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "#701")
	assert.Empty(t, issueSvc.createdIssues)
	assert.Empty(t, wf.dispatched)
}

func TestHandleResolveConflicts_FailedMatchingIssueDoesNotBlockNewResolver(t *testing.T) {
	ms := &platform.Milestone{Number: 111, Title: "Review Cycle Non Convergence Synthesis"}
	existingBody := integrator.BuildConflictResolutionIssueBody(integrator.ConflictResolutionIssueParams{
		Kind:         integrator.ConflictResolutionKindPRBase,
		Milestone:    ms,
		BatchPR:      849,
		PRHeadBranch: "herd/batch/111-review-cycle-non-convergence-synthesis",
		PRHeadSHA:    "head123",
		BaseBranch:   "main",
		BaseSHA:      "base456",
	})
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 701, State: "open", Labels: []string{issues.TypeFix, issues.StatusFailed}, Body: existingBody},
	}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: ms}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.NoError(t, result.Error)
	assert.Equal(t, "🔧 Created conflict-resolution issue #200 and dispatched worker.", result.Message)
	require.Len(t, issueSvc.createdIssues, 1)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "200", wf.dispatched[0]["issue_number"])
}

func TestHandleResolveConflicts_StaleResolverFrontMatterDoesNotBlockNewResolver(t *testing.T) {
	ms := &platform.Milestone{Number: 111, Title: "Review Cycle Non Convergence Synthesis"}
	existingBody := integrator.BuildConflictResolutionIssueBody(integrator.ConflictResolutionIssueParams{
		Kind:         integrator.ConflictResolutionKindPRBase,
		Milestone:    ms,
		BatchPR:      849,
		PRHeadBranch: "herd/batch/111-review-cycle-non-convergence-synthesis",
		PRHeadSHA:    "stale-head",
		BaseBranch:   "main",
		BaseSHA:      "stale-base",
		UserContext:  "The new conflict mentions head123 and base456 in discussion, but front matter is stale.",
	})
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 701, State: "open", Labels: []string{issues.TypeFix, issues.StatusInProgress}, Body: existingBody},
	}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: conflictingResolveConflictsPR(),
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: ms}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.NoError(t, result.Error)
	assert.Equal(t, "🔧 Created conflict-resolution issue #200 and dispatched worker.", result.Message)
	require.Len(t, issueSvc.createdIssues, 1)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "200", wf.dispatched[0]["issue_number"])
}

func TestHandleResolveConflicts_CleanPRNoOps(t *testing.T) {
	tests := []struct {
		name             string
		mergeStateStatus string
	}{
		{name: "clean status", mergeStateStatus: "CLEAN"},
		{name: "empty status with known mergeable", mergeStateStatus: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newTestIssueService()
			wf := &testWorkflowService{}
			prSvc := &testPRService{
				getResult: map[int]*platform.PullRequest{
					849: {
						Number:           849,
						Head:             "herd/batch/111-review-cycle-non-convergence-synthesis",
						Base:             "main",
						MergeableKnown:   true,
						Mergeable:        true,
						MergeStateStatus: tt.mergeStateStatus,
					},
				},
			}
			p := &testPlatform{
				issues:     issueSvc,
				prs:        prSvc,
				workflows:  wf,
				repo:       &testRepoService{defaultBranch: "main"},
				milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
			}
			hctx := resolveConflictsHandlerContext(p, baseConfig())

			result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

			require.NoError(t, result.Error)
			assert.Equal(t, "ℹ️ PR is not currently conflicting with base.", result.Message)
			assert.Empty(t, issueSvc.createdIssues)
			assert.Empty(t, wf.dispatched)
		})
	}
}

func TestHandleResolveConflicts_BlockedPRNoOps(t *testing.T) {
	issueSvc := newTestIssueService()
	wf := &testWorkflowService{}
	pr := conflictingResolveConflictsPR()
	pr.MergeStateStatus = "BLOCKED"
	pr.MergeableKnown = true
	pr.Mergeable = false
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			849: pr,
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
	}
	hctx := resolveConflictsHandlerContext(p, baseConfig())

	result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

	require.NoError(t, result.Error)
	assert.Equal(t, "ℹ️ PR is not currently conflicting with base.", result.Message)
	assert.Empty(t, issueSvc.createdIssues)
	assert.Empty(t, wf.dispatched)
}

func TestHandleResolveConflicts_ConflictStatusesDispatchOnce(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "dirty", status: "DIRTY"},
		{name: "conflicting", status: "CONFLICTING"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newTestIssueService()
			issueSvc.listResult = []*platform.Issue{}
			wf := &testWorkflowService{}
			pr := conflictingResolveConflictsPR()
			pr.MergeStateStatus = tt.status
			prSvc := &testPRService{
				getResult: map[int]*platform.PullRequest{
					849: pr,
				},
			}
			p := &testPlatform{
				issues:     issueSvc,
				prs:        prSvc,
				workflows:  wf,
				repo:       &testRepoService{defaultBranch: "main"},
				milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
			}
			hctx := resolveConflictsHandlerContext(p, baseConfig())

			result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

			require.NoError(t, result.Error)
			assert.Equal(t, "🔧 Created conflict-resolution issue #200 and dispatched worker.", result.Message)
			require.Len(t, issueSvc.createdIssues, 1)
			require.Len(t, wf.dispatched, 1)
			assert.Equal(t, "200", wf.dispatched[0]["issue_number"])
		})
	}
}

func TestHandleResolveConflicts_UnknownMergeabilityRetriesBounded(t *testing.T) {
	tests := []struct {
		name             string
		mergeStateStatus string
	}{
		{name: "unknown", mergeStateStatus: "UNKNOWN"},
		{name: "unavailable", mergeStateStatus: "unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origSleep := resolveConflictsSleep
			sleepCalls := 0
			resolveConflictsSleep = func(context.Context, time.Duration) error {
				sleepCalls++
				return nil
			}
			t.Cleanup(func() { resolveConflictsSleep = origSleep })

			unknownPR := &platform.PullRequest{
				Number:           849,
				Head:             "herd/batch/111-review-cycle-non-convergence-synthesis",
				Base:             "main",
				HeadSHA:          "head123",
				BaseSHA:          "base456",
				MergeableKnown:   false,
				Mergeable:        false,
				MergeStateStatus: tt.mergeStateStatus,
			}
			issueSvc := newTestIssueService()
			wf := &testWorkflowService{}
			prSvc := &testPRService{
				getSequences: map[int][]*platform.PullRequest{
					849: {unknownPR, unknownPR, unknownPR, unknownPR},
				},
			}
			p := &testPlatform{
				issues:     issueSvc,
				prs:        prSvc,
				workflows:  wf,
				repo:       &testRepoService{defaultBranch: "main"},
				milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{111: {Number: 111, Title: "Review Cycle Non Convergence Synthesis"}}},
			}
			hctx := resolveConflictsHandlerContext(p, baseConfig())

			result := handleResolveConflicts(hctx, Command{Name: "resolve-conflicts"})

			require.NoError(t, result.Error)
			assert.Contains(t, result.Message, "could not determine whether this PR is currently conflicting")
			assert.Equal(t, resolveConflictsMergeabilityAttempts, prSvc.getCalls[849])
			assert.Equal(t, resolveConflictsMergeabilityAttempts-1, sleepCalls)
			assert.Empty(t, issueSvc.createdIssues)
			assert.Empty(t, wf.dispatched)
		})
	}
}

func TestResolveConflictPRStateClassification(t *testing.T) {
	tests := []struct {
		name         string
		pr           *platform.PullRequest
		wantConflict bool
		wantClean    bool
		wantKnown    bool
	}{
		{name: "DIRTY", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: false, MergeStateStatus: "DIRTY"}, wantConflict: true, wantKnown: true},
		{name: "CONFLICTING", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: false, MergeStateStatus: "CONFLICTING"}, wantConflict: true, wantKnown: true},
		{name: "CLEAN", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: true, MergeStateStatus: "CLEAN"}, wantClean: true, wantKnown: true},
		{name: "BEHIND", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: true, MergeStateStatus: "BEHIND"}, wantClean: true, wantKnown: true},
		{name: "HAS_HOOKS", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: true, MergeStateStatus: "HAS_HOOKS"}, wantClean: true, wantKnown: true},
		{name: "UNSTABLE", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: true, MergeStateStatus: "UNSTABLE"}, wantClean: true, wantKnown: true},
		{name: "UNKNOWN", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: "UNKNOWN"}},
		{name: "UNAVAILABLE", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: "unavailable"}},
		{name: "MergeableKnown=false", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: ""}},
		{name: "known unmergeable empty status", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: false, MergeStateStatus: ""}, wantKnown: true},
		{name: "BLOCKED non-conflict", pr: &platform.PullRequest{MergeableKnown: true, Mergeable: false, MergeStateStatus: "BLOCKED"}, wantKnown: true},
		{name: "unknown BLOCKED", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: "BLOCKED"}},
		{name: "authoritative dirty with unknown mergeable flag", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: "DIRTY"}, wantConflict: true, wantKnown: true},
		{name: "authoritative clean with unknown mergeable flag", pr: &platform.PullRequest{MergeableKnown: false, Mergeable: false, MergeStateStatus: "CLEAN"}, wantClean: true, wantKnown: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantConflict, prReportsConflict(tt.pr))
			assert.Equal(t, tt.wantClean, prReportsClean(tt.pr))
			assert.Equal(t, tt.wantKnown, prMergeabilityKnown(tt.pr))
		})
	}
}

func conflictingResolveConflictsPR() *platform.PullRequest {
	return &platform.PullRequest{
		Number:           849,
		Head:             "herd/batch/111-review-cycle-non-convergence-synthesis",
		Base:             "main",
		HeadSHA:          "head123",
		BaseSHA:          "base456",
		MergeableKnown:   true,
		Mergeable:        false,
		MergeStateStatus: "DIRTY",
	}
}

func resolveConflictsHandlerContext(p *testPlatform, cfg *config.Config) *HandlerContext {
	return &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      cfg,
		IssueNumber: 849,
		IssueBody:   "/herd resolve-conflicts\n\nUse the generated conflict state only.",
		AuthorLogin: "octocat",
		IsPR:        true,
	}
}
