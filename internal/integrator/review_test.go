package integrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	agentprompt "github.com/herd-os/herd/internal/agent/prompt"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/reviewdiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestRepo creates a minimal git repo with main and batch branches for testing.
func initTestRepo(t *testing.T) (string, *git.Git) {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "-b", "main", dir},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
		{"git", "-C", dir, "branch", "herd/batch/1-batch"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "cmd %v failed: %s", args, string(out))
	}
	return dir, git.New(dir)
}

func runReviewTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

func reviewTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
	return strings.TrimSpace(string(out))
}

func TestLivePRMergeState(t *testing.T) {
	tests := []struct {
		name string
		pr   *platform.PullRequest
		want prMergeState
	}{
		{
			name: "nil PR is unknown",
			want: prMergeState{Unknown: true},
		},
		{
			name: "known mergeable is clean",
			pr: &platform.PullRequest{
				MergeableKnown: true,
				Mergeable:      true,
			},
			want: prMergeState{
				MergeableKnown: true,
				Mergeable:      true,
				Clean:          true,
			},
		},
		{
			name: "clean status is clean",
			pr: &platform.PullRequest{
				MergeStateStatus: " clean ",
			},
			want: prMergeState{
				MergeStateStatus: "CLEAN",
				Clean:            true,
			},
		},
		{
			name: "clean status with known unmergeable is not clean",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "CLEAN",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "CLEAN",
			},
		},
		{
			name: "dirty status is blocking",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				MergeStateStatus: "DIRTY",
			},
			want: prMergeState{
				MergeableKnown:   true,
				MergeStateStatus: "DIRTY",
				Blocking:         true,
			},
		},
		{
			name: "dirty status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "DIRTY",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "DIRTY",
				Blocking:         true,
			},
		},
		{
			name: "blocked status is blocking",
			pr: &platform.PullRequest{
				MergeStateStatus: "BLOCKED",
			},
			want: prMergeState{
				MergeStateStatus: "BLOCKED",
				Blocking:         true,
			},
		},
		{
			name: "blocked status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BLOCKED",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BLOCKED",
				Blocking:         true,
			},
		},
		{
			name: "behind status is blocking",
			pr: &platform.PullRequest{
				MergeStateStatus: "BEHIND",
			},
			want: prMergeState{
				MergeStateStatus: "BEHIND",
				Blocking:         true,
			},
		},
		{
			name: "behind status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BEHIND",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BEHIND",
				Blocking:         true,
			},
		},
		{
			name: "unstable status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "UNSTABLE",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "UNSTABLE",
				Blocking:         true,
			},
		},
		{
			name: "has hooks status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "HAS_HOOKS",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "HAS_HOOKS",
				Blocking:         true,
			},
		},
		{
			name: "unrecognized status overrides known mergeable",
			pr: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "QUEUED",
			},
			want: prMergeState{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "QUEUED",
			},
		},
		{
			name: "unknown status is blocking and unknown",
			pr: &platform.PullRequest{
				MergeStateStatus: "UNKNOWN",
			},
			want: prMergeState{
				MergeStateStatus: "UNKNOWN",
				Blocking:         true,
				Unknown:          true,
			},
		},
		{
			name: "absent mergeability is unknown",
			pr:   &platform.PullRequest{},
			want: prMergeState{Unknown: true},
		},
		{
			name: "known unmergeable without status is not clean or unknown",
			pr: &platform.PullRequest{
				MergeableKnown: true,
			},
			want: prMergeState{MergeableKnown: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, livePRMergeState(tt.pr))
		})
	}
}

func TestIsCascadeOrMergeConflictFinding(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want bool
	}{
		{name: "cascade label", desc: "herd/cascade-failed is present", want: true},
		{name: "unresolved merge conflict", desc: "Unresolved merge conflict blocks this PR", want: true},
		{name: "unresolved merge conflict without pr phrase", desc: "Unresolved merge conflict blocks merge", want: true},
		{name: "branch conflict", desc: "Branch conflict remains between batch and base", want: true},
		{name: "file-level cascade label docs", desc: "docs/design/execution.md: herd/cascade-failed docs still imply the label can only be removed manually", want: false},
		{name: "symbol-level cascade label behavior", desc: "function CascadeFailed cleanup is inconsistent with label behavior", want: false},
		{name: "file-level current pr merge state", desc: "internal/merge/resolver.go: GitHub reports this PR has an unresolved merge conflict", want: true},
		{name: "file-level merge conflict logic", desc: "internal/merge/resolver.go: resolve merge conflicts ignores errors", want: false},
		{name: "root file-level merge conflict logic", desc: "go.mod: merge conflict marker dependency resolution is wrong", want: false},
		{name: "root file-level cascade label behavior", desc: "package.json: herd/cascade-failed cleanup script ignores errors", want: false},
		{name: "typescript file-level merge conflict logic", desc: "web/src/mergeResolver.ts: resolve merge conflicts ignores errors", want: false},
		{name: "python file-level merge conflict logic", desc: "scripts/merge_resolver.py: unresolved conflict handler drops errors", want: false},
		{name: "symbol-level merge conflict logic", desc: "function resolveConflicts ignores errors while resolving merge conflicts", want: false},
		{name: "conflict in business logic only", desc: "The new option conflicts with documented behavior in settings.go", want: false},
		{name: "empty", desc: " ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isCascadeOrMergeConflictFinding(agent.ReviewFinding{Description: tt.desc}))
		})
	}
}

func TestReconcileReviewFindingsWithLivePRState(t *testing.T) {
	cascadeFinding := agent.ReviewFinding{Severity: "HIGH", Description: "unresolved merge conflict with the base branch"}
	codeFinding := agent.ReviewFinding{Severity: "HIGH", Description: "internal/merge/resolver.go: resolve merge conflicts ignores errors"}

	tests := []struct {
		name              string
		pr                *platform.PullRequest
		wantDescriptions  []string
		wantRemovedLabel  bool
		wantIgnoredCount  int
		wantStaleLabel    bool
		wantLabelRemoved  bool
		wantRemoveErrText string
		removeErr         error
	}{
		{
			name: "clean removes label and filters only cascade finding",
			pr: &platform.PullRequest{
				Number:           50,
				MergeStateStatus: "CLEAN",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{codeFinding.Description},
			wantRemovedLabel: true,
			wantIgnoredCount: 1,
			wantStaleLabel:   true,
			wantLabelRemoved: true,
		},
		{
			name: "clean keeps non-cascade conflict mention",
			pr: &platform.PullRequest{
				Number:           50,
				MergeStateStatus: "CLEAN",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{codeFinding.Description},
			wantRemovedLabel: true,
			wantIgnoredCount: 1,
			wantStaleLabel:   true,
			wantLabelRemoved: true,
		},
		{
			name: "clean status with known unmergeable preserves findings and label",
			pr: &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "CLEAN",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name: "blocked preserves all findings and label",
			pr: &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BLOCKED",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name: "behind preserves all findings and label when mergeable true",
			pr: &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BEHIND",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name: "unstable preserves all findings and label when mergeable true",
			pr: &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "UNSTABLE",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name: "has hooks preserves all findings and label when mergeable true",
			pr: &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "HAS_HOOKS",
				Labels:           []string{issues.CascadeFailed},
			},
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name:             "unknown nil PR preserves all findings",
			wantDescriptions: []string{cascadeFinding.Description, codeFinding.Description},
		},
		{
			name: "clean cleanup error still filters",
			pr: &platform.PullRequest{
				Number:           50,
				MergeStateStatus: "CLEAN",
				Labels:           []string{issues.CascadeFailed},
			},
			removeErr:         errors.New("remove failed"),
			wantDescriptions:  []string{codeFinding.Description},
			wantRemovedLabel:  true,
			wantIgnoredCount:  1,
			wantStaleLabel:    true,
			wantRemoveErrText: "remove failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.removeLabelsErr = tt.removeErr

			got, stats := reconcileReviewFindingsWithLivePRState(context.Background(), issueSvc, tt.pr, []agent.ReviewFinding{cascadeFinding, codeFinding})

			gotDescriptions := make([]string, 0, len(got))
			for _, finding := range got {
				gotDescriptions = append(gotDescriptions, finding.Description)
			}
			assert.Equal(t, tt.wantDescriptions, gotDescriptions)
			assert.Equal(t, tt.wantIgnoredCount, stats.StalePRStateFindingsIgnored)
			assert.Equal(t, tt.wantStaleLabel, stats.CascadeLabelWasStale)
			assert.Equal(t, tt.wantLabelRemoved, stats.CascadeLabelRemoved)
			if tt.wantRemoveErrText == "" {
				assert.Empty(t, stats.CascadeLabelRemoveError)
			} else {
				assert.Contains(t, stats.CascadeLabelRemoveError, tt.wantRemoveErrText)
			}
			if tt.wantRemovedLabel {
				assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
			} else {
				assert.Empty(t, issueSvc.removedLabels[50])
			}
		})
	}
}

func TestReconcileReviewFindingsWithLivePRStatePreservesRootFileFindings(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "merge conflict logic", description: "go.mod: merge conflict marker dependency resolution is wrong"},
		{name: "cascade label logic", description: "package.json: herd/cascade-failed cleanup script ignores errors"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &platform.PullRequest{
				Number:           50,
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			}
			finding := agent.ReviewFinding{Severity: "HIGH", Description: tt.description}

			got, stats := reconcileReviewFindingsWithLivePRState(context.Background(), newMockIssueService(), pr, []agent.ReviewFinding{finding})

			require.Len(t, got, 1)
			assert.Equal(t, tt.description, got[0].Description)
			assert.Zero(t, stats.StalePRStateFindingsIgnored)
		})
	}
}

// --- Mock Agent ---

type mockReviewAgent struct {
	reviewResult *agent.ReviewResult
	reviewErr    error
	onReview     func()
	// results, when non-nil, returns scripted ReviewResults on successive
	// calls. After the slice is exhausted, the last entry is repeated.
	results  []*agent.ReviewResult
	calls    int
	lastDiff string
	lastOpts agent.ReviewOptions
}

func (m *mockReviewAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *mockReviewAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *mockReviewAgent) Review(_ context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	m.lastDiff = diff
	m.lastOpts = opts
	if m.onReview != nil {
		m.onReview()
	}
	if m.results != nil {
		idx := m.calls
		if idx >= len(m.results) {
			idx = len(m.results) - 1
		}
		m.calls++
		return m.results[idx], m.reviewErr
	}
	m.calls++
	return m.reviewResult, m.reviewErr
}
func (m *mockReviewAgent) Discuss(_ context.Context, _ agent.DiscussOptions) error {
	return nil
}

type nilRefreshPRService struct {
	*mockPRService
}

func (m *nilRefreshPRService) Get(context.Context, int) (*platform.PullRequest, error) {
	return nil, nil
}

// Helper to build a standard test platform for review tests
func newReviewTestPlatform(prList []*platform.PullRequest, milestoneIssues []*platform.Issue) *mockPlatform {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = milestoneIssues

	return &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: prList,
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}
}

func newReviewLockTestPlatform(issueSvc platform.IssueService) *mockPlatform {
	baseIssueSvc, ok := issueSvc.(*mockIssueService)
	if ok {
		baseIssueSvc.listResult = []*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
		}
	}
	return &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				1: {Number: 1, Title: "Batch"},
			},
		},
	}
}

func mustReviewLockComment(t *testing.T, state reviewLockState) string {
	t.Helper()
	body, err := buildReviewLockComment(state)
	require.NoError(t, err)
	return body
}

func mustReviewLockCommitMessage(t *testing.T, state reviewLockState) string {
	t.Helper()
	body, err := buildReviewLockCommitMessage(state)
	require.NoError(t, err)
	return body
}

func legacyReviewLockCommitMessage(prNumber, batchNumber int, owner string, acquiredAt time.Time) string {
	return fmt.Sprintf("Herd review lock\n\npr: %d\nbatch: %d\nowner: %s\nacquired_at: %s\ntoken: legacy-token", prNumber, batchNumber, owner, acquiredAt.UTC().Format(time.RFC3339Nano))
}

func reviewLockCommentCount(comments []*platform.Comment, prNumber int) int {
	count := 0
	for _, c := range comments {
		state, ok := parseReviewLockComment(c.Body)
		if ok && state.PRNumber == prNumber {
			count++
		}
	}
	return count
}

func TestParseReviewLockComment(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	acquiredAt := now
	expiresAt := now.Add(reviewLockExpiry)
	valid := reviewLockState{
		PRNumber:    50,
		BatchNumber: 1,
		RunID:       100,
		Owner:       "test",
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiresAt,
	}
	validBody := mustReviewLockComment(t, valid)

	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "valid", body: validBody, want: true},
		{name: "valid with surrounding text", body: "prefix\n" + validBody + "\nsuffix", want: true},
		{name: "malformed json", body: reviewLockMarkerPrefix + `{"pr_number":` + reviewLockMarkerSuffix, want: false},
		{name: "missing suffix", body: reviewLockMarkerPrefix + `{}`, want: false},
		{name: "no marker", body: "ordinary comment", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseReviewLockComment(tt.body)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Equal(t, valid.PRNumber, got.PRNumber)
				assert.Equal(t, valid.BatchNumber, got.BatchNumber)
				assert.Equal(t, valid.RunID, got.RunID)
			}
		})
	}
}

func TestParseLegacyReviewLockCommitMessage(t *testing.T) {
	acquiredAt := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name        string
		message     string
		wantOK      bool
		wantPR      int
		wantBatch   int
		wantOwner   string
		wantExpires time.Time
	}{
		{
			name:        "valid legacy marker",
			message:     legacyReviewLockCommitMessage(50, 7, "old-owner-7", acquiredAt),
			wantOK:      true,
			wantPR:      50,
			wantBatch:   7,
			wantOwner:   "old-owner-7",
			wantExpires: acquiredAt.Add(reviewLockExpiry),
		},
		{
			name:        "valid legacy marker with time string format",
			message:     fmt.Sprintf("Herd review lock\n\npr: 50\nbatch: 1\nowner: old-owner\nacquired_at: %s\ntoken: legacy-token", acquiredAt.Format("2006-01-02 15:04:05 -0700 MST")),
			wantOK:      true,
			wantPR:      50,
			wantBatch:   1,
			wantOwner:   "old-owner",
			wantExpires: acquiredAt.Add(reviewLockExpiry),
		},
		{name: "missing herd header", message: "Other lock\n\npr: 50\nacquired_at: 2026-07-01T09:30:00Z", wantOK: false},
		{name: "missing pr", message: "Herd review lock\n\nbatch: 1\nacquired_at: 2026-07-01T09:30:00Z", wantOK: false},
		{name: "invalid pr", message: "Herd review lock\n\npr: 50x\nbatch: 1\nacquired_at: 2026-07-01T09:30:00Z", wantOK: false},
		{name: "missing acquired_at", message: "Herd review lock\n\npr: 50\nbatch: 1", wantOK: false},
		{name: "invalid acquired_at", message: "Herd review lock\n\npr: 50\nbatch: 1\nacquired_at: yesterday", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLegacyReviewLockCommitMessage(tt.message)

			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, "locked", got.Status)
				assert.Equal(t, tt.wantPR, got.PRNumber)
				assert.Equal(t, tt.wantBatch, got.BatchNumber)
				assert.Equal(t, tt.wantOwner, got.Owner)
				require.NotNil(t, got.AcquiredAt)
				require.NotNil(t, got.ExpiresAt)
				assert.Equal(t, tt.wantExpires, *got.ExpiresAt)
			}
		})
	}
}

func TestAcquireReviewLock_FastForwardConflictBlocksDuplicate(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	issueSvc := newMockIssueService()
	repoSvc := &mockRepoService{branchExists: map[string]bool{"herd/batch/1-batch": true}}

	first, acquired, err := acquireReviewLock(context.Background(), issueSvc, repoSvc, 50, 1, 100, "abc123", now)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, first)

	require.NoError(t, releaseReviewLock(context.Background(), issueSvc, repoSvc, first))
	unlockedSHA := repoSvc.branchSHAs[lockBranch]
	unlockedCommitCount := repoSvc.markerCommitSeq
	repoSvc.onUpdateBranch = func(name, _ string) {
		if name != lockBranch || repoSvc.branchSHAs[lockBranch] != unlockedSHA {
			return
		}
		winnerState := lockedReviewLockState(50, 1, 999, "abc123", "winner-lock", now)
		winnerSHA, createErr := repoSvc.CreateCommit(context.Background(), unlockedSHA, mustReviewLockCommitMessage(t, winnerState))
		require.NoError(t, createErr)
		repoSvc.branchSHAs[lockBranch] = winnerSHA
	}

	second, acquired, err := acquireReviewLock(context.Background(), issueSvc, repoSvc, 50, 1, 101, "abc123", now)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, second)
	assert.GreaterOrEqual(t, repoSvc.markerCommitSeq, unlockedCommitCount+2, "loser created a candidate before losing the fast-forward update")
	headState, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "winner-lock", headState.LockID)
	assert.True(t, repoSvc.branchExists[lockBranch])
}

func TestAcquireReviewLock_ExpiredLockIsReclaimedByAppendingCommit(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	expiredAt := now.Add(-time.Minute)
	acquiredAt := now.Add(-3 * time.Hour)
	expiredState := reviewLockState{
		Kind:        "herd-review-lock",
		Version:     1,
		Status:      "locked",
		LockID:      "expired",
		PRNumber:    50,
		BatchNumber: 1,
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiredAt,
	}
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "expired-sha"},
		commitMessages: map[string]string{
			"expired-sha": mustReviewLockCommitMessage(t, expiredState),
		},
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)
	assert.True(t, strings.HasPrefix(repoSvc.branchSHAs[lockBranch], "expired-sha-lock-"))
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "locked", state.Status)
	assert.NotEqual(t, "expired", state.LockID)
}

func TestAcquireReviewLock_ConcurrentStaleReclaimLoserObservesWinner(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	expiredAt := now.Add(-time.Minute)
	acquiredAt := now.Add(-3 * time.Hour)
	expiredState := reviewLockState{Kind: "herd-review-lock", Version: 1, Status: "locked", LockID: "expired", PRNumber: 50, BatchNumber: 1, AcquiredAt: &acquiredAt, ExpiresAt: &expiredAt}
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "expired-sha"},
		commitMessages: map[string]string{
			"expired-sha": mustReviewLockCommitMessage(t, expiredState),
		},
	}
	repoSvc.onUpdateBranch = func(name, _ string) {
		if name != lockBranch || repoSvc.branchSHAs[lockBranch] != "expired-sha" {
			return
		}
		winnerState := lockedReviewLockState(50, 1, 999, "new-sha", "winner-lock", now)
		winnerSHA, createErr := repoSvc.CreateCommit(context.Background(), "expired-sha", mustReviewLockCommitMessage(t, winnerState))
		require.NoError(t, createErr)
		repoSvc.branchSHAs[lockBranch] = winnerSHA
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, handle)
	headState, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "winner-lock", headState.LockID)
}

func TestReleaseReviewLockOnlyUnlocksMatchingLockID(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	active := lockedReviewLockState(50, 1, 200, "sha", "new-lock", now)
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "new-lock-sha"},
		commitMessages: map[string]string{
			"new-lock-sha": mustReviewLockCommitMessage(t, active),
		},
	}
	oldHandle := &reviewLockHandle{branch: lockBranch, state: lockedReviewLockState(50, 1, 100, "sha", "old-lock", now)}

	require.NoError(t, releaseReviewLock(context.Background(), newMockIssueService(), repoSvc, oldHandle))
	assert.Equal(t, "new-lock-sha", repoSvc.branchSHAs[lockBranch])
	assert.Equal(t, 0, repoSvc.markerCommitSeq)
}

func TestReleaseReviewLockOldHolderCannotUnlockNewerLock(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	oldHandle := &reviewLockHandle{branch: lockBranch, state: lockedReviewLockState(50, 1, 100, "sha", "old-lock", now)}
	active := lockedReviewLockState(50, 1, 200, "sha", "new-lock", now)
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "new-lock-sha"},
		commitMessages: map[string]string{
			"new-lock-sha": mustReviewLockCommitMessage(t, active),
		},
	}

	require.NoError(t, releaseReviewLock(context.Background(), newMockIssueService(), repoSvc, oldHandle))
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "locked", state.Status)
	assert.Equal(t, "new-lock", state.LockID)
}

func TestReleaseReviewLockConflictRetryUnlocksOwnLock(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	active := lockedReviewLockState(50, 1, 100, "sha", "own-lock", now)
	repoSvc := &mockRepoService{
		branchExists:    map[string]bool{lockBranch: true},
		branchSHAs:      map[string]string{lockBranch: "own-lock-sha"},
		updateConflicts: 1,
		commitMessages: map[string]string{
			"own-lock-sha": mustReviewLockCommitMessage(t, active),
		},
	}
	handle := &reviewLockHandle{branch: lockBranch, state: active}

	require.NoError(t, releaseReviewLock(context.Background(), newMockIssueService(), repoSvc, handle))
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "unlocked", state.Status)
	assert.Equal(t, "own-lock", state.ReleasedLockID)
	assert.Equal(t, 0, repoSvc.updateConflicts)
}

func TestAcquireReviewLock_BranchCreationRaceReadsExistingBranch(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	releasedAt := now.Add(-time.Minute)
	unlocked := reviewLockState{Kind: "herd-review-lock", Version: 1, Status: "unlocked", PRNumber: 50, BatchNumber: 1, ReleasedAt: &releasedAt}
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "existing-unlocked"},
		commitMessages: map[string]string{
			"existing-unlocked": mustReviewLockCommitMessage(t, unlocked),
		},
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)
	assert.True(t, strings.HasPrefix(repoSvc.branchSHAs[lockBranch], "existing-unlocked-lock-"))
}

func TestAcquireReviewLock_StaleLegacyBranchStateIsMigratedByAppendingCommit(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "legacy-sha"},
		commitMessages: map[string]string{
			"legacy-sha": legacyReviewLockCommitMessage(50, 1, "old-owner", now.Add(-reviewLockExpiry-time.Minute)),
		},
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)

	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)
	newHead := repoSvc.branchSHAs[lockBranch]
	assert.Equal(t, "legacy-sha", repoSvc.commitParents[newHead])
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[newHead])
	require.True(t, ok)
	assert.Equal(t, "locked", state.Status)
	assert.Equal(t, 50, state.PRNumber)
	assert.NotEmpty(t, state.LockID)
}

func TestAcquireReviewLock_FreshLegacyBranchStateStillBlocks(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "legacy-sha"},
		commitMessages: map[string]string{
			"legacy-sha": legacyReviewLockCommitMessage(50, 1, "old-owner", now.Add(-time.Minute)),
		},
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)

	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, handle)
	assert.Equal(t, "legacy-sha", repoSvc.branchSHAs[lockBranch])
	assert.NotContains(t, repoSvc.commitParents, "legacy-sha")
}

func TestAcquireReviewLock_LegacyMigrationConflictRetries(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	repoSvc := &mockRepoService{
		branchExists: map[string]bool{lockBranch: true},
		branchSHAs:   map[string]string{lockBranch: "legacy-sha"},
		commitMessages: map[string]string{
			"legacy-sha": legacyReviewLockCommitMessage(50, 1, "old-owner", now.Add(-reviewLockExpiry-time.Minute)),
		},
	}
	repoSvc.onUpdateBranch = func(name, _ string) {
		if name != lockBranch || repoSvc.branchSHAs[lockBranch] != "legacy-sha" {
			return
		}
		winnerState := lockedReviewLockState(50, 1, 999, "new-sha", "winner-lock", now)
		winnerSHA, createErr := repoSvc.CreateCommit(context.Background(), "legacy-sha", mustReviewLockCommitMessage(t, winnerState))
		require.NoError(t, createErr)
		repoSvc.branchSHAs[lockBranch] = winnerSHA
	}

	handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)

	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, handle)
	headState, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "winner-lock", headState.LockID)
	assert.GreaterOrEqual(t, repoSvc.markerCommitSeq, 2, "loser created a candidate before retrying after conflict")
}

func TestAcquireReviewLock_MalformedBranchStateFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lockBranch := reviewLockBranch(50)
	tests := []struct {
		name    string
		message string
	}{
		{name: "malformed json", message: `{"kind":`},
		{name: "legacy missing acquired_at", message: "Herd review lock\n\npr: 50\nbatch: 1\nowner: old"},
		{name: "legacy wrong pr", message: legacyReviewLockCommitMessage(51, 1, "old-owner", now.Add(-reviewLockExpiry-time.Minute))},
		{name: "non-herd non-json", message: "Some other lock\n\npr: 50\nacquired_at: 2026-07-01T09:00:00Z"},
		{name: "wrong kind", message: `{"kind":"other","version":1,"status":"unlocked","pr_number":50}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoSvc := &mockRepoService{
				branchExists:   map[string]bool{lockBranch: true},
				branchSHAs:     map[string]string{lockBranch: "bad-sha"},
				commitMessages: map[string]string{"bad-sha": tt.message},
			}

			handle, acquired, err := acquireReviewLock(context.Background(), newMockIssueService(), repoSvc, 50, 1, 100, "new-sha", now)

			require.NoError(t, err)
			assert.False(t, acquired)
			assert.Nil(t, handle)
			assert.Equal(t, "bad-sha", repoSvc.branchSHAs[lockBranch])
		})
	}
}

func TestFilterReviewLockCommentsRemovesDiagnosticLockState(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	acquiredAt := now
	expiresAt := now.Add(reviewLockExpiry)
	body := mustReviewLockComment(t, reviewLockState{PRNumber: 50, BatchNumber: 1, AcquiredAt: &acquiredAt, ExpiresAt: &expiresAt})

	got := filterReviewLockComments([]*platform.Comment{
		{ID: 1, Body: "ordinary"},
		{ID: 2, Body: body},
	})

	require.Len(t, got, 1)
	assert.Equal(t, int64(1), got[0].ID)
}

func TestReview_ManualActiveLockSkipCommentsWithDiagnostics(t *testing.T) {
	issueSvc := newMockIssueService()
	now := time.Now().UTC()
	lockBranch := reviewLockBranch(50)
	active := lockedReviewLockState(50, 1, 99, "sha-recorded", "active-lock", now)
	active.Owner = "review-owner"
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true, lockBranch: true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-current", lockBranch: "active-sha"},
		commitMessages: map[string]string{
			"active-sha": mustReviewLockCommitMessage(t, active),
		},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}
	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(issueSvc)
	mock.repo = repoSvc
	mock.prs = prSvc

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir, Manual: true})

	require.NoError(t, err)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.Equal(t, 0, ag.calls)
	require.Len(t, prSvc.comments, 1)
	comment := prSvc.comments[0]
	assert.Contains(t, strings.ToLower(comment), "skipped")
	assert.Contains(t, strings.ToLower(comment), "active")
	assert.Contains(t, comment, "review-owner")
	assert.Contains(t, comment, formatReviewLockTime(active.AcquiredAt))
	assert.Contains(t, comment, formatReviewLockTime(active.ExpiresAt))
	assert.Contains(t, comment, "sha-recorded")
	assert.Contains(t, comment, "sha-current")
}

func TestReview_AutomaticActiveLockSkipOnlyLogsOrAtLeastDoesNotComment(t *testing.T) {
	tests := []struct {
		name      string
		manual    bool
		wantCount int
	}{
		{name: "automatic review", manual: false, wantCount: 0},
		{name: "manual review", manual: true, wantCount: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			now := time.Now().UTC()
			lockBranch := reviewLockBranch(50)
			active := lockedReviewLockState(50, 1, 99, "abc123", "active-lock", now)
			repoSvc := &mockRepoService{
				defaultBranch: "main",
				branchExists:  map[string]bool{"herd/batch/1-batch": true, lockBranch: true},
				branchSHAs:    map[string]string{"herd/batch/1-batch": "current-sha", lockBranch: "active-sha"},
				commitMessages: map[string]string{
					"active-sha": mustReviewLockCommitMessage(t, active),
				},
			}
			prSvc := &mockCapturingPRService{
				mockPRService: &mockPRService{
					getResult: map[int]*platform.PullRequest{
						50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
					},
				},
			}

			ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}
			dir, g := initTestRepo(t)
			mock := newReviewLockTestPlatform(issueSvc)
			mock.repo = repoSvc
			mock.prs = prSvc
			result, err := Review(context.Background(), mock, ag, g, &config.Config{
				Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
			}, ReviewParams{PRNumber: 50, RepoRoot: dir, Manual: tt.manual})

			require.NoError(t, err)
			assert.Equal(t, 50, result.BatchPRNumber)
			assert.Equal(t, 0, ag.calls)
			assert.Len(t, prSvc.comments, tt.wantCount)
		})
	}
}

func TestReview_DiscardsReviewResultWhenHeadAdvances(t *testing.T) {
	issueSvc := newMockIssueService()
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return issueSvc.Create(context.Background(), title, body, labels, milestone)
		},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-old"},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		onReview: func() {
			repoSvc.branchSHAs["herd/batch/1-batch"] = "sha-new"
		},
	}
	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = repoSvc

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.False(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	assert.Empty(t, prSvc.reviews)
	assert.Equal(t, 0, createdIssues)
	assert.Empty(t, wf.dispatched)
	assert.False(t, prSvc.merged)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "sha-old")
	assert.Contains(t, prSvc.comments[0], "sha-new")
	assert.Contains(t, strings.ToLower(prSvc.comments[0]), "discarded")
	assert.Contains(t, strings.ToLower(prSvc.comments[0]), "changed")
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_DiscardsFallbackPreparedDiffReviewResultWhenHeadAdvances(t *testing.T) {
	issueSvc := newMockIssueService()
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return issueSvc.Create(context.Background(), title, body, labels, milestone)
		},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffErr: platform.ErrPullRequestDiffTooLarge,
			listFilesResult: []*platform.PullRequestFile{
				{
					Path:      "src/fallback.go",
					Status:    "modified",
					Additions: 1,
					Deletions: 1,
					Changes:   2,
					Patch:     "@@ -1 +1 @@\n-old\n+fallback\n",
				},
			},
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-old"},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		onReview: func() {
			repoSvc.branchSHAs["herd/batch/1-batch"] = "sha-new"
		},
	}
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = repoSvc

	result, err := Review(context.Background(), mock, ag, nil, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.False(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, prSvc.getDiffCalled)
	assert.True(t, prSvc.listFilesCalled)
	assert.Contains(t, ag.lastDiff, "Source: github-files-api")
	assert.Contains(t, ag.lastDiff, "src/fallback.go")
	assert.Empty(t, prSvc.reviews)
	assert.Equal(t, 0, createdIssues)
	assert.Empty(t, wf.dispatched)
	assert.False(t, prSvc.merged)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "sha-old")
	assert.Contains(t, prSvc.comments[0], "sha-new")
	assert.Contains(t, strings.ToLower(prSvc.comments[0]), "discarded")
	assert.Contains(t, strings.ToLower(prSvc.comments[0]), "changed")
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_DiscardsFindingsWhenHeadAdvances(t *testing.T) {
	issueSvc := newMockIssueService()
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return issueSvc.Create(context.Background(), title, body, labels, milestone)
		},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-old"},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing validation"},
				{Severity: "MEDIUM", Description: "Missing test"},
			},
		},
		onReview: func() {
			repoSvc.branchSHAs["herd/batch/1-batch"] = "sha-new"
		},
	}
	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = repoSvc

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.Equal(t, 1, ag.calls)
	assert.Equal(t, 0, createdIssues)
	assert.Empty(t, wf.dispatched)
	assert.Empty(t, prSvc.reviews)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "sha-old")
	assert.Contains(t, prSvc.comments[0], "sha-new")
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_AutoMergeDoesNotRunWhenHeadAdvances(t *testing.T) {
	issueSvc := newMockIssueService()
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-old"},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		onReview: func() {
			repoSvc.branchSHAs["herd/batch/1-batch"] = "sha-new"
		},
	}
	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator:   config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.False(t, prSvc.merged)
	assert.NotContains(t, repoSvc.deletedBranches, "herd/batch/1-batch")
	assert.Empty(t, prSvc.reviews)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "sha-old")
	assert.Contains(t, prSvc.comments[0], "sha-new")
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_PreparesFallbackDiffWhenRawDiffTooLarge(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffErr: platform.ErrPullRequestDiffTooLarge,
			listFilesResult: []*platform.PullRequestFile{
				{
					Path:      "src/fallback.go",
					Status:    "modified",
					Additions: 1,
					Deletions: 1,
					Changes:   2,
					Patch:     "@@ -1 +1 @@\n-old\n+fallback\n",
				},
			},
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-reviewed"},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), mock, ag, nil, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.True(t, prSvc.getDiffCalled)
	assert.True(t, prSvc.listFilesCalled)
	assert.Contains(t, ag.lastDiff, "Source: github-files-api")
	assert.Contains(t, ag.lastDiff, "src/fallback.go")
	assert.Contains(t, ag.lastDiff, "+fallback")
}

func TestReview_PrefersLocalGitDiffOverRawGitHubDiff(t *testing.T) {
	dir, g := initTestRepo(t)
	require.NoError(t, g.Checkout("herd/batch/1-batch"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local-review.txt"), []byte("local review\n"), 0644))
	runReviewTestGit(t, dir, "add", "local-review.txt")
	runReviewTestGit(t, dir, "commit", "-m", "local review change")
	headSHA := reviewTestGitOutput(t, dir, "rev-parse", "HEAD")

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffErr: platform.ErrPullRequestDiffTooLarge,
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": headSHA},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.False(t, prSvc.getDiffCalled)
	assert.False(t, prSvc.listFilesCalled)
	assert.Contains(t, ag.lastDiff, "Source: local-git")
	assert.Contains(t, ag.lastDiff, "local-review.txt")
	assert.Contains(t, ag.lastDiff, "+local review")
}

func TestReview_CoverageBlockedWithActionableFindingsPostsCoverageAndCreatesFixIssue(t *testing.T) {
	dir, g := initTestRepo(t)
	require.NoError(t, g.Checkout("herd/batch/1-batch"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src\n\nfunc A() {}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package src\n\nfunc B() {}\n"), 0644))
	runReviewTestGit(t, dir, "add", "src/a.go", "src/b.go")
	runReviewTestGit(t, dir, "commit", "-m", "add review files")
	headSHA := reviewTestGitOutput(t, dir, "rev-parse", "HEAD")

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.createResult = &platform.Issue{Number: 100, Title: "Review fixes (cycle 1)"}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": headSHA},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = repoSvc
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Summary:  "found issue",
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Critical bug in reviewed chunk"},
			},
		},
	}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 3,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
				MaxChunks:        1,
			},
		},
		Workers: config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Equal(t, "Review fixes (cycle 1)", issueSvc.createdTitle)
	assert.Contains(t, issueSvc.createdBody, "Critical bug in reviewed chunk")
	assert.Len(t, wf.dispatched, 1)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "Critical bug in reviewed chunk")
	assert.Contains(t, prSvc.comments[0], "not all material source files were reviewed")
	assert.Contains(t, prSvc.comments[0], "src/b.go: max chunks reached")
	assert.Contains(t, prSvc.comments[0], "required 2 review chunk(s)")
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
}

func TestReview_AppendsCoverageWhenPreparedDiffIsLimited(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffErr: platform.ErrPullRequestDiffTooLarge,
			listFilesResult: []*platform.PullRequestFile{
				{Path: "dist/app.js", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
				{Path: "image.png", Status: "added"},
				{Path: "big.go", Status: "modified", Patch: strings.Repeat("+x\n", 20000)},
			},
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-reviewed"},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), mock, ag, nil, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Approved)
	require.NotEmpty(t, prSvc.comments)
	assert.Contains(t, prSvc.comments[0], "## Diff Coverage")
	assert.Contains(t, prSvc.comments[0], "Files summarized but not reviewed: 2")
	assert.Contains(t, prSvc.comments[0], "Files reviewed with truncated diffs: 1")
	assert.Contains(t, prSvc.comments[0], "generated file: 1")
	assert.Contains(t, prSvc.comments[0], "dist/app.js")
	assert.Contains(t, prSvc.comments[0], "binary file: 1")
	assert.Contains(t, prSvc.comments[0], "image.png")
	assert.Contains(t, prSvc.comments[0], "big.go: file diff exceeds per-file limit and was truncated")
}

func TestReview_ZeroChunksWithOnlyAllowableOmissionsRequestsChanges(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffErr: platform.ErrPullRequestDiffTooLarge,
			listFilesResult: []*platform.PullRequestFile{
				{Path: "dist/app.js", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
				{Path: "image.png", Status: "added"},
			},
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "sha-reviewed"},
	}
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = repoSvc
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), mock, ag, nil, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, 0, ag.calls)
	assert.Equal(t, 0, createdIssues)
	assert.Empty(t, wf.dispatched)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "No review chunks were sent to the review agent")
	assert.Contains(t, prSvc.comments[0], "Files summarized but not reviewed")
	assert.Contains(t, prSvc.comments[0], "dist/app.js: generated file")
	assert.Contains(t, prSvc.comments[0], "image.png: binary file")
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
}

func TestReview_ReleasesReviewLockAfterApprovedReview(t *testing.T) {
	issueSvc := newMockIssueService()
	repoSvc := &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}}
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(issueSvc)
	mock.repo = repoSvc
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	assert.Equal(t, 0, reviewLockCommentCount(issueSvc.storedComments[50], 50))
	lockBranch := reviewLockBranch(50)
	assert.True(t, repoSvc.branchExists[lockBranch])
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "unlocked", state.Status)
}

func TestReview_ReleasesLockWithFreshContextWhenParentContextCancelled(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.respectCanceledContext = true
	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
		respectCanceledContext: true,
	}
	repoSvc := &mockRepoService{
		defaultBranch:          "main",
		branchExists:           map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:             map[string]string{"herd/batch/1-batch": "sha-current"},
		respectCanceledContext: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		onReview:     cancel,
	}

	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc
	result, err := Review(ctx, mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	require.Len(t, prSvc.comments[50], 1)
	assert.Contains(t, prSvc.comments[50][0], "LGTM")
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, prSvc.reviews[0].event)
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_CreatesFixIssueWithFreshContextWhenParentContextCancelled(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.respectCanceledContext = true
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
		respectCanceledContext: true,
	}
	wf := &mockWorkflowService{respectCanceledContext: true}
	repoSvc := &mockRepoService{
		defaultBranch:          "main",
		branchExists:           map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:             map[string]string{"herd/batch/1-batch": "sha-current"},
		respectCanceledContext: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing validation"},
			},
		},
		onReview: cancel,
	}

	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.repo = repoSvc
	mock.workflows = wf

	result, err := Review(ctx, mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Equal(t, 1, ag.calls)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
	require.Len(t, prSvc.comments[50], 1)
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assertReviewLockUnlocked(t, repoSvc)
}

func TestReview_ReleasesReviewLockAfterCreatingFixIssue(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{
		Approved: false,
		Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "Missing validation"}},
	}}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), newReviewLockTestPlatform(mockCreate), ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, 1, ag.calls)
	assert.Equal(t, 1, createdIssues)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Equal(t, 0, reviewLockCommentCount(issueSvc.storedComments[50], 50))
}

func assertReviewLockUnlocked(t *testing.T, repoSvc *mockRepoService) {
	t.Helper()
	lockBranch := reviewLockBranch(50)
	require.True(t, repoSvc.branchExists[lockBranch])
	state, ok := parseReviewLockCommitMessage(repoSvc.commitMessages[repoSvc.branchSHAs[lockBranch]])
	require.True(t, ok)
	assert.Equal(t, "unlocked", state.Status)
}

type reviewIdempotencyFixture struct {
	mock          *mockPlatform
	issueSvc      *mockIssueService
	prSvc         *mockCapturingPRService
	wf            *mockWorkflowService
	repoSvc       *mockRepoService
	createdIssues int
	dir           string
	g             *git.Git
}

func newReviewIdempotencyFixture(t *testing.T, headSHA string) *reviewIdempotencyFixture {
	t.Helper()
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}

	fx := &reviewIdempotencyFixture{issueSvc: issueSvc}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			fx.createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	fx.prSvc = &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
	fx.wf = &mockWorkflowService{}
	fx.repoSvc = &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": headSHA},
	}
	fx.mock = newReviewLockTestPlatform(mockCreate)
	fx.mock.prs = fx.prSvc
	fx.mock.workflows = fx.wf
	fx.mock.repo = fx.repoSvc
	fx.dir, fx.g = initTestRepo(t)
	return fx
}

func (fx *reviewIdempotencyFixture) addReviewResultMarkerComment(t *testing.T, prNumber, batchNumber int, headSHA, status string) {
	fx.addReviewResultMarkerCommentFrom(t, prNumber, batchNumber, headSHA, status, "github-actions[bot]", "NONE")
}

func (fx *reviewIdempotencyFixture) addReviewResultMarkerCommentFrom(t *testing.T, prNumber, batchNumber int, headSHA, status, authorLogin, authorAssociation string) {
	t.Helper()
	body, err := appendReviewResultMarker("✅ **HerdOS Agent Review**\n\nPrior result", newReviewResultMarker(prNumber, batchNumber, headSHA, status, 1, 0, time.Now()))
	require.NoError(t, err)
	fx.addCommentFrom(prNumber, body, authorLogin, authorAssociation)
}

func (fx *reviewIdempotencyFixture) addCommentFrom(prNumber int, body, authorLogin, authorAssociation string) {
	id := fx.issueSvc.nextCommentID
	fx.issueSvc.nextCommentID++
	fx.issueSvc.comments[prNumber] = append(fx.issueSvc.comments[prNumber], body)
	fx.issueSvc.storedComments[prNumber] = append(fx.issueSvc.storedComments[prNumber], &platform.Comment{
		ID:                id,
		Body:              body,
		AuthorLogin:       authorLogin,
		AuthorAssociation: authorAssociation,
	})
}

func TestReview_AutomaticSkipsApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-current", reviewResultStatusApproved)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SkippedDuplicateApprovedHead)
	assert.Contains(t, result.SkipReason, "PR #50")
	assert.Contains(t, result.SkipReason, "sha-current")
	assert.Equal(t, "sha-current", result.HeadSHA)
	assert.Equal(t, 0, ag.calls)
	assert.Empty(t, fx.prSvc.comments)
	assert.Empty(t, fx.prSvc.reviews)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_AutomaticIgnoresHumanApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerCommentFrom(t, 50, 1, "sha-current", reviewResultStatusApproved, "alice", "MEMBER")
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, result.Approved)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	require.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, fx.prSvc.reviews[0].event)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_AutomaticSkipsAuthenticatedHumanApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.mock.authenticatedLogin = "jfturcot"
	fx.addReviewResultMarkerCommentFrom(t, 50, 1, "sha-current", reviewResultStatusApproved, "jfturcot", "MEMBER")
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SkippedDuplicateApprovedHead)
	assert.Equal(t, 0, ag.calls)
	assert.Empty(t, fx.prSvc.comments)
	assert.Empty(t, fx.prSvc.reviews)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_AutomaticIgnoresDifferentHumanApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.mock.authenticatedLogin = "jfturcot"
	fx.addReviewResultMarkerCommentFrom(t, 50, 1, "sha-current", reviewResultStatusApproved, "alice", "MEMBER")
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, result.Approved)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	require.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, fx.prSvc.reviews[0].event)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_AutomaticSkipsTrustedBotApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerCommentFrom(t, 50, 1, "sha-current", reviewResultStatusApproved, "herd-os[bot]", "NONE")
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SkippedDuplicateApprovedHead)
	assert.Equal(t, 0, ag.calls)
	assert.Empty(t, fx.prSvc.comments)
	assert.Empty(t, fx.prSvc.reviews)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_AutomaticSkipDoesNotPostAnotherApprovalComment(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-current", reviewResultStatusApproved)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SkippedDuplicateApprovedHead)
	assert.Equal(t, 0, ag.calls)
	assert.Empty(t, fx.prSvc.comments)
	assert.Empty(t, fx.prSvc.reviews)
}

func TestReview_AutomaticCommentListingFailureFailsClosed(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-current", reviewResultStatusApproved)
	fx.issueSvc.listCommentsErr = fmt.Errorf("comments unavailable")
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "listing PR comments for review idempotency")
	assert.Equal(t, 0, ag.calls)
	assert.Empty(t, fx.prSvc.comments)
	assert.Empty(t, fx.prSvc.reviews)
	assert.Equal(t, 0, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	assertReviewLockUnlocked(t, fx.repoSvc)
}

func TestReview_ManualRunsDespiteApprovedMarkerForCurrentHead(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-current", reviewResultStatusApproved)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, result.Approved)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	require.Len(t, fx.prSvc.comments, 1)
	assert.Contains(t, fx.prSvc.comments[0], reviewResultMarkerPrefix)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, fx.prSvc.reviews[0].event)
}

func TestReview_HeadChangeInvalidatesApprovedMarker(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-new")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-old", reviewResultStatusApproved)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, result.Approved)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	require.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
}

func TestReview_ChangesRequestedMarkerDoesNotSuppressAutomaticReview(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-current", reviewResultStatusChangesRequested)
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "Missing validation"}},
		},
	}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	assert.Equal(t, 1, fx.createdIssues)
	require.Len(t, fx.wf.dispatched, 1)
	require.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, fx.prSvc.reviews[0].event)
}

func TestReview_SecondSerializedInvocationSkipsAfterFirstApproval(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	cfg := &config.Config{Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3}}
	firstAg := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	firstResult, err := Review(context.Background(), fx.mock, firstAg, fx.g, cfg, ReviewParams{PRNumber: 50, RepoRoot: fx.dir})
	require.NoError(t, err)
	require.True(t, firstResult.Approved)
	require.Len(t, fx.prSvc.comments, 1)
	fx.addCommentFrom(50, fx.prSvc.comments[0], "github-actions[bot]", "NONE")

	secondAg := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "second should not run"}}
	secondResult, err := Review(context.Background(), fx.mock, secondAg, fx.g, cfg, ReviewParams{PRNumber: 50, RepoRoot: fx.dir})

	require.NoError(t, err)
	require.NotNil(t, secondResult)
	assert.Equal(t, 1, firstAg.calls)
	assert.Equal(t, 0, secondAg.calls)
	assert.True(t, secondResult.SkippedDuplicateApprovedHead)
	assert.Equal(t, "sha-current", secondResult.HeadSHA)
	assert.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, fx.prSvc.reviews[0].event)
}

func TestReview_MalformedReviewResultMarkersDoNotBlock(t *testing.T) {
	fx := newReviewIdempotencyFixture(t, "sha-current")
	fx.addCommentFrom(50, reviewResultMarkerPrefix+`{"version":`+reviewResultMarkerSuffix, "github-actions[bot]", "NONE")
	fx.addReviewResultMarkerComment(t, 51, 1, "sha-current", reviewResultStatusApproved)
	fx.addReviewResultMarkerComment(t, 50, 2, "sha-current", reviewResultStatusApproved)
	fx.addReviewResultMarkerComment(t, 50, 1, "sha-other", reviewResultStatusApproved)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	result, err := Review(context.Background(), fx.mock, ag, fx.g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: fx.dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls)
	assert.True(t, result.Approved)
	assert.False(t, result.SkippedDuplicateApprovedHead)
	require.Len(t, fx.prSvc.comments, 1)
	require.Len(t, fx.prSvc.reviews, 1)
}

func TestReview_StaleReviewLockIsReplacedAndReviewRuns(t *testing.T) {
	issueSvc := newMockIssueService()
	now := time.Now().UTC()
	lockBranch := reviewLockBranch(50)
	acquiredAt := now.Add(-3 * time.Hour)
	expiresAt := now.Add(-time.Hour)
	staleState := reviewLockState{
		Kind:        "herd-review-lock",
		Version:     1,
		Status:      "locked",
		LockID:      "stale-lock",
		PRNumber:    50,
		BatchNumber: 1,
		RunID:       99,
		Owner:       "stale",
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiresAt,
	}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true, lockBranch: true},
		branchSHAs:    map[string]string{lockBranch: "stale-sha"},
		commitMessages: map[string]string{
			"stale-sha": mustReviewLockCommitMessage(t, staleState),
		},
	}
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	dir, g := initTestRepo(t)
	mock := newReviewLockTestPlatform(issueSvc)
	mock.repo = repoSvc
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	assert.Equal(t, 0, reviewLockCommentCount(issueSvc.storedComments[50], 50))
}

func TestReview_MalformedAndUnrelatedReviewLocksDoNotBlock(t *testing.T) {
	issueSvc := newMockIssueService()
	now := time.Now().UTC()
	_, err := issueSvc.AddCommentReturningID(context.Background(), 50, reviewLockMarkerPrefix+`{"pr_number":`+reviewLockMarkerSuffix)
	require.NoError(t, err)
	acquiredAt := now
	expiresAt := now.Add(reviewLockExpiry)
	_, err = issueSvc.AddCommentReturningID(context.Background(), 50, mustReviewLockComment(t, reviewLockState{
		PRNumber:    999,
		BatchNumber: 1,
		Owner:       "other-pr",
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiresAt,
	}))
	require.NoError(t, err)
	ag := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), newReviewLockTestPlatform(issueSvc), ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 1, ag.calls)
	assert.Equal(t, 0, reviewLockCommentCount(issueSvc.storedComments[50], 50))
	assert.Equal(t, 1, reviewLockCommentCount(issueSvc.storedComments[50], 999))
}

func TestReview_SecondTriggerSkipsWhileFirstHoldsReviewLock(t *testing.T) {
	issueSvc := newMockIssueService()
	mock := newReviewLockTestPlatform(issueSvc)
	dir, g := initTestRepo(t)
	cfg := &config.Config{Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3}}

	secondAg := &mockReviewAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "second"}}
	secondResult := (*ReviewResult)(nil)
	secondErr := error(nil)
	calledSecond := false
	firstAg := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "first"},
		onReview: func() {
			if calledSecond {
				return
			}
			calledSecond = true
			secondResult, secondErr = Review(context.Background(), mock, secondAg, g, cfg, ReviewParams{PRNumber: 50, RepoRoot: dir})
		},
	}

	firstResult, err := Review(context.Background(), mock, firstAg, g, cfg, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NoError(t, secondErr)
	require.NotNil(t, secondResult)
	assert.True(t, firstResult.Approved)
	assert.Equal(t, 50, secondResult.BatchPRNumber)
	assert.Equal(t, 1, firstAg.calls)
	assert.Equal(t, 0, secondAg.calls)
	assert.Equal(t, 0, reviewLockCommentCount(issueSvc.storedComments[50], 50))
}

func TestReview_NoBatchPR(t *testing.T) {
	mock := newReviewTestPlatform(nil, nil)

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 0, result.BatchPRNumber)
}

func TestReview_Disabled(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		nil,
	)

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: false},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_Approved(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n\n## Acceptance Criteria\n\n- [ ] Works\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_ChangesRequested_CreatesFixes(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	// Track created issues
	createdIssues := []*platform.Issue{}
	nextNum := 100

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	// Override Create on the issue service to track creations
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: nextNum, Title: title}
			nextNum++
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Tests not covering edge case"},
			},
			Comments: []string{"Missing error handling in auth.go", "Tests not covering edge case"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 1, result.FixCycle)
	assert.Len(t, result.FixIssues, 1)
	assert.Len(t, createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
	assert.Equal(t, "Review fixes (cycle 1)", createdIssues[0].Title)
}

func TestReview_ReturnsErrorWhenRefetchedPRIsNil(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &nilRefreshPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"}},
		},
	}
	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "platform returned nil PR for #50")
}

func TestRefreshedPRWithOriginalIdentity(t *testing.T) {
	tests := []struct {
		name      string
		original  *platform.PullRequest
		refreshed *platform.PullRequest
		want      *platform.PullRequest
		wantErr   string
	}{
		{
			name:      "nil refreshed PR returns clear error",
			original:  &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main"},
			refreshed: nil,
			wantErr:   "platform returned nil PR for #50",
		},
		{
			name:      "partial refreshed PR preserves original identity fields",
			original:  &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main"},
			refreshed: &platform.PullRequest{MergeableKnown: true, Mergeable: true, MergeStateStatus: "CLEAN"},
			want:      &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main", MergeableKnown: true, Mergeable: true, MergeStateStatus: "CLEAN"},
		},
		{
			name:      "full refreshed PR keeps live identity",
			original:  &platform.PullRequest{Number: 50, Head: "old-head", Base: "old-base"},
			refreshed: &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main", MergeStateStatus: "BLOCKED"},
			want:      &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main", MergeStateStatus: "BLOCKED"},
		},
		{
			name:      "mismatched PR number is rejected",
			original:  &platform.PullRequest{Number: 50, Head: "herd/batch/1-batch", Base: "main"},
			refreshed: &platform.PullRequest{Number: 51, Head: "herd/batch/1-batch", Base: "main"},
			wantErr:   "platform returned PR #51 while refreshing PR #50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := refreshedPRWithOriginalIdentity(tt.original, tt.refreshed)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReview_CleanPRFiltersStaleCascadeFindingAndRemovesLabel(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		finding: agent.ReviewFinding{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
	})

	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
	comment := requireCommentContaining(t, prSvc.comments, "Stale PR-state findings ignored")
	assert.Contains(t, comment, "did not dispatch a fix worker")
	assert.Contains(t, comment, "## Review Aggregation")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.Contains(t, comment, "- Stale cascade label cleanup: removed")
	assert.NotContains(t, comment, "- Raw findings before dedupe: 1")
	assert.NotContains(t, comment, "- Findings after dedupe: 1")
	assert.NotContains(t, strings.Join(prSvc.comments, "\n"), "unresolved merge conflict on this PR")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_CleanPRFiltersUnanchoredPRLevelMergeConflictFinding(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
		},
		finding: agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
	})

	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	comment := requireCommentContaining(t, prSvc.comments, "Stale PR-state findings ignored")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.NotContains(t, strings.Join(prSvc.comments, "\n"), "Unresolved merge conflict blocks merge")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_CleanStatusKnownUnmergeablePreservesCascadeFindingAndLabel(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        false,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		finding: agent.ReviewFinding{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
	})

	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Contains(t, issueSvc.createdBody, "unresolved merge conflict on this PR")
	assert.Empty(t, issueSvc.removedLabels[50])
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
	assert.Contains(t, requireCommentContaining(t, prSvc.comments, "unresolved merge conflict on this PR"), "fix worker dispatched")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_RefetchedBlockedPRPreservesCascadeFindingAndLabel(t *testing.T) {
	var prompt string
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		livePR: &platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "BLOCKED",
			Labels:           []string{issues.CascadeFailed},
		},
		finding:       agent.ReviewFinding{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
		capturePrompt: &prompt,
	})

	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Contains(t, issueSvc.createdBody, "unresolved merge conflict on this PR")
	assert.Empty(t, issueSvc.removedLabels[50])
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
	assert.Contains(t, requireCommentContaining(t, prSvc.comments, "unresolved merge conflict on this PR"), "fix worker dispatched")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
	assert.NotContains(t, strings.Join(prSvc.comments, "\n"), "Stale PR-state findings ignored")
	assert.Contains(t, prompt, "Merge state status: CLEAN")
	assert.NotContains(t, prompt, "Merge state status: BLOCKED")
	assertPromptDescribesInvocationTimePRMetadata(t, prompt)
}

func TestReview_RefetchedCleanPRFiltersCascadeFindingAndRemovesLabel(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "BLOCKED",
			Labels:           []string{issues.CascadeFailed},
		},
		livePR: &platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		finding: agent.ReviewFinding{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
	})

	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
	comment := requireCommentContaining(t, prSvc.comments, "Stale PR-state findings ignored")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.Contains(t, comment, "- Stale cascade label cleanup: removed")
	assert.NotContains(t, strings.Join(prSvc.comments, "\n"), "unresolved merge conflict on this PR")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_CleanPRPreservesFileLevelMergeConflictFinding(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
		},
		finding: agent.ReviewFinding{Severity: "HIGH", Description: "internal/merge/resolver.go: resolve merge conflicts ignores errors"},
	})

	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Contains(t, issueSvc.createdBody, "internal/merge/resolver.go: resolve merge conflicts ignores errors")
	assert.Empty(t, issueSvc.removedLabels[50])
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
	assert.Contains(t, requireCommentContaining(t, prSvc.comments, "internal/merge/resolver.go"), "fix worker dispatched")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_CleanPRPreservesNonAllowlistedFileLevelMergeConflictFindings(t *testing.T) {
	tests := []struct {
		name    string
		finding string
	}{
		{name: "root go mod", finding: "go.mod: merge conflict marker dependency resolution is wrong"},
		{name: "root package json", finding: "package.json: resolve merge conflicts script ignores errors"},
		{name: "root settings go", finding: "settings.go: unresolved conflict handler drops errors"},
		{name: "typescript", finding: "web/src/mergeResolver.ts: resolve merge conflicts ignores errors"},
		{name: "python", finding: "scripts/merge_resolver.py: unresolved conflict handler drops errors"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
				pr: platform.PullRequest{
					MergeableKnown:   true,
					Mergeable:        true,
					MergeStateStatus: "CLEAN",
				},
				finding: agent.ReviewFinding{Severity: "HIGH", Description: tt.finding},
			})

			require.NotNil(t, result)
			assert.False(t, result.Approved)
			assert.Equal(t, []int{100}, result.FixIssues)
			assert.Contains(t, issueSvc.createdBody, tt.finding)
			assert.Empty(t, issueSvc.removedLabels[50])
			require.Len(t, wf.dispatched, 1)
			assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
			assert.Contains(t, requireCommentContaining(t, prSvc.comments, tt.finding), "fix worker dispatched")
			assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
		})
	}
}

func TestReview_CleanPRPreservesFileLevelCascadeLabelFinding(t *testing.T) {
	tests := []struct {
		name    string
		finding string
		comment string
	}{
		{
			name:    "nested docs file",
			finding: "docs/design/execution.md: herd/cascade-failed docs still imply the label can only be removed manually",
			comment: "docs/design/execution.md",
		},
		{
			name:    "root package json file",
			finding: "package.json: herd/cascade-failed cleanup script ignores errors",
			comment: "package.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
				pr: platform.PullRequest{
					MergeableKnown:   true,
					Mergeable:        true,
					MergeStateStatus: "CLEAN",
				},
				finding: agent.ReviewFinding{Severity: "MEDIUM", Description: tt.finding},
			})

			require.NotNil(t, result)
			assert.False(t, result.Approved)
			assert.Equal(t, []int{100}, result.FixIssues)
			assert.Contains(t, issueSvc.createdBody, tt.finding)
			assert.Empty(t, issueSvc.removedLabels[50])
			require.Len(t, wf.dispatched, 1)
			assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
			assert.Contains(t, requireCommentContaining(t, prSvc.comments, tt.comment), "fix worker dispatched")
			assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
		})
	}
}

func TestReview_CleanPRFiltersStaleCascadeFindingWithDedupeMetadata(t *testing.T) {
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
			{Severity: "HIGH", Description: "Previous review says the herd cascade failed due to merge conflicts in chunk 2/3."},
		},
	})

	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	comment := requireCommentContaining(t, prSvc.comments, "Stale PR-state findings ignored")
	assert.Contains(t, comment, "## Review Aggregation")
	assert.Contains(t, comment, "- Raw findings before dedupe: 2")
	assert.Contains(t, comment, "- Findings after dedupe: 1")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.Contains(t, comment, "- Stale cascade label cleanup: removed")
}

func TestReview_CleanPRFiltersStaleCascadeFindingWhenLabelCleanupFails(t *testing.T) {
	cleanupErr := errors.New("github label API failed")
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Conflict-resolution cascade state says a branch conflict remains"},
		removeLabelsErr: cleanupErr,
	})

	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
	comment := requireCommentContaining(t, prSvc.comments, "Stale cascade label cleanup")
	assert.Contains(t, comment, cleanupErr.Error())
	assert.Contains(t, comment, "## Review Aggregation")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.Contains(t, comment, "- Stale cascade label cleanup: failed (github label API failed)")
	assert.NotContains(t, comment, "Stale finding was still ignored")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestReview_CleanupFailureWithNonActionableFindingPostsSingleMarker(t *testing.T) {
	cleanupErr := errors.New("github label API failed")
	result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
		pr: platform.PullRequest{
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "CLEAN",
			Labels:           []string{issues.CascadeFailed},
		},
		findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR"},
			{Severity: "LOW", Description: "docs/review.md: minor wording nit"},
		},
		removeLabelsErr: cleanupErr,
	})

	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
	require.Len(t, prSvc.comments, 1)
	assert.Equal(t, 1, strings.Count(strings.Join(prSvc.comments, "\n"), reviewResultMarkerPrefix))
	comment := prSvc.comments[0]
	assert.Contains(t, comment, "docs/review.md: minor wording nit")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.Contains(t, comment, "- Stale cascade label cleanup: failed (github label API failed)")
	marker, ok := parseReviewResultMarker(comment)
	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, marker.Status)
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
}

func TestReview_HistoricalCommentsDoNotOverrideCleanLivePRMetadata(t *testing.T) {
	dir, g, headSHA := initChunkedReviewRepo(t, 1)
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.createResult = &platform.Issue{Number: 100, Title: "Review fixes (cycle 1)"}
	issueSvc.storedComments[50] = []*platform.Comment{{
		ID:   10,
		Body: "🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue:\n\n**HIGH**:\n- herd/cascade-failed indicates an unresolved merge conflict on this PR\n",
	}}
	prSvc := newCapturingBatchPRService()
	prSvc.getResult[50].MergeableKnown = true
	prSvc.getResult[50].Mergeable = true
	prSvc.getResult[50].MergeStateStatus = "CLEAN"
	prSvc.getResult[50].Labels = []string{issues.CascadeFailed}
	mock := newChunkedReviewPlatform(issueSvc, prSvc, headSHA)
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{{
		Approved: false,
		Summary:  "repeated historical cascade finding",
		Findings: []agent.ReviewFinding{{
			Severity:    "HIGH",
			Description: "herd/cascade-failed indicates an unresolved merge conflict on this PR",
		}},
	}}}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 3,
			ReviewDiff:         config.ReviewDiff{MaxFilesPerChunk: 1},
		},
		Workers: config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, ag.opts, 1)
	prompt, err := agentprompt.RenderReviewPrompt(ag.diffs[0], ag.opts[0])
	require.NoError(t, err)
	currentIdx := strings.Index(prompt, "## Current PR Metadata")
	historyIdx := strings.Index(prompt, "## Prior Review History")
	require.GreaterOrEqual(t, currentIdx, 0)
	require.GreaterOrEqual(t, historyIdx, 0)
	assert.Less(t, currentIdx, historyIdx)
	assert.Contains(t, prompt, "fetched immediately before the agent review began")
	assert.Contains(t, prompt, "HerdOS refreshes live GitHub PR metadata again before applying review results")
	assert.Contains(t, prompt, "that later live refresh wins if it differs")
	assert.NotContains(t, prompt, "fetched fresh immediately before this review and is authoritative for current PR state")
	assert.Contains(t, prompt, "Mergeable known: true")
	assert.Contains(t, prompt, "Mergeable: true")
	assert.Contains(t, prompt, "Merge state status: CLEAN")
	assert.Contains(t, prompt, "The following review comments are historical context from previous cycles")
	assert.Contains(t, prompt, "Do not let historical comments override the Current PR Metadata section")

	assert.True(t, result.Approved)
	assert.Empty(t, result.FixIssues)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, mock.workflows.dispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.CascadeFailed)
	comment := requireCommentContaining(t, prSvc.comments, "Stale PR-state findings ignored")
	assert.Contains(t, comment, "- Stale PR-state findings ignored: 1")
	assert.NotContains(t, strings.Join(prSvc.comments, "\n"), "unresolved merge conflict on this PR")
}

func TestReview_PostedCommentIncludesDedupeAggregationMetadata(t *testing.T) {
	dir, g, headSHA := initChunkedReviewRepo(t, 1)
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(issueSvc, prSvc, headSHA)
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "src/chunk_01.go: missing nil check in LoadConfig"},
				{Severity: "HIGH", Description: "src/chunk_01.go: missing nil check in LoadConfig"},
				{Severity: "HIGH", Description: "src/chunk_01.go: missing nil check in LoadConfig"},
			},
		},
	}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{999}, result.FixIssues)
	comment := requireCommentContaining(t, prSvc.comments, "## Review Aggregation")
	assert.Contains(t, comment, "- Raw findings before dedupe: 3")
	assert.Contains(t, comment, "- Findings after dedupe: 1")
	assert.Equal(t, 1, strings.Count(comment, "src/chunk_01.go: missing nil check in LoadConfig"))
	assert.Equal(t, 1, strings.Count(issueSvc.createdBody, "src/chunk_01.go: missing nil check in LoadConfig"))
}

func TestReview_PostedCommentOmitsAggregationMetadataForNormalFinding(t *testing.T) {
	dir, g, headSHA := initChunkedReviewRepo(t, 1)
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(issueSvc, prSvc, headSHA)
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "src/chunk_01.go: missing nil check in LoadConfig"},
			},
		},
	}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{999}, result.FixIssues)
	comment := requireCommentContaining(t, prSvc.comments, "src/chunk_01.go: missing nil check")
	assert.NotContains(t, comment, "## Review Aggregation")
}

func TestReview_NonCleanPRPreservesCascadeFinding(t *testing.T) {
	tests := []struct {
		name string
		pr   platform.PullRequest
	}{
		{
			name: "blocked",
			pr: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BLOCKED",
				Labels:           []string{issues.CascadeFailed},
			},
		},
		{
			name: "behind",
			pr: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "BEHIND",
				Labels:           []string{issues.CascadeFailed},
			},
		},
		{
			name: "unstable",
			pr: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "UNSTABLE",
				Labels:           []string{issues.CascadeFailed},
			},
		},
		{
			name: "has hooks",
			pr: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "HAS_HOOKS",
				Labels:           []string{issues.CascadeFailed},
			},
		},
		{
			name: "unknown",
			pr: platform.PullRequest{
				MergeStateStatus: "UNKNOWN",
				Labels:           []string{issues.CascadeFailed},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, issueSvc, prSvc, wf := runStaleCascadeReview(t, staleCascadeReviewCase{
				pr:      tt.pr,
				finding: agent.ReviewFinding{Severity: "HIGH", Description: "unresolved merge conflict between batch branch and base branch"},
			})

			require.NotNil(t, result)
			assert.False(t, result.Approved)
			assert.Equal(t, []int{100}, result.FixIssues)
			assert.Contains(t, issueSvc.createdBody, "unresolved merge conflict")
			assert.Empty(t, issueSvc.removedLabels[50])
			require.Len(t, wf.dispatched, 1)
			assert.Equal(t, "100", wf.dispatched[0]["issue_number"])
			assert.Contains(t, requireCommentContaining(t, prSvc.comments, "unresolved merge conflict"), "fix worker dispatched")
			assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
		})
	}
}

type staleCascadeReviewCase struct {
	pr              platform.PullRequest
	livePR          *platform.PullRequest
	finding         agent.ReviewFinding
	findings        []agent.ReviewFinding
	removeLabelsErr error
	capturePrompt   *string
}

func runStaleCascadeReview(t *testing.T, tc staleCascadeReviewCase) (*ReviewResult, *mockIssueService, *mockCapturingPRService, *mockWorkflowService) {
	t.Helper()

	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.createResult = &platform.Issue{Number: 100, Title: "Review fixes (cycle 1)"}
	issueSvc.removeLabelsErr = tc.removeLabelsErr

	pr := tc.pr
	pr.Number = 50
	pr.Title = "[herd] Batch"
	pr.Head = "herd/batch/1-batch"
	pr.Base = "main"
	livePR := &pr
	if tc.livePR != nil {
		live := *tc.livePR
		live.Number = pr.Number
		live.Title = pr.Title
		live.Head = pr.Head
		live.Base = pr.Base
		livePR = &live
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{&pr},
			getResult:  map[int]*platform.PullRequest{50: livePR},
		},
	}
	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		},
	}
	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}
	findings := tc.findings
	if findings == nil {
		findings = []agent.ReviewFinding{tc.finding}
	}
	comments := make([]string, 0, len(findings))
	for _, finding := range findings {
		comments = append(comments, finding.Description)
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Summary:  "found stale cascade state",
			Findings: findings,
			Comments: comments,
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	if tc.capturePrompt != nil {
		prompt, renderErr := agentprompt.RenderReviewPrompt(ag.lastDiff, ag.lastOpts)
		require.NoError(t, renderErr)
		*tc.capturePrompt = prompt
	}
	return result, issueSvc, prSvc, wf
}

func TestReview_LowSeverityIncludedWhenConfigured(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := []*platform.Issue{}
	nextNum := 100

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: nextNum, Title: title, Body: body}
			nextNum++
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "LOW", Description: "Minor style issue in utils.go"},
			},
			Comments: []string{"Minor style issue"},
		},
	}

	dir, g := initTestRepo(t)

	// With default config (medium), LOW findings should NOT create fix issues
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3, ReviewFixSeverity: "medium"},
	}, ReviewParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Len(t, createdIssues, 0)

	// With review_fix_severity: low, LOW findings SHOULD create fix issues
	createdIssues = nil
	result, err = Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3, ReviewFixSeverity: "low"},
	}, ReviewParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
}

func TestReview_SkipsWhenFixWorkersInProgress(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A fix issue is still in-progress from a previous review cycle
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "issue found"}},
			Comments: []string{"issue found"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// Should skip — no review ran, no approval, no fixes created
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_SkipsWhenFixWorkersReady(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A fix issue is ready (not yet dispatched)
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "issue found"}},
			Comments: []string{"issue found"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
}

func TestReview_RunsWhenAllFixWorkersDone(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// All fix issues are done — review should proceed
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		issueSvc.listResult,
	)
	// Override the issue service with our custom one
	mock.issues = issueSvc

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
}

func TestReview_SkipsWhenCIFixInProgress(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A CI fix issue is in-progress
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix CI", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  ci_fix_cycle: 1\n---\n\n## Task\nFix CI\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
}

func TestReview_MaxCyclesHit(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
			// Existing fix issue at cycle 3
			{Number: 60, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix it\n",
				Labels: []string{issues.StatusDone}},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Still broken"},
			},
			Comments: []string{"Still broken"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.MaxCyclesHit)
}

func TestReview_SafetyValve(t *testing.T) {
	dir, g, headSHA := initChunkedReviewRepo(t, 1)
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(mockCreate, prSvc, headSHA)

	findings := makeHighReviewFindings("single-pass issue", safetyValveLimit+1)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: false, Findings: findings, Comments: reviewCommentsFromFindings(findings)},
	}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 10},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.MaxCyclesHit)
	assert.Empty(t, result.FixIssues)
	assert.Equal(t, 0, createdIssues)
	comment := requireCommentContaining(t, prSvc.comments, "safety limit")
	assert.Contains(t, comment, "single review pass")
	assert.NotContains(t, comment, "chunk")
}

func TestReview_ChunkedHighFindingsAcrossChunksDoesNotTripSafetyValve(t *testing.T) {
	results := make([]*agent.ReviewResult, 0, 7)
	for chunk := 1; chunk <= 7; chunk++ {
		findings := makeHighReviewFindings(fmt.Sprintf("src/chunk_%02d.go: chunk %d issue", chunk, chunk), 2)
		results = append(results, &agent.ReviewResult{
			Approved: false,
			Summary:  fmt.Sprintf("chunk %d found issues", chunk),
			Findings: findings,
			Comments: reviewCommentsFromFindings(findings),
		})
	}

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	result, prSvc, err := runChunkedReviewFixture(t, 7, results, mockCreate)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.MaxCyclesHit)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Equal(t, 1, createdIssues)
	assert.Contains(t, createdBody, "chunk 1 issue 01")
	assert.Contains(t, createdBody, "chunk 7 issue 02")
	for _, comment := range prSvc.comments {
		assert.NotContains(t, comment, "safety limit")
	}
	comment := requireCommentContaining(t, prSvc.comments, "Diff Coverage")
	assert.Contains(t, comment, "Chunks reviewed")
}

func TestReview_ChunkedSingleChunkOverLimitTripsSafetyValve(t *testing.T) {
	results := make([]*agent.ReviewResult, 0, 7)
	for chunk := 1; chunk <= 7; chunk++ {
		result := &agent.ReviewResult{Approved: true, Summary: "ok"}
		if chunk == 4 {
			findings := makeHighReviewFindings("offending chunk 4 issue", 12)
			result = &agent.ReviewResult{
				Approved: false,
				Summary:  "too many findings",
				Findings: findings,
				Comments: reviewCommentsFromFindings(findings),
			}
		}
		results = append(results, result)
	}

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	result, prSvc, err := runChunkedReviewFixture(t, 7, results, mockCreate)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.MaxCyclesHit)
	assert.Empty(t, result.FixIssues)
	assert.Equal(t, 0, createdIssues)
	comment := requireCommentContaining(t, prSvc.comments, "Agent review chunk 4/7 found 12 high-severity issues")
	assert.Contains(t, comment, "safety limit (10)")
	assert.Contains(t, comment, "Diff Coverage")
	assert.NotContains(t, comment, "single pass")
	assert.NotContains(t, comment, "single review pass")
	assert.Contains(t, comment, "offending chunk 4 issue 01")
	assert.Contains(t, comment, "offending chunk 4 issue 02")
	assert.Contains(t, comment, "offending chunk 4 issue 03")
}

func TestReview_ChunkedTwoChunksUnderLimitDoesNotTripSafetyValve(t *testing.T) {
	results := []*agent.ReviewResult{
		{Approved: false, Summary: "chunk 1", Findings: makeHighReviewFindings("chunk one issue", 9)},
		{Approved: false, Summary: "chunk 2", Findings: makeHighReviewFindings("chunk two issue", 9)},
	}
	for _, result := range results {
		result.Comments = reviewCommentsFromFindings(result.Findings)
	}
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	result, _, err := runChunkedReviewFixture(t, 2, results, mockCreate)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.MaxCyclesHit)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Contains(t, createdBody, "chunk one issue 01")
	assert.Contains(t, createdBody, "chunk two issue 09")
}

func TestReview_ChunkedSafetyValveUsesRawChunkCountsBeforeDedup(t *testing.T) {
	findings := make([]agent.ReviewFinding, 0, safetyValveLimit+1)
	for i := 0; i < safetyValveLimit+1; i++ {
		findings = append(findings, agent.ReviewFinding{Severity: "HIGH", Description: "Duplicate chunk finding"})
	}
	results := []*agent.ReviewResult{
		{Approved: false, Summary: "duplicates", Findings: findings, Comments: reviewCommentsFromFindings(findings)},
		{Approved: true, Summary: "ok"},
	}
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	result, prSvc, err := runChunkedReviewFixture(t, 2, results, mockCreate)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.MaxCyclesHit)
	assert.Empty(t, result.FixIssues)
	assert.Equal(t, 0, createdIssues)
	comment := requireCommentContaining(t, prSvc.comments, "Agent review chunk 1/2 found 11 high-severity issues")
	assert.Contains(t, comment, "Duplicate chunk finding")
}

func TestReview_ChunkedSafetyValveUsesSingleReviewedChunkStatsBeforeDedup(t *testing.T) {
	findings := make([]agent.ReviewFinding, 0, safetyValveLimit+1)
	for i := 0; i < safetyValveLimit+1; i++ {
		findings = append(findings, agent.ReviewFinding{Severity: "HIGH", Description: "Duplicate single chunk finding"})
	}

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}
	dir, g, headSHA := initChunkedReviewRepo(t, 2)
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(mockCreate, prSvc, headSHA)
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{{
		Approved: false,
		Summary:  "duplicates",
		Findings: findings,
		Comments: reviewCommentsFromFindings(findings),
	}}}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 10,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
				MaxChunks:        1,
			},
		},
		Workers: config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.MaxCyclesHit)
	assert.Empty(t, result.FixIssues)
	assert.Equal(t, 0, createdIssues)
	comment := requireCommentContaining(t, prSvc.comments, "Agent review chunk 1/2 found 11 high-severity issues")
	assert.Contains(t, comment, "Duplicate single chunk finding")
	assert.Contains(t, comment, "Diff Coverage")
	assert.NotContains(t, comment, "single review pass")
}

func TestReview_ChunkedDedupStillRunsAfterAggregation(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 2, Text: "diff --git a/a.go b/a.go\n", IncludedFiles: []reviewdiff.ChangedFile{{Path: "a.go"}}},
			{Index: 2, Total: 2, Text: "diff --git a/b.go b/b.go\n", IncludedFiles: []reviewdiff.ChangedFile{{Path: "b.go"}}},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:         "test",
			TotalFiles:     2,
			ReviewMode:     reviewdiff.CoverageModeChunked,
			ChunksPlanned:  2,
			ChunksReviewed: 2,
			FilesReviewed:  2,
			Complete:       true,
		},
	}
	results := []*agent.ReviewResult{
		{Approved: false, Summary: "chunk 1", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "Existing active finding"},
		}},
		{Approved: false, Summary: "chunk 2", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "Brand new chunked finding"},
		}},
	}
	for _, result := range results {
		result.Comments = reviewCommentsFromFindings(result.Findings)
	}
	baseIssue := &platform.Issue{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"}
	activeFix := &platform.Issue{
		Number: 80,
		State:  "open",
		Title:  "Review fixes (cycle 1)",
		Labels: []string{issues.StatusInProgress},
		Body:   "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\n1. Existing active finding\n",
	}
	ag := &chunkCaptureAgent{results: results}
	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	require.NotNil(t, aggregate.Result)
	require.Len(t, aggregate.Result.Findings, 2)

	actionableFindings := dedupFindings(aggregate.Result.Findings, activeFixIssues([]*platform.Issue{baseIssue, activeFix}))

	require.Len(t, actionableFindings, 1)
	assert.Equal(t, "Brand new chunked finding", actionableFindings[0].Description)
}

func TestReview_MaxCyclesHitStillWinsBeforeChunkedSafetyValve(t *testing.T) {
	results := []*agent.ReviewResult{
		{Approved: false, Summary: "too many", Findings: makeHighReviewFindings("over limit", safetyValveLimit+1)},
		{Approved: true, Summary: "ok"},
	}
	results[0].Comments = reviewCommentsFromFindings(results[0].Findings)
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
		{
			Number: 60,
			State:  "open",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nPrior fix\n",
		},
	}
	mockCreate := &mockIssueServiceWithCreate{mockIssueService: issueSvc}
	dir, g, headSHA := initChunkedReviewRepo(t, 2)
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(mockCreate, prSvc, headSHA)
	ag := &chunkCaptureAgent{results: results}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 3,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
			},
		},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.MaxCyclesHit)
	comment := requireCommentContaining(t, prSvc.comments, "max fix cycles")
	assert.NotContains(t, comment, "Agent review chunk")
	assert.NotContains(t, comment, "safety limit")
}

func TestReview_AutoMerge(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator:   config.Integrator{Review: true, Strategy: "squash", ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.True(t, prSvc.merged)
}

func TestPostMergeCleanup(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10}, {Number: 11},
	}

	msSvc := &mockMilestoneService{}
	repoSvc := &mockRepoService{}
	mock := &mockPlatform{
		issues:     issueSvc,
		milestones: msSvc,
		repo:       repoSvc,
	}

	err := postMergeCleanup(context.Background(), mock, 1, "herd/batch/1-test")
	require.NoError(t, err)

	// Should close all issues
	assert.Contains(t, issueSvc.updatedIssues, 10)
	assert.Contains(t, issueSvc.updatedIssues, 11)
	assert.Equal(t, "closed", *issueSvc.updatedIssues[10].State)
	assert.Equal(t, "closed", *issueSvc.updatedIssues[11].State)

	// Should close milestone
	assert.Contains(t, msSvc.updatedNumbers, 1)
	assert.Contains(t, msSvc.updatedStates, "closed")

	// Should delete batch branch
	assert.Equal(t, "herd/batch/1-test", repoSvc.deletedBranch)
}

func TestReview_LoadsRoleInstructions(t *testing.T) {
	dir, g := initTestRepo(t)

	// Create .herd/integrator.md
	require.NoError(t, os.MkdirAll(dir+"/.herd", 0755))
	require.NoError(t, os.WriteFile(dir+"/.herd/integrator.md", []byte("Be strict about error handling"), 0644))

	// Use a capturing agent to verify system prompt is passed
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, "Be strict about error handling", capturedOpts.SystemPrompt)
}

func TestReview_ByPRNumber(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
	}

	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch"},
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: msSvc,
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_BatchLookup(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 60, Title: "[herd] Batch", Head: "herd/batch/1-batch"}},
		},
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				1: {Number: 1, Title: "Batch"},
			},
		},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{BatchNumber: 1, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 60, result.BatchPRNumber)
}

func TestReview_BatchLookup_NoPR(t *testing.T) {
	mock := &mockPlatform{
		issues: newMockIssueService(),
		prs: &mockPRService{
			listResult: []*platform.PullRequest{},
		},
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				5: {Number: 5, Title: "My Feature"},
			},
		},
	}

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewParams{BatchNumber: 5, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 0, result.BatchPRNumber)
}

func TestReview_AutoMergeFailure(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRServiceWithMergeErr{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		mergeErr: fmt.Errorf("merge conflict on GitHub"),
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator:   config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	// Should propagate the merge error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-merging batch PR")
	// Post-merge cleanup should NOT have run (milestone not closed)
	assert.Empty(t, issueSvc.updatedIssues)
}

func TestReview_DisabledAutoMergeFailure(t *testing.T) {
	// When review is disabled but auto-merge fails, error should propagate
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRServiceWithMergeErr{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		mergeErr: fmt.Errorf("branch protection"),
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	_, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator:   config.Integrator{Review: false},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-merging batch PR")
	assert.Empty(t, issueSvc.updatedIssues) // No cleanup ran
}

func TestReview_AgentError(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewErr: fmt.Errorf("agent crashed"),
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	// Agent errors now return a neutral result (not an error) so the workflow
	// succeeds and the review retries on the next trigger.
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Empty(t, result.FixIssues)
}

// mockCapturingPRService wraps mockPRService and captures AddComment and CreateReview calls.
type mockCapturingPRService struct {
	*mockPRService
	comments []string
	reviews  []capturedReview
}

type capturedReview struct {
	body  string
	event platform.ReviewEvent
}

func reviewEvents(reviews []capturedReview) []platform.ReviewEvent {
	events := make([]platform.ReviewEvent, 0, len(reviews))
	for _, review := range reviews {
		events = append(events, review.event)
	}
	return events
}

func (m *mockCapturingPRService) AddComment(_ context.Context, _ int, body string) error {
	m.comments = append(m.comments, body)
	return nil
}

func (m *mockCapturingPRService) CreateReview(_ context.Context, _ int, body string, event platform.ReviewEvent) error {
	m.reviews = append(m.reviews, capturedReview{body: body, event: event})
	return nil
}

func TestReview_DispatchCountAccurateWhenSomeCreatesFail(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	// Create always succeeds (single batched issue now)
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Issue one"},
				{Severity: "HIGH", Description: "Issue two"},
			},
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// One batched fix issue
	assert.Len(t, result.FixIssues, 1)

	// The findings comment should contain structured HIGH section
	require.NotEmpty(t, prSvc.comments)
	findingsComment := ""
	for _, c := range prSvc.comments {
		if strings.HasPrefix(c, "🔍") {
			findingsComment = c
			break
		}
	}
	require.NotEmpty(t, findingsComment, "expected a findings comment")
	assert.Contains(t, findingsComment, "**HIGH**")
}

func TestReview_NoCommentWhenAllCreatesFail(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	// All Creates fail
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return nil, fmt.Errorf("create failed")
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Issue one"},
				{Severity: "HIGH", Description: "Issue two"},
			},
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Empty(t, result.FixIssues)
	assert.True(t, result.AllCreatesFailed, "AllCreatesFailed must be true when all issue creates fail")
	assert.Equal(t, 2, result.FindingsCount, "FindingsCount must reflect the number of high findings")

	// No findings comment should be posted when all creates fail
	for _, c := range prSvc.comments {
		assert.False(t, strings.HasPrefix(c, "🔍"), "findings comment must not be posted when create fails")
	}
}

func TestParseBatchBranchMilestone(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		want    int
		wantErr bool
	}{
		{"valid", "herd/batch/4-some-slug", 4, false},
		{"valid single digit", "herd/batch/1-batch", 1, false},
		{"valid multi digit", "herd/batch/42-long-name-here", 42, false},
		{"not a batch branch", "herd/worker/10-task", 0, true},
		{"no dash", "herd/batch/4", 0, true},
		{"not a number", "herd/batch/abc-slug", 0, true},
		{"random string", "main", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBatchBranchMilestone(tt.branch)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
	assert.Equal(t, "first line", truncate("first line\nsecond line", 60))
}

func TestBuildCurrentPRMetadata(t *testing.T) {
	tests := []struct {
		name     string
		pr       *platform.PullRequest
		headSHA  string
		labels   []string
		ciStatus string
		want     []string
	}{
		{
			name: "complete metadata",
			pr: &platform.PullRequest{
				Number:           50,
				Head:             "feature",
				Base:             "main",
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "clean",
			},
			headSHA:  "abc123",
			labels:   []string{"herd/status:done", "reviewed"},
			ciStatus: "success",
			want: []string{
				"PR number: #50",
				"Head branch: feature",
				"Base branch: main",
				"Head SHA: abc123",
				"Mergeable known: true",
				"Mergeable: true",
				"Merge state status: clean",
				"Labels: herd/status:done, reviewed",
				"CI status: success",
			},
		},
		{
			name: "missing optional data",
			pr: &platform.PullRequest{
				Number: 77,
			},
			want: []string{
				"PR number: #77",
				"Head branch: unavailable",
				"Base branch: unavailable",
				"Head SHA: unavailable",
				"Mergeable known: false",
				"Mergeable: false",
				"Merge state status: unavailable",
				"Labels: (none)",
				"CI status: unavailable",
			},
		},
		{
			name:     "nil PR still records external state",
			headSHA:  "def456",
			labels:   []string{"bug"},
			ciStatus: "pending",
			want: []string{
				"Head SHA: def456",
				"Labels: bug",
				"CI status: pending",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCurrentPRMetadata(tt.pr, tt.headSHA, tt.labels, tt.ciStatus)
			for _, want := range tt.want {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestCurrentPRCIStatus(t *testing.T) {
	tests := []struct {
		name       string
		checks     *metadataCheckService
		headSHA    string
		headRef    string
		wantStatus string
		wantRef    string
	}{
		{
			name:       "nil checks unavailable",
			wantStatus: "unavailable",
		},
		{
			name:       "empty ref unavailable",
			checks:     &metadataCheckService{status: "success"},
			wantStatus: "unavailable",
		},
		{
			name:       "uses head sha before branch",
			checks:     &metadataCheckService{status: "success"},
			headSHA:    "abc123",
			headRef:    "feature",
			wantStatus: "success",
			wantRef:    "abc123",
		},
		{
			name:       "falls back to branch",
			checks:     &metadataCheckService{status: "pending"},
			headRef:    "feature",
			wantStatus: "pending",
			wantRef:    "feature",
		},
		{
			name:       "error unavailable",
			checks:     &metadataCheckService{err: fmt.Errorf("boom")},
			headSHA:    "abc123",
			wantStatus: "unavailable",
			wantRef:    "abc123",
		},
		{
			name:       "empty status unavailable",
			checks:     &metadataCheckService{status: " "},
			headSHA:    "abc123",
			wantStatus: "unavailable",
			wantRef:    "abc123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockPlatform{checks: tt.checks}
			got := currentPRCIStatus(context.Background(), mock, tt.headSHA, tt.headRef)
			assert.Equal(t, tt.wantStatus, got)
			if tt.checks != nil {
				assert.Equal(t, tt.wantRef, tt.checks.ref)
			}
		})
	}
}

// capturingMockAgent captures ReviewOptions for assertions
type capturingMockAgent struct {
	result       *agent.ReviewResult
	capturedOpts *agent.ReviewOptions
	capturedDiff *string
}

func (m *capturingMockAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *capturingMockAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *capturingMockAgent) Review(_ context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	if m.capturedDiff != nil {
		*m.capturedDiff = diff
	}
	*m.capturedOpts = opts
	return m.result, nil
}
func (m *capturingMockAgent) Discuss(_ context.Context, _ agent.DiscussOptions) error {
	return nil
}

type metadataCheckService struct {
	status string
	err    error
	ref    string
}

func (m *metadataCheckService) GetCombinedStatus(_ context.Context, ref string) (string, error) {
	m.ref = ref
	return m.status, m.err
}

func (m *metadataCheckService) RerunFailedChecks(_ context.Context, _ string) error {
	return nil
}

// mockPRServiceWithMergeErr wraps mockPRService to fail on Merge
type mockPRServiceWithMergeErr struct {
	*mockPRService
	mergeErr error
}

func (m *mockPRServiceWithMergeErr) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, m.mergeErr
}

// mockIssueServiceWithCreate wraps mockIssueService to override Create
type mockIssueServiceWithCreate struct {
	*mockIssueService
	onCreate func(title, body string, labels []string, milestone *int) (*platform.Issue, error)
}

func (m *mockIssueServiceWithCreate) Create(ctx context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if m.onCreate != nil {
		return m.onCreate(title, body, labels, milestone)
	}
	return nil, nil
}

func makeHighReviewFindings(prefix string, count int) []agent.ReviewFinding {
	findings := make([]agent.ReviewFinding, 0, count)
	for i := 1; i <= count; i++ {
		findings = append(findings, agent.ReviewFinding{
			Severity:    "HIGH",
			Description: fmt.Sprintf("%s %02d", prefix, i),
		})
	}
	return findings
}

func initChunkedReviewRepo(t *testing.T, files int) (string, *git.Git, string) {
	t.Helper()
	dir, g := initTestRepo(t)
	require.NoError(t, g.Checkout("herd/batch/1-batch"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0755))
	for i := 1; i <= files; i++ {
		path := filepath.Join(dir, "src", fmt.Sprintf("chunk_%02d.go", i))
		content := fmt.Sprintf("package src\n\nfunc Chunk%02d() string { return \"chunk-%02d\" }\n", i, i)
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
		runReviewTestGit(t, dir, "add", path)
	}
	runReviewTestGit(t, dir, "commit", "-m", "add chunked review files")
	headSHA := reviewTestGitOutput(t, dir, "rev-parse", "HEAD")
	return dir, g, headSHA
}

func newChunkedReviewPlatform(issueSvc platform.IssueService, prSvc *mockCapturingPRService, headSHA string) *mockPlatform {
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.workflows = &mockWorkflowService{}
	mock.repo = &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": headSHA},
	}
	return mock
}

func newCapturingBatchPRService() *mockCapturingPRService {
	return &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
			},
		},
	}
}

func requireCommentContaining(t *testing.T, comments []string, needle string) string {
	t.Helper()
	for _, comment := range comments {
		if strings.Contains(comment, needle) {
			return comment
		}
	}
	require.Failf(t, "missing comment", "expected a PR comment containing %q in %#v", needle, comments)
	return ""
}

func assertPromptDescribesInvocationTimePRMetadata(t *testing.T, prompt string) {
	t.Helper()

	assert.Contains(t, prompt, "fetched immediately before the agent review began")
	assert.Contains(t, prompt, "HerdOS refreshes live GitHub PR metadata again before applying review results")
	assert.Contains(t, prompt, "that later live refresh wins if it differs")
	assert.NotContains(t, prompt, "fetched fresh immediately before this review and is authoritative for current PR state")
}

func runChunkedReviewFixture(t *testing.T, files int, results []*agent.ReviewResult, issueSvc platform.IssueService) (*ReviewResult, *mockCapturingPRService, error) {
	t.Helper()
	dir, g, headSHA := initChunkedReviewRepo(t, files)
	prSvc := newCapturingBatchPRService()
	mock := newChunkedReviewPlatform(issueSvc, prSvc, headSHA)
	ag := &chunkCaptureAgent{results: results}
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 10,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
			},
		},
		Workers: config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})
	return result, prSvc, err
}

// --- New Tests ---

func TestReview_OnlyLowFindings_Approves(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Summary:  "Looks good overall",
			Findings: []agent.ReviewFinding{
				{Severity: "LOW", Description: "Typo in comment"},
			},
			Comments: []string{"Typo in comment"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved, "Should approve when only LOW findings")

	// Verify both review cycle comment and batch summary comment are posted
	require.Len(t, prSvc.comments, 2, "Expected review cycle comment and batch summary comment")

	assert.True(t, strings.HasPrefix(prSvc.comments[0], "🔍"), "First comment should be review cycle comment")
	assert.True(t, strings.Contains(prSvc.comments[1], "Batch Summary"), "Second comment should be batch summary")

	// Verify approve review was submitted
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, prSvc.reviews[0].event)
}

func TestReview_RequestChangesReview(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Critical bug"},
			},
			Comments: []string{"Critical bug"},
		},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// Verify CreateReview was called with REQUEST_CHANGES
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.Contains(t, prSvc.reviews[0].body, "actionable issues")
}

func TestReview_BatchFixIssue_SingleIssue(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	var createdTitle string
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdTitle = title
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Bug A"},
				{Severity: "HIGH", Description: "Bug B"},
				{Severity: "HIGH", Description: "Bug C"},
			},
			Comments: []string{"Bug A", "Bug B", "Bug C"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Len(t, result.FixIssues, 1, "Should create ONE fix issue per cycle")
	assert.Equal(t, "Review fixes (cycle 1)", createdTitle)
	assert.Contains(t, createdBody, "Bug A")
	assert.Contains(t, createdBody, "Bug B")
	assert.Contains(t, createdBody, "Bug C")
}

func TestReview_DedupFindings(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Open fix issues from prior cycles — neither is "active" (in-progress
	// or ready), so neither suppresses the corresponding new finding. The
	// done issue is a past attempt and the unlabeled issue is not active.
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix: Missing error handling in auth.go\n"},
		{Number: 81, State: "open", Title: "Review fixes (cycle 2)",
			Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 2\n---\n\n## Task\nFix: Race condition in worker pool\n"},
	}

	createCalled := false
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createCalled = true
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Race condition in worker pool"},
				{Severity: "HIGH", Description: "SQL injection in query builder"},
			},
			Comments: []string{"Missing error handling in auth.go", "Race condition in worker pool", "SQL injection in query builder"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, createCalled, "Should create fix issue")
	assert.Len(t, result.FixIssues, 1)
	// All three findings survive — neither prior fix issue is active
	// (in-progress or ready), so neither participates in dedup. The
	// done fix issue is a past attempt and recurring findings against
	// it must produce a fresh fix worker.
	assert.Contains(t, createdBody, "SQL injection in query builder")
	assert.Contains(t, createdBody, "Missing error handling in auth.go")
	assert.Contains(t, createdBody, "Race condition in worker pool")
}

// TestReview_RecurringFindingNotSwallowed verifies that recurring findings
// matching a prior-cycle fix issue with status=done (worker finished, awaiting
// batch merge) are NOT deduped — the recurrence is evidence the prior fix
// did not take, so a fresh fix worker must be created.
func TestReview_RecurringFindingNotSwallowed(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Prior fix issue from a previous cycle — status=done means the worker
	// finished but the issue is still open awaiting batch merge. The new
	// dedup logic must NOT suppress findings against this issue.
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix: Missing error handling in auth.go\nFix: Race condition in worker pool\n"},
	}

	createCalled := false
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createCalled = true
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Race condition in worker pool"},
			},
			Comments: []string{"Missing error handling in auth.go", "Race condition in worker pool"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, createCalled, "Should create a new fix issue — done fix issues do not dedup")
	assert.False(t, result.Approved, "Should not approve when actionable findings remain")
	assert.Len(t, result.FixIssues, 1)
	// Both recurring findings should appear in the new fix issue body since
	// the prior fix issue is done (a past attempt), not active.
	assert.Contains(t, createdBody, "Missing error handling in auth.go")
	assert.Contains(t, createdBody, "Race condition in worker pool")
}

func TestReview_SkipsCompletedBatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch", State: "closed"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved, "Should not mark as approved when skipping completed batch")
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.Nil(t, result.FixIssues)
}

func TestReview_SkipsWhenSomeFixWorkersStillRunning(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// 3 fix issues: 2 done, 1 in-progress → should skip
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix 1", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 1\n"},
		{Number: 81, Title: "Fix 2", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 2\n"},
		{Number: 82, Title: "Fix 3", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 3\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "something"}},
			Comments: []string{"something"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues, "Should skip review when fix worker is still in progress")
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_ProceedsWhenAllFixWorkersDone_MultipleIssues(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// 3 fix issues: all done → should proceed
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix 1", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 1\n"},
		{Number: 81, Title: "Fix 2", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 2\n"},
		{Number: 82, Title: "Fix 3", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 3\n"},
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		issueSvc.listResult,
	)
	mock.issues = issueSvc

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "All good"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved, "Should proceed and approve when all fix workers are done")
}

func TestReview_StrictnessPassedToAgent(t *testing.T) {
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3, ReviewStrictness: "strict"},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, "strict", capturedOpts.Strictness)
}

func TestFilterFindingsBySeverity(t *testing.T) {
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "bug"},
		{Severity: "MEDIUM", Description: "edge case"},
		{Severity: "LOW", Description: "style"},
		{Severity: "high", Description: "another bug"}, // case insensitive
		{Severity: "", Description: "unknown defaults to low"},
	}
	high, medium, low, criteria := filterFindingsBySeverity(findings)
	assert.Len(t, high, 2)
	assert.Len(t, medium, 1)
	assert.Len(t, low, 2) // empty severity defaults to low
	assert.Len(t, criteria, 0)
}

func TestFilterFindingsBySeverity_Criteria(t *testing.T) {
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "bug"},
		{Severity: "MEDIUM", Description: "edge case"},
		{Severity: "LOW", Description: "style"},
		{Severity: "CRITERIA", Description: "criterion is too vague"},
		{Severity: "high", Description: "another bug"},
		{Severity: "", Description: "unknown defaults to low"},
	}
	high, medium, low, criteria := filterFindingsBySeverity(findings)
	assert.Len(t, high, 2)
	assert.Len(t, medium, 1)
	assert.Len(t, low, 2)
	assert.Len(t, criteria, 1)
	assert.Equal(t, "criterion is too vague", criteria[0].Description)
}

func TestDedupFindings(t *testing.T) {
	tests := []struct {
		name         string
		findings     []agent.ReviewFinding
		openFixes    []*platform.Issue
		wantDescs    []string
		wantDedupLen int
	}{
		{
			name: "title match deduplicates",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Race condition in worker pool"},
			},
			openFixes: []*platform.Issue{
				{Number: 80, Title: "Fix: Missing error handling in auth.go", Body: "Fix it"},
			},
			wantDescs:    []string{"Race condition in worker pool"},
			wantDedupLen: 1,
		},
		{
			name: "batched body matches individual lines not raw body",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Race condition in worker pool"},
				{Severity: "HIGH", Description: "New unrelated finding"},
			},
			openFixes: []*platform.Issue{
				{Number: 80, Title: "Review fixes (cycle 1)",
					Body: "---\nherd:\n  version: 1\n  type: fix\n---\n\n## Task\nFix the following issues found during agent review:\n\n1. Race condition in worker pool\n2. Missing error handling in auth.go\n"},
			},
			wantDescs:    []string{"New unrelated finding"},
			wantDedupLen: 1,
		},
		{
			name: "no false positive on partial substring across batched findings",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "pool timeout is too short"},
			},
			openFixes: []*platform.Issue{
				{Number: 80, Title: "Review fixes (cycle 1)",
					Body: "---\nherd:\n  version: 1\n  type: fix\n---\n\n## Task\nFix the following issues found during agent review:\n\n1. Race condition in worker pool\n2. timeout is too long in scheduler\n"},
			},
			wantDescs:    []string{"pool timeout is too short"},
			wantDedupLen: 1,
		},
		{
			name: "all findings deduped returns empty",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
			},
			openFixes: []*platform.Issue{
				{Number: 80, Title: "Review fixes (cycle 1)",
					Body: "1. Missing error handling in auth.go\n"},
			},
			wantDedupLen: 0,
		},
		{
			name: "short description does not false-positive on substring",
			findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "bug"},
			},
			openFixes: []*platform.Issue{
				{Number: 90, Title: "Fix: debug logging bug in scheduler",
					Body: "1. debug logging bug in scheduler\n"},
			},
			wantDescs:    []string{"bug"},
			wantDedupLen: 1,
		},
		{
			name: "short description exact match still deduplicates",
			findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "bug"},
			},
			openFixes: []*platform.Issue{
				{Number: 91, Title: "bug", Body: "fix it"},
			},
			wantDedupLen: 0,
		},
		{
			name: "short description exact match in body line deduplicates",
			findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "bug"},
			},
			openFixes: []*platform.Issue{
				{Number: 92, Title: "Review fixes (cycle 1)",
					Body: "1. bug\n2. other issue\n"},
			},
			wantDedupLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dedupFindings(tt.findings, tt.openFixes)
			assert.Len(t, result, tt.wantDedupLen)
			for i, desc := range tt.wantDescs {
				assert.Equal(t, desc, result[i].Description)
			}
		})
	}
}

const fixBody = "---\nherd:\n  version: 1\n  type: fix\n---\n\n## Task\nFix: Missing error handling in auth.go\n"

func TestActiveFixIssues_FiltersByStatus(t *testing.T) {
	tests := []struct {
		name        string
		issue       *platform.Issue
		wantInclude bool
	}{
		{
			name: "closed issue with type=fix in-progress is excluded",
			issue: &platform.Issue{Number: 1, State: "closed",
				Labels: []string{issues.StatusInProgress}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open type=feature in-progress is excluded",
			issue: &platform.Issue{Number: 2, State: "open",
				Labels: []string{issues.StatusInProgress},
				Body:   "---\nherd:\n  version: 1\n  type: feature\n---\n\n## Task\nDo it\n"},
			wantInclude: false,
		},
		{
			name: "open type=fix in-progress is included",
			issue: &platform.Issue{Number: 3, State: "open",
				Labels: []string{issues.StatusInProgress}, Body: fixBody},
			wantInclude: true,
		},
		{
			name: "open type=fix ready is included",
			issue: &platform.Issue{Number: 4, State: "open",
				Labels: []string{issues.StatusReady}, Body: fixBody},
			wantInclude: true,
		},
		{
			name: "open type=fix done is excluded",
			issue: &platform.Issue{Number: 5, State: "open",
				Labels: []string{issues.StatusDone}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open type=fix failed is excluded",
			issue: &platform.Issue{Number: 6, State: "open",
				Labels: []string{issues.StatusFailed}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open type=fix blocked is excluded",
			issue: &platform.Issue{Number: 7, State: "open",
				Labels: []string{issues.StatusBlocked}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open type=fix cancelled is excluded",
			issue: &platform.Issue{Number: 8, State: "open",
				Labels: []string{issues.StatusCancelled}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open type=fix with no status label is excluded",
			issue: &platform.Issue{Number: 9, State: "open",
				Labels: []string{}, Body: fixBody},
			wantInclude: false,
		},
		{
			name: "open issue with malformed body (no front matter) is excluded",
			issue: &platform.Issue{Number: 10, State: "open",
				Labels: []string{issues.StatusInProgress}, Body: "no front matter here"},
			wantInclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := activeFixIssues([]*platform.Issue{tt.issue})
			if tt.wantInclude {
				require.Len(t, out, 1)
				assert.Equal(t, tt.issue.Number, out[0].Number)
			} else {
				assert.Empty(t, out)
			}
		})
	}
}

// TestDedupFindings_DoesNotDedupAgainstDoneFixIssues verifies that a finding
// matching a done fix issue is preserved through the filter+dedup pipeline.
func TestDedupFindings_DoesNotDedupAgainstDoneFixIssues(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix: Missing error handling in auth.go\n"},
	}
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "Missing error handling in auth.go"},
	}

	result := dedupFindings(findings, activeFixIssues(allIssues))

	require.Len(t, result, 1)
	assert.Equal(t, "Missing error handling in auth.go", result[0].Description)
}

// TestDedupFindings_DedupsAgainstInProgressFixIssues verifies that findings
// matching an in-progress fix issue ARE removed by the filter+dedup pipeline.
func TestDedupFindings_DedupsAgainstInProgressFixIssues(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusInProgress},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\n1. Missing error handling in auth.go\n"},
	}
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "Missing error handling in auth.go"},
		{Severity: "HIGH", Description: "Brand new unrelated finding"},
	}

	result := dedupFindings(findings, activeFixIssues(allIssues))

	require.Len(t, result, 1)
	assert.Equal(t, "Brand new unrelated finding", result[0].Description)
}

// TestDedupFindings_DedupsAgainstReadyFixIssues verifies that findings
// matching a ready fix issue ARE removed by the filter+dedup pipeline.
func TestDedupFindings_DedupsAgainstReadyFixIssues(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusReady},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\n1. Missing error handling in auth.go\n"},
	}
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "Missing error handling in auth.go"},
		{Severity: "HIGH", Description: "Brand new unrelated finding"},
	}

	result := dedupFindings(findings, activeFixIssues(allIssues))

	require.Len(t, result, 1)
	assert.Equal(t, "Brand new unrelated finding", result[0].Description)
}

func TestDescriptionMatch(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		descPrefix string
		want       bool
	}{
		{
			name:       "long prefix uses substring match",
			text:       "some context about missing error handling in auth.go and more",
			descPrefix: "missing error handling in auth.go",
			want:       true,
		},
		{
			name:       "long prefix no match",
			text:       "something completely different here",
			descPrefix: "missing error handling in auth.go",
			want:       false,
		},
		{
			name:       "short prefix requires exact match",
			text:       "debug logging bug in scheduler",
			descPrefix: "bug",
			want:       false,
		},
		{
			name:       "short prefix exact match succeeds",
			text:       "bug",
			descPrefix: "bug",
			want:       true,
		},
		{
			name:       "empty prefix matches empty text",
			text:       "",
			descPrefix: "",
			want:       true,
		},
		{
			name:       "prefix at boundary length uses substring",
			text:       "xx 01234567890123456789 yy",
			descPrefix: "01234567890123456789",
			want:       true,
		},
		{
			name:       "prefix just under boundary uses equality",
			text:       "xx 0123456789012345678 yy",
			descPrefix: "0123456789012345678",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, descriptionMatch(tt.text, tt.descPrefix))
		})
	}
}

func TestExtractFindingLines(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "numbered list",
			body: "Fix the following:\n\n1. First finding\n2. Second finding\n",
			want: []string{"Fix the following:", "First finding", "Second finding"},
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "plain text lines",
			body: "Fix: something broken\n",
			want: []string{"Fix: something broken"},
		},
		{
			name: "mixed numbered and plain",
			body: "Header\n1. Finding one\nplain line\n2. Finding two\n",
			want: []string{"Header", "Finding one", "plain line", "Finding two"},
		},
		{
			name: "numbered item with empty text after prefix",
			body: "1. \n2. Real finding\n",
			want: []string{"Real finding"},
		},
		{
			name: "numbered item with only whitespace after prefix",
			body: "1.   \n2. Keep this\n",
			want: []string{"Keep this"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFindingLines(tt.body)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildReviewCycleComment(t *testing.T) {
	high := []agent.ReviewFinding{{Severity: "HIGH", Description: "bug"}}
	medium := []agent.ReviewFinding{{Severity: "MEDIUM", Description: "edge case"}}
	low := []agent.ReviewFinding{{Severity: "LOW", Description: "style"}}

	comment := buildReviewCycleComment(2, 5, []int{100}, high, medium, low, nil)
	assert.Contains(t, comment, "cycle 2 of 5")
	assert.Contains(t, comment, "Found 3 issues")
	assert.Contains(t, comment, "**HIGH** (fix worker dispatched → #100)")
	assert.Contains(t, comment, "**MEDIUM** (fix worker dispatched")
	assert.Contains(t, comment, "**LOW** (informational)")
}

func TestBuildReviewCycleComment_NoCycle(t *testing.T) {
	medium := []agent.ReviewFinding{{Severity: "MEDIUM", Description: "edge case"}}
	comment := buildReviewCycleComment(0, 3, nil, nil, medium, nil, nil)
	assert.Contains(t, comment, "🔍 **HerdOS Agent Review**\n\n")
	assert.NotContains(t, comment, "cycle")
	assert.Contains(t, comment, "Found 1 issue:")
	assert.NotContains(t, comment, "Found 1 issues")
}

func TestBuildReviewCycleComment_SingularPlural(t *testing.T) {
	tests := []struct {
		name     string
		high     []agent.ReviewFinding
		medium   []agent.ReviewFinding
		low      []agent.ReviewFinding
		expected string
	}{
		{
			name:     "singular with one finding",
			medium:   []agent.ReviewFinding{{Severity: "MEDIUM", Description: "one issue"}},
			expected: "Found 1 issue:\n\n",
		},
		{
			name:     "plural with two findings",
			high:     []agent.ReviewFinding{{Severity: "HIGH", Description: "bug"}},
			low:      []agent.ReviewFinding{{Severity: "LOW", Description: "style"}},
			expected: "Found 2 issues:\n\n",
		},
		{
			name:     "no findings",
			expected: "No issues found.\n",
		},
		{
			name: "plural with many findings",
			high: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "bug1"},
				{Severity: "HIGH", Description: "bug2"},
				{Severity: "HIGH", Description: "bug3"},
			},
			expected: "Found 3 issues:\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := buildReviewCycleComment(1, 3, nil, tt.high, tt.medium, tt.low, nil)
			assert.Contains(t, comment, tt.expected)
		})
	}
}

func TestBuildReviewCycleComment_WithCriteria(t *testing.T) {
	criteria := []agent.ReviewFinding{
		{Severity: "CRITERIA", Description: "Criterion 'tests pass' is too vague"},
		{Severity: "CRITERIA", Description: "Criterion 'no regressions' is unmeasurable"},
	}
	comment := buildReviewCycleComment(1, 3, nil, nil, nil, nil, criteria)
	assert.Contains(t, comment, "**CRITERIA** (requires human review):")
	assert.Contains(t, comment, "- Criterion 'tests pass' is too vague")
	assert.Contains(t, comment, "- Criterion 'no regressions' is unmeasurable")
}

func TestBuildReviewCycleComment_CriteriaInTotalCount(t *testing.T) {
	high := []agent.ReviewFinding{{Severity: "HIGH", Description: "bug"}}
	criteria := []agent.ReviewFinding{{Severity: "CRITERIA", Description: "vague criterion"}}
	comment := buildReviewCycleComment(1, 3, nil, high, nil, nil, criteria)
	assert.Contains(t, comment, "Found 2 issues:")
}

func TestCollectFixRequests(t *testing.T) {
	tests := []struct {
		name     string
		comments []*platform.Comment
		want     []string
	}{
		{
			name:     "no comments",
			comments: nil,
			want:     nil,
		},
		{
			name: "quoted description",
			comments: []*platform.Comment{
				{Body: `/herd fix "make the logo bigger"`},
			},
			want: []string{"make the logo bigger"},
		},
		{
			name: "unquoted description",
			comments: []*platform.Comment{
				{Body: "/herd fix make the logo bigger"},
			},
			want: []string{"make the logo bigger"},
		},
		{
			name: "mixed fix and non-fix comments",
			comments: []*platform.Comment{
				{Body: "looks good to me"},
				{Body: `/herd fix "fix the typo"`},
				{Body: "nice work"},
				{Body: "/herd fix add error handling"},
			},
			want: []string{"fix the typo", "add error handling"},
		},
		{
			name: "empty description skipped",
			comments: []*platform.Comment{
				{Body: "/herd fix"},
			},
			want: nil,
		},
		{
			name: "/herd fixci not matched",
			comments: []*platform.Comment{
				{Body: "/herd fixci something"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listCommentsResult = tt.comments
			mock := &mockPlatform{
				issues: issueSvc,
			}
			got := collectFixRequests(context.Background(), mock, 1)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReview_PassesFixRequestsToAgent(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: `/herd fix "use larger font"`},
	}

	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Contains(t, capturedOpts.AcceptanceCriteria, "User requested: use larger font")
}

func TestReview_NoFixComments_NoCriteriaAdded(t *testing.T) {
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	for _, c := range capturedOpts.AcceptanceCriteria {
		assert.NotContains(t, c, "User requested:")
	}
}

func TestCollectPriorReviewComments(t *testing.T) {
	tests := []struct {
		name     string
		comments []*platform.Comment
		want     []string
	}{
		{
			name:     "no comments",
			comments: nil,
			want:     nil,
		},
		{
			name: "mixed comment types only returns review comments",
			comments: []*platform.Comment{
				{Body: "looks good to me"},
				{Body: `/herd fix "fix the typo"`},
				{Body: "🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue"},
				{Body: "nice work"},
			},
			want: []string{"🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue"},
		},
		{
			name: "both emoji prefixes matched",
			comments: []*platform.Comment{
				{Body: "🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound issues"},
				{Body: "✅ **HerdOS Agent Review** (cycle 2 of 3)\n\nAll good"},
			},
			want: []string{
				"🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound issues",
				"✅ **HerdOS Agent Review** (cycle 2 of 3)\n\nAll good",
			},
		},
		{
			name: "non-matching similar prefix not matched",
			comments: []*platform.Comment{
				{Body: "🔍 Some other thing"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectPriorReviewComments(tt.comments)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReview_PassesPriorReviewCommentsToAgent(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: "looks good"},
		{Body: "🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue:\n\n**HIGH**:\n- Missing error handling"},
		{Body: `/herd fix "use larger font"`},
		{Body: "✅ **HerdOS Agent Review** (cycle 2 of 3)\n\nAll good"},
	}

	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue:\n\n**HIGH**:\n- Missing error handling",
		"✅ **HerdOS Agent Review** (cycle 2 of 3)\n\nAll good",
	}, capturedOpts.PriorReviewComments)
	// Also verify fix requests are merged into acceptance criteria
	assert.Contains(t, capturedOpts.AcceptanceCriteria, "User requested: use larger font")
}

func TestReview_NoPriorReviewComments_EmptyField(t *testing.T) {
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Nil(t, capturedOpts.PriorReviewComments)
}

func TestReview_CurrentPRMetadataPassedToAgent(t *testing.T) {
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{
			Number:           50,
			Title:            "[herd] Batch",
			Head:             "herd/batch/1-batch",
			Base:             "main",
			Labels:           []string{"herd/status:done", "reviewed"},
			MergeableKnown:   true,
			Mergeable:        true,
			MergeStateStatus: "clean",
		}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)
	mock.checks = &metadataCheckService{status: "success"}
	mock.repo.branchSHAs = map[string]string{"herd/batch/1-batch": "abc123"}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "PR number: #50")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Head branch: herd/batch/1-batch")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Base branch: main")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Head SHA: abc123")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Mergeable known: true")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Mergeable: true")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Merge state status: clean")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Labels: herd/status:done, reviewed")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "CI status: success")
}

func TestCollectUserFeedbackComments(t *testing.T) {
	tests := []struct {
		name     string
		comments []*platform.Comment
		want     []string
	}{
		{
			name:     "no comments",
			comments: nil,
			want:     nil,
		},
		{
			name: "only user comments returned",
			comments: []*platform.Comment{
				{Body: "🔍 **HerdOS Agent Review**\nFindings..."},
				{Body: "This nil check finding is a false positive"},
				{Body: "/herd fix something"},
			},
			want: []string{"This nil check finding is a false positive"},
		},
		{
			name: "all HerdOS prefixes excluded",
			comments: []*platform.Comment{
				{Body: "🔍 **HerdOS Agent Review**\nFindings"},
				{Body: "✅ **HerdOS Agent Review**\nApproved"},
				{Body: "⚠️ **HerdOS Integrator**\nWarning"},
				{Body: "🔧 Fix something"},
				{Body: "🔄 **Integrator**\nRetrying"},
				{Body: "📋 **Worker Progress**\nUpdate"},
				{Body: "/herd fix thing"},
				{Body: "/herd retry"},
			},
			want: nil,
		},
		{
			name: "empty and whitespace-only comments excluded",
			comments: []*platform.Comment{
				{Body: ""},
				{Body: "   "},
				{Body: "\n\t\n"},
				{Body: "Real feedback here"},
			},
			want: []string{"Real feedback here"},
		},
		{
			name: "trimmed body is used for prefix check",
			comments: []*platform.Comment{
				{Body: "   🔍 **HerdOS Agent Review**\nFindings"},
				{Body: "  user feedback with leading space  "},
			},
			want: []string{"user feedback with leading space"},
		},
		{
			name: "multiple user comments preserved in order",
			comments: []*platform.Comment{
				{Body: "first feedback"},
				{Body: "🔍 **HerdOS Agent Review**\nFindings"},
				{Body: "second feedback"},
			},
			want: []string{"first feedback", "second feedback"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectUserFeedbackComments(tt.comments)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReview_UserFeedbackPassedToAgent(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: "🔍 **HerdOS Agent Review**\nFindings..."},
		{Body: "This nil check finding is a false positive"},
		{Body: "/herd fix something"},
	}

	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, []string{"This nil check finding is a false positive"}, capturedOpts.UserFeedbackComments)
}

func TestBuildBatchSummaryComment(t *testing.T) {
	tests := []struct {
		name     string
		issues   []*platform.Issue
		summary  string
		expected []string
	}{
		{
			name: "separates review fix and CI fix issues",
			issues: []*platform.Issue{
				{Number: 1, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo A\n"},
				{Number: 2, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo B\n"},
				{Number: 3, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 2\n---\n\n## Task\nFix\n"},
				{Number: 4, Body: "---\nherd:\n  version: 1\n  ci_fix_cycle: 1\n---\n\n## Task\nCI Fix\n"},
			},
			summary: "All looks good",
			expected: []string{
				"✅ **HerdOS Agent Review**",
				"All looks good",
				"Original tasks: 2",
				"Review fix issues: 1",
				"CI fix issues: 1",
				"Review cycles: 2",
				"CI fix cycles: 1",
				"Total issues: 4",
			},
		},
		{
			name:    "no issues",
			issues:  nil,
			summary: "Empty batch",
			expected: []string{
				"Original tasks: 0",
				"Review fix issues: 0",
				"CI fix issues: 0",
				"Total issues: 0",
			},
		},
		{
			name: "only original tasks",
			issues: []*platform.Issue{
				{Number: 1, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo A\n"},
			},
			summary: "Clean",
			expected: []string{
				"Original tasks: 1",
				"Review fix issues: 0",
				"CI fix issues: 0",
				"Review cycles: 0",
				"CI fix cycles: 0",
			},
		},
		{
			name: "CI fix issue with type fix uses CIFixCycle",
			issues: []*platform.Issue{
				{Number: 1, Body: "---\nherd:\n  version: 1\n  type: fix\n  ci_fix_cycle: 2\n---\n\n## Task\nCI fix with type fix\n"},
			},
			summary: "Mixed",
			expected: []string{
				"Original tasks: 0",
				"Review fix issues: 0",
				"CI fix issues: 1",
				"CI fix cycles: 2",
			},
		},
		{
			name: "multiple review fix cycles",
			issues: []*platform.Issue{
				{Number: 1, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 1\n"},
				{Number: 2, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix 3\n"},
			},
			summary: "Fixes",
			expected: []string{
				"Review fix issues: 2",
				"CI fix issues: 0",
				"Review cycles: 3",
			},
		},
		{
			name: "body without front matter counted as original task",
			issues: []*platform.Issue{
				{Number: 1, Body: "not a herd issue"},
				{Number: 2, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo A\n"},
			},
			summary: "With junk",
			expected: []string{
				"Original tasks: 2",
				"Total issues: 2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := buildBatchSummaryComment(tt.issues, tt.summary)
			for _, exp := range tt.expected {
				assert.Contains(t, comment, exp)
			}
		})
	}
}

// --- Tests for ReviewStandalone ---

func newStandalonePlatform() (*mockPlatform, *mockCapturingPRService, *mockIssueService, *mockWorkflowService) {
	issueSvc := newMockIssueService()
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffResult: "diff --git a/main.go b/main.go\n",
			getResult: map[int]*platform.PullRequest{
				77: {
					Number:           77,
					Title:            "Standalone",
					Base:             "main",
					Head:             "feature",
					Labels:           []string{"needs-review"},
					MergeStateStatus: "blocked",
				},
			},
		},
	}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}
	return mock, prSvc, issueSvc, wf
}

type standaloneRefreshPRService struct {
	*mockCapturingPRService
	initial    *platform.PullRequest
	refreshed  *platform.PullRequest
	refreshErr error
	getCalls   int
}

func (m *standaloneRefreshPRService) Get(context.Context, int) (*platform.PullRequest, error) {
	m.getCalls++
	if m.getCalls == 1 {
		return m.initial, nil
	}
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshed, nil
}

func TestReviewStandalone_PostsComment(t *testing.T) {
	mock, prSvc, issueSvc, wf := newStandalonePlatform()

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling"},
				{Severity: "MEDIUM", Description: "Consider adding tests"},
			},
		},
	}

	result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.FindingsCount)

	// Findings comment posted
	require.Len(t, prSvc.comments, 1)
	assert.True(t, strings.HasPrefix(prSvc.comments[0], "🔍"), "expected findings comment with 🔍 prefix")
	assert.Contains(t, prSvc.comments[0], "**HIGH**")
	assert.Contains(t, prSvc.comments[0], "Missing error handling")
	assert.Contains(t, prSvc.comments[0], "**MEDIUM**")

	// No fix issues, no workers
	assert.Empty(t, issueSvc.createdTitle, "standalone review must not create fix issues")
	assert.Empty(t, wf.dispatched, "standalone review must not dispatch workers")

	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
}

func TestReviewStandalone_ReturnsErrorWhenRefetchedPRFails(t *testing.T) {
	tests := []struct {
		name       string
		refreshed  *platform.PullRequest
		refreshErr error
		wantErr    string
	}{
		{
			name:    "nil refreshed PR",
			wantErr: "platform returned nil PR for #77",
		},
		{
			name:       "refresh error",
			refreshErr: errors.New("github unavailable"),
			wantErr:    "refreshing PR #77 after standalone review for current merge state: github unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			prSvc := &standaloneRefreshPRService{
				mockCapturingPRService: &mockCapturingPRService{
					mockPRService: &mockPRService{
						diffResult: "diff --git a/main.go b/main.go\n",
					},
				},
				initial: &platform.PullRequest{
					Number: 77,
					Title:  "Standalone",
					Base:   "main",
					Head:   "feature",
				},
				refreshed:  tt.refreshed,
				refreshErr: tt.refreshErr,
			}
			mock := &mockPlatform{
				issues:     issueSvc,
				prs:        prSvc,
				workflows:  &mockWorkflowService{},
				repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
				milestones: &mockMilestoneService{},
			}
			ag := &mockReviewAgent{
				reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
			}

			result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
				Integrator: config.Integrator{Review: true},
			}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

			require.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, 2, prSvc.getCalls)
			assert.Empty(t, prSvc.comments)
			assert.Empty(t, prSvc.reviews)
		})
	}
}

func TestReviewStandalone_ReconcilesStalePRStateFindings(t *testing.T) {
	tests := []struct {
		name              string
		initialPR         platform.PullRequest
		postReviewPR      *platform.PullRequest
		finding           agent.ReviewFinding
		wantFiltered      bool
		wantReviewEvent   platform.ReviewEvent
		wantStaleMetadata bool
	}{
		{
			name: "clean filters unanchored merge conflict",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
				Labels:           []string{issues.CascadeFailed},
			},
			finding:           agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantFiltered:      true,
			wantReviewEvent:   platform.ReviewApprove,
			wantStaleMetadata: true,
		},
		{
			name: "dirty preserves unanchored merge conflict",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "DIRTY",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "blocked preserves unanchored merge conflict",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "BLOCKED",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "unknown preserves unanchored merge conflict",
			initialPR: platform.PullRequest{
				MergeableKnown:   false,
				MergeStateStatus: "UNKNOWN",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "post-review dirty preserves unanchored merge conflict despite initial clean",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			},
			postReviewPR: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "DIRTY",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "post-review blocked preserves unanchored merge conflict despite initial clean",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			},
			postReviewPR: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "BLOCKED",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "post-review unknown preserves unanchored merge conflict despite initial clean",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			},
			postReviewPR: &platform.PullRequest{
				MergeableKnown:   false,
				MergeStateStatus: "UNKNOWN",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
		{
			name: "post-review clean filters unanchored merge conflict despite initial dirty",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        false,
				MergeStateStatus: "DIRTY",
			},
			postReviewPR: &platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			},
			finding:           agent.ReviewFinding{Severity: "HIGH", Description: "Unresolved merge conflict blocks merge"},
			wantFiltered:      true,
			wantReviewEvent:   platform.ReviewApprove,
			wantStaleMetadata: true,
		},
		{
			name: "clean preserves file-level merge conflict logic",
			initialPR: platform.PullRequest{
				MergeableKnown:   true,
				Mergeable:        true,
				MergeStateStatus: "CLEAN",
			},
			finding:         agent.ReviewFinding{Severity: "HIGH", Description: "internal/merge/resolver.go: resolve merge conflicts ignores errors"},
			wantReviewEvent: platform.ReviewRequestChanges,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, prSvc, issueSvc, wf := newStandalonePlatform()
			initialPR := tt.initialPR
			initialPR.Number = 77
			initialPR.Title = "Standalone"
			initialPR.Base = "main"
			initialPR.Head = "feature"
			var postReviewPR platform.PullRequest
			if tt.postReviewPR == nil {
				postReviewPR = initialPR
			} else {
				postReviewPR = *tt.postReviewPR
			}
			prSvc.getResults = []*platform.PullRequest{&initialPR, &postReviewPR}
			ag := &mockReviewAgent{
				reviewResult: &agent.ReviewResult{
					Approved: false,
					Findings: []agent.ReviewFinding{tt.finding},
				},
			}

			result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
				Integrator: config.Integrator{Review: true},
			}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, 2, prSvc.getCalls)
			require.Len(t, prSvc.comments, 1)
			require.Len(t, prSvc.reviews, 1)
			assert.Equal(t, tt.wantReviewEvent, prSvc.reviews[0].event)
			assert.Empty(t, issueSvc.createdTitle, "standalone review must not create fix issues")
			assert.Empty(t, issueSvc.removedLabels[77], "standalone review must not clean up labels")
			assert.Empty(t, wf.dispatched, "standalone review must not dispatch workers")

			if tt.wantFiltered {
				assert.Equal(t, 0, result.FindingsCount)
				assert.NotContains(t, prSvc.comments[0], tt.finding.Description)
			} else {
				assert.Equal(t, 1, result.FindingsCount)
				assert.Contains(t, prSvc.comments[0], tt.finding.Description)
			}
			if tt.wantStaleMetadata {
				assert.Contains(t, prSvc.comments[0], "Stale PR-state findings ignored")
				assert.Contains(t, prSvc.comments[0], "- Stale PR-state findings ignored: 1")
			} else {
				assert.NotContains(t, prSvc.comments[0], "- Stale PR-state findings ignored:")
			}
			if tt.postReviewPR != nil {
				prompt, renderErr := agentprompt.RenderReviewPrompt(ag.lastDiff, ag.lastOpts)
				require.NoError(t, renderErr)
				assert.Contains(t, prompt, "Merge state status: "+tt.initialPR.MergeStateStatus)
				assert.NotContains(t, prompt, "Merge state status: "+tt.postReviewPR.MergeStateStatus)
				assertPromptDescribesInvocationTimePRMetadata(t, prompt)
			}
		})
	}
}

func TestReviewStandalone_Approved(t *testing.T) {
	mock, prSvc, issueSvc, wf := newStandalonePlatform()

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.FindingsCount)

	// Approval comment posted
	require.Len(t, prSvc.comments, 1)
	assert.True(t, strings.HasPrefix(prSvc.comments[0], "✅"))
	assert.Contains(t, prSvc.comments[0], "LGTM")

	// Approve review submitted
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, prSvc.reviews[0].event)

	// No fix issues, no workers
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
}

func TestReviewStandalone_CoverageBlockedWithFindingsPostsCoverageAndFindings(t *testing.T) {
	mock, prSvc, issueSvc, wf := newStandalonePlatform()
	prSvc.diffResult = strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"index 1111111..2222222 100644",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1 +1 @@",
		"-oldA",
		"+newA",
		"diff --git a/b.go b/b.go",
		"index 3333333..4444444 100644",
		"--- a/b.go",
		"+++ b/b.go",
		"@@ -1 +1 @@",
		"-oldB",
		"+newB",
		"",
	}, "\n")
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Summary:  "found issue",
			Findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "Ordinary review finding"},
			},
		},
	}

	result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{
			Review: true,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
				MaxChunks:        1,
			},
		},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.FindingsCount)
	assert.Equal(t, 1, ag.calls)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "not all material source files were reviewed")
	assert.Contains(t, prSvc.comments[0], "b.go: max chunks reached")
	assert.Contains(t, prSvc.comments[0], "Ordinary review finding")
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
}

func TestReviewStandalone_ZeroChunksWithOnlyAllowableOmissionsRequestsChanges(t *testing.T) {
	mock, prSvc, issueSvc, wf := newStandalonePlatform()
	prSvc.diffErr = platform.ErrPullRequestDiffTooLarge
	prSvc.listFilesResult = []*platform.PullRequestFile{
		{Path: "dist/app.js", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		{Path: "image.png", Status: "added"},
	}
	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "should not run"},
	}

	result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.FindingsCount)
	assert.Equal(t, 0, ag.calls)
	require.Len(t, prSvc.comments, 1)
	assert.Contains(t, prSvc.comments[0], "No review chunks were sent to the review agent")
	assert.Contains(t, prSvc.comments[0], "Files summarized but not reviewed")
	assert.Contains(t, prSvc.comments[0], "dist/app.js: generated file")
	assert.Contains(t, prSvc.comments[0], "image.png: binary file")
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.Empty(t, issueSvc.createdTitle)
	assert.Empty(t, wf.dispatched)
}

func TestReviewStandalone_NoFixIssues(t *testing.T) {
	mock, prSvc, issueSvc, wf := newStandalonePlatform()

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Security bug in auth.go"},
				{Severity: "HIGH", Description: "Broken concurrency"},
			},
		},
	}

	_, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewFixSeverity: "medium"},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)

	// A findings comment must be posted
	require.NotEmpty(t, prSvc.comments)

	// No fix issues and no workers dispatched regardless of severity
	assert.Empty(t, issueSvc.createdTitle, "standalone review must NOT create fix issues")
	assert.Empty(t, wf.dispatched, "standalone review must NOT dispatch workers")
}

func TestReviewStandalone_ChunkedRepeatedUnparseablePostsManualIntervention(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, prSvc, issueSvc, wf := newStandalonePlatform()
	prSvc.diffResult = strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"index 1111111..2222222 100644",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1 +1 @@",
		"-oldA",
		"+newA",
		"diff --git a/b.go b/b.go",
		"index 3333333..4444444 100644",
		"--- a/b.go",
		"+++ b/b.go",
		"@@ -1 +1 @@",
		"-oldB",
		"+newB",
		"",
	}, "\n")
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{
		{IsUnparseable: true, Summary: "Failed to parse ..."},
		{IsUnparseable: true, Summary: "Failed to parse ..."},
	}}

	result, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{
			Review: true,
			ReviewDiff: config.ReviewDiff{
				MaxFilesPerChunk: 1,
				MaxChunks:        2,
			},
		},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ManualInterventionNeeded)
	assert.Equal(t, 0, result.FindingsCount)
	require.Len(t, ag.opts, 2)
	assert.True(t, ag.opts[0].ChunkedReview)
	assert.Equal(t, 1, ag.opts[0].ChunkIndex)
	assert.Equal(t, 2, ag.opts[0].TotalChunks)
	assert.Equal(t, 1, ag.opts[1].ChunkIndex, "retry should reuse the first chunk instead of advancing")
	assert.Equal(t, []string{ag.diffs[0], ag.diffs[0]}, ag.diffs, "retry should reuse the same chunk diff")
	assert.Contains(t, ag.diffs[0], "a.go")
	assert.NotContains(t, ag.diffs[0], "diff --git a/b.go b/b.go")

	require.Len(t, prSvc.comments, 1)
	assert.Equal(t, manualInterventionReviewComment, prSvc.comments[0])
	assert.NotContains(t, reviewEvents(prSvc.reviews), platform.ReviewApprove)
	assert.Empty(t, issueSvc.createdTitle, "standalone manual intervention must not create fix issues")
	assert.Empty(t, wf.dispatched, "standalone manual intervention must not dispatch workers")
}

func TestReviewStandalone_ExtraInstructions(t *testing.T) {
	issueSvc := newMockIssueService()
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffResult: "diff --git a/main.go b/main.go\n",
			getResult: map[int]*platform.PullRequest{
				77: {
					Number:           77,
					Title:            "Standalone",
					Base:             "main",
					Head:             "feature",
					Labels:           []string{"needs-review"},
					MergeStateStatus: "blocked",
				},
			},
		},
	}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	var capturedOpts agent.ReviewOptions
	ag := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	dir := t.TempDir()
	// Create a .herd/integrator.md so SystemPrompt is pre-populated before extra instructions are appended.
	require.NoError(t, os.MkdirAll(dir+"/.herd", 0755))
	require.NoError(t, os.WriteFile(dir+"/.herd/integrator.md", []byte("Base instructions"), 0644))

	_, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: dir, ExtraInstructions: "Focus on security issues"})

	require.NoError(t, err)
	assert.Contains(t, capturedOpts.SystemPrompt, "Base instructions")
	assert.Contains(t, capturedOpts.SystemPrompt, "Focus on security issues")
}

func TestReviewStandalone_UserFeedbackPassedToAgent(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: "🔍 **HerdOS Agent Review**\nFindings..."},
		{Body: "This nil check finding is a false positive"},
		{Body: "/herd fix something"},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			diffResult: "diff --git a/main.go b/main.go\n",
			getResult: map[int]*platform.PullRequest{
				77: {
					Number:           77,
					Title:            "Standalone",
					Base:             "main",
					Head:             "feature",
					Labels:           []string{"needs-review"},
					MergeStateStatus: "blocked",
				},
			},
		},
	}
	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	var capturedOpts agent.ReviewOptions
	ag := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	_, err := ReviewStandalone(context.Background(), mock, ag, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewStandaloneParams{PRNumber: 77, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.Equal(t, []string{"This nil check finding is a false positive"}, capturedOpts.UserFeedbackComments)
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "PR number: #77")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Head branch: feature")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Base branch: main")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Head SHA: unavailable")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Merge state status: blocked")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "Labels: needs-review")
	assert.Contains(t, capturedOpts.CurrentPRMetadata, "CI status: unavailable")
}

// --- Unparseable-output retry tests ---

func newUnparseableRetryPlatform() (*mockPlatform, *mockCapturingPRService) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}
	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}
	return mock, prSvc
}

func TestReview_RetriesOnUnparseableOutput(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, _ := newUnparseableRetryPlatform()

	ag := &mockReviewAgent{results: []*agent.ReviewResult{
		{IsUnparseable: true, Summary: "Failed to parse ..."},
		{Approved: true, Summary: "LGTM"},
	}}

	dir, g := initTestRepo(t)
	res, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Approved)
	assert.Equal(t, 2, ag.calls)
	assert.False(t, res.ManualInterventionNeeded)
}

func TestReview_PostsCommentOnRepeatedUnparseable(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, prSvc := newUnparseableRetryPlatform()

	ag := &mockReviewAgent{results: []*agent.ReviewResult{
		{IsUnparseable: true, Summary: "Failed to parse ..."},
		{IsUnparseable: true, Summary: "Failed to parse ..."},
	}}

	dir, g := initTestRepo(t)
	res, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.ManualInterventionNeeded)
	assert.Equal(t, 50, res.BatchPRNumber)
	assert.Equal(t, 2, ag.calls)

	require.Len(t, prSvc.comments, 1)
	assert.Equal(t, manualInterventionReviewComment, prSvc.comments[0])
}

func TestReview_DoesNotSilentlyDrop(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, _ := newUnparseableRetryPlatform()

	ag := &mockReviewAgent{results: []*agent.ReviewResult{
		{IsUnparseable: true, Summary: "Failed to parse ..."},
		{IsUnparseable: true, Summary: "Failed to parse ..."},
	}}

	dir, g := initTestRepo(t)
	res, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.ManualInterventionNeeded, "ManualInterventionNeeded must be true to prove the silent-drop is gone")
	assert.False(t, res.Approved)
}

func TestReview_NoRetryWhenFirstCallSucceeds(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, _ := newUnparseableRetryPlatform()

	ag := &mockReviewAgent{results: []*agent.ReviewResult{
		{Approved: true, Summary: "LGTM"},
	}}

	dir, g := initTestRepo(t)
	res, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Approved)
	assert.Equal(t, 1, ag.calls, "must not retry when first call succeeds")
}

func TestReview_BackwardCompatLegacyFailedToParse(t *testing.T) {
	// Older claude package versions did not set IsUnparseable; instead the
	// failure was signaled via a Summary prefix. The retry/manual-intervention
	// flow must still trigger.
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, prSvc := newUnparseableRetryPlatform()

	ag := &mockReviewAgent{results: []*agent.ReviewResult{
		{IsUnparseable: false, Summary: "Failed to parse review output"},
		{IsUnparseable: false, Summary: "Failed to parse review output"},
	}}

	dir, g := initTestRepo(t)
	res, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.ManualInterventionNeeded)
	assert.Equal(t, 2, ag.calls)
	require.NotEmpty(t, prSvc.comments)
}

// blockingMockReviewAgent returns an unparseable result on the first call,
// then blocks on ctx for any subsequent calls.
type blockingMockReviewAgent struct {
	calls int
}

func (b *blockingMockReviewAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (b *blockingMockReviewAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (b *blockingMockReviewAgent) Discuss(_ context.Context, _ agent.DiscussOptions) error {
	return nil
}
func (b *blockingMockReviewAgent) Review(ctx context.Context, _ string, _ agent.ReviewOptions) (*agent.ReviewResult, error) {
	b.calls++
	if b.calls == 1 {
		return &agent.ReviewResult{IsUnparseable: true, Summary: "Failed to parse ..."}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReview_CtxCancelledDuringRetryWait(t *testing.T) {
	// Use a delay long enough that we can cancel the context before it expires.
	old := unparseableRetryDelay
	unparseableRetryDelay = 5 * time.Second
	t.Cleanup(func() { unparseableRetryDelay = old })

	mock, _ := newUnparseableRetryPlatform()

	ag := &blockingMockReviewAgent{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	dir, g := initTestRepo(t)
	res, err := Review(ctx, mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	// Review() swallows the agent-side error and returns a neutral result.
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Approved)
	assert.False(t, res.ManualInterventionNeeded)
	assert.Equal(t, 1, ag.calls, "second Review call should not fire after ctx cancellation")
}

// --- Worker no-op verdict collection / stable disagreement tests (#577) ---

const sampleWorkerVerdictA = "**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- **Missing nil check**: src/foo.go:12 — the nil-check claim is wrong; the value is constructed three lines above and is never nil at this point.\n\nConclusion: code already handles this correctly."

const sampleWorkerVerdictB = "**Worker #43 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- **Race condition in worker pool**: src/pool.go:80 — the alleged race is impossible because the mutex held in submit() covers the read at line 95.\n\nConclusion: no race exists."

func TestCollectWorkerNoOpVerdicts(t *testing.T) {
	comments := []*platform.Comment{
		{Body: sampleWorkerVerdictA},
		{Body: "🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue"},
		{Body: "This finding is a false positive — please look at the test file"},
		{Body: sampleWorkerVerdictB},
		{Body: "/herd fix something"},
		{Body: ""},
	}

	got := collectWorkerNoOpVerdicts(comments)
	require.Len(t, got, 2)
	assert.Equal(t, sampleWorkerVerdictA, got[0])
	assert.Equal(t, sampleWorkerVerdictB, got[1])
}

func TestCollectWorkerNoOpVerdicts_NoVerdicts(t *testing.T) {
	comments := []*platform.Comment{
		{Body: "🔍 **HerdOS Agent Review**"},
		{Body: "Plain user feedback"},
		{Body: "/herd fix"},
		{Body: ""},
		// First line shape is close but missing the suffix
		{Body: "**Worker #99 — some other note**\n\nbody"},
		// First line shape is close but missing the prefix
		{Body: "Worker #99 — no-op verdict\n\nbody"},
	}
	got := collectWorkerNoOpVerdicts(comments)
	assert.Nil(t, got)
}

func TestCollectUserFeedbackComments_ExcludesWorkerVerdicts(t *testing.T) {
	comments := []*platform.Comment{
		{Body: sampleWorkerVerdictA},
		{Body: "Real user feedback here"},
		{Body: "🔍 **HerdOS Agent Review**\nFindings"},
	}
	got := collectUserFeedbackComments(comments)
	assert.Equal(t, []string{"Real user feedback here"}, got)
	for _, c := range got {
		assert.NotContains(t, c, "Worker #", "worker verdicts must not be returned as user feedback")
	}
}

func TestReview_PassesWorkerNoOpVerdicts(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: sampleWorkerVerdictA},
		{Body: sampleWorkerVerdictB},
	}

	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.Len(t, capturedOpts.WorkerNoOpVerdicts, 2)
	assert.Equal(t, sampleWorkerVerdictA, capturedOpts.WorkerNoOpVerdicts[0])
	assert.Equal(t, sampleWorkerVerdictB, capturedOpts.WorkerNoOpVerdicts[1])
}

func TestReview_DetectsStableDisagreement(t *testing.T) {
	// Previous-cycle worker verdicts cover findings A and B; the fake
	// agent returns findings A, B, and C. A and B are blocked because they
	// match prior verdicts; C is genuinely new but is dropped on the floor
	// by design — when stable disagreement is detected the integrator halts
	// the entire cycle so the user can decide.
	findingA := "Missing nil check on FooBar value before dereference in src/foo.go"
	findingB := "Race condition in worker pool submit/read in src/pool.go"
	findingC := "Brand new finding the worker has never seen"

	verdictA := "**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- " + findingA + " — nil check is wrong; value is constructed three lines above.\n\nConclusion: already handled."
	verdictB := "**Worker #43 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- " + findingB + " — alleged race is impossible due to mutex coverage.\n\nConclusion: no race."

	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: verdictA},
		{Body: verdictB},
	}

	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 999, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mock := &mockPlatform{
		issues:     mockCreate,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: findingA},
				{Severity: "HIGH", Description: findingB},
				{Severity: "HIGH", Description: findingC},
			},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.StableDisagreement, "StableDisagreement must be true")
	assert.Equal(t, 2, result.FindingsCount, "FindingsCount should reflect blocked findings (A and B)")

	// No fix issue created, no worker dispatched.
	assert.Equal(t, 0, createdIssues, "stable-disagreement cycle must not create a fix issue")
	assert.Empty(t, wf.dispatched, "stable-disagreement cycle must not dispatch a fix worker")

	// herd/stable-disagreement label added to the batch PR.
	assert.Contains(t, issueSvc.addedLabels[50], issues.StableDisagreement)

	// Stable-disagreement comment posted with the expected header and content.
	var stableComment string
	for _, c := range prSvc.comments {
		if strings.HasPrefix(c, "⚠️ **Stable disagreement detected**") {
			stableComment = c
			break
		}
	}
	require.NotEmpty(t, stableComment, "expected a stable-disagreement comment")
	assert.Contains(t, stableComment, findingA)
	assert.Contains(t, stableComment, findingB)
	assert.NotContains(t, stableComment, findingC, "genuinely-new finding C is dropped on the floor by design")
}

func TestReview_NoStableDisagreementWhenAllNew(t *testing.T) {
	// Worker verdicts exist but no current finding matches any of them —
	// normal fix-issue creation should proceed.
	verdict := "**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- Some completely unrelated topic that no current finding mentions whatsoever\n\nConclusion: prior fix not needed."

	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: verdict},
	}

	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 999, Title: title}, nil
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Brand new finding A that's not in any verdict"},
				{Severity: "HIGH", Description: "Brand new finding B that's not in any verdict"},
			},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.StableDisagreement, "no verdict matches → no stable disagreement")
	assert.NotContains(t, issueSvc.addedLabels[50], issues.StableDisagreement, "label must not be added")
	assert.Equal(t, 1, createdIssues, "normal fix-issue creation must proceed")
	assert.NotEmpty(t, wf.dispatched, "fix worker must be dispatched")
}

func TestReview_NonConvergenceEscalatesToStrategyFixIssue(t *testing.T) {
	fx := newReviewNonConvergenceIntegrationFixture(t, reviewNonConvergenceCurrentFindings(28))

	result, err := Review(context.Background(), fx.mock, fx.ag, fx.g, fx.cfg, ReviewParams{PRNumber: 849, RepoRoot: fx.dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{9601}, result.FixIssues)
	assert.Equal(t, 39, result.FixCycle)
	assert.Equal(t, 1, result.FindingsCount)
	require.Len(t, fx.createdIssues, 1)
	assert.True(t, strings.HasPrefix(fx.createdIssues[0].title, "Review strategy fix"), "must create strategy issue, not normal review fixes")
	assert.NotContains(t, fx.createdIssues[0].title, "Review fixes")
	assert.Contains(t, fx.createdIssues[0].labels, issues.ReviewNonConverging)
	assert.Contains(t, fx.createdIssues[0].labels, issues.TypeFix)
	assert.Contains(t, fx.createdIssues[0].labels, issues.StatusInProgress)
	assert.Contains(t, fx.createdIssues[0].body, "Solve the shared architecture/design problem")
	assert.NotContains(t, fx.createdIssues[0].body, "/herd fix")
	require.Len(t, fx.wf.dispatched, 1)
	assert.Equal(t, "herd-worker.yml", fx.wf.dispatchedWorkflows[0])
	assert.Equal(t, "9601", fx.wf.dispatched[0]["issue_number"])
	assert.Equal(t, "herd/batch/111-batch", fx.wf.dispatched[0]["batch_branch"])

	comment := requireCommentContaining(t, fx.prSvc.comments, "Herd review is not converging")
	assert.Contains(t, comment, "Cycles analyzed: 34, 35, 36, 37, 38, 39")
	assert.Contains(t, comment, "Finding count trend: 14, 20, 21, 24, 28, 28")
	assert.Contains(t, comment, "Fix issues considered: #951, #952, #953, #954, #955")
	assert.Contains(t, comment, "Dominant package clusters: internal/controlplane/dispatch")
	assert.Contains(t, comment, "Dominant root-cause terms:")
	assert.Contains(t, comment, "idempotency")
	assert.Contains(t, comment, "Escalation reason:")
	assert.Contains(t, comment, "Strategy fix issue: #9601")
	assert.NotContains(t, strings.Join(fx.prSvc.comments, "\n"), "/herd fix")
	marker, ok := parseReviewResultMarker(comment)
	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, marker.Status)
	assert.Equal(t, 39, marker.Cycle)
	assert.Equal(t, 1, marker.FindingsCount)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, fx.prSvc.reviews[0].event)
	assert.Contains(t, fx.prSvc.reviews[0].body, "Strategy-level fix worker dispatched")
	assert.Contains(t, fx.prSvc.reviews[0].body, "#9601")
}

func TestReview_NonConvergenceEscalatesWithOlderHistoricalHeadSHAs(t *testing.T) {
	fx := newReviewNonConvergenceIntegrationFixture(t, reviewNonConvergenceCurrentFindings(28))
	fx.setHistoryWithHeadSHAs(t, []int{14, 20, 21, 24, 28}, []string{
		"older-head-34",
		"older-head-35",
		"older-head-36",
		"older-head-37",
		"older-head-38",
	})

	result, err := Review(context.Background(), fx.mock, fx.ag, fx.g, fx.cfg, ReviewParams{PRNumber: 849, RepoRoot: fx.dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{9601}, result.FixIssues)
	assert.Equal(t, 39, result.FixCycle)
	assert.Equal(t, 1, result.FindingsCount)
	require.Len(t, fx.createdIssues, 1)
	assert.True(t, strings.HasPrefix(fx.createdIssues[0].title, "Review strategy fix"))
	assert.NotContains(t, fx.createdIssues[0].title, "Review fixes")
	assert.Contains(t, fx.createdIssues[0].labels, issues.ReviewNonConverging)
	require.Len(t, fx.wf.dispatched, 1)
	assert.Equal(t, "9601", fx.wf.dispatched[0]["issue_number"])

	comment := requireCommentContaining(t, fx.prSvc.comments, "Herd review is not converging")
	assert.Contains(t, comment, "Cycles analyzed: 34, 35, 36, 37, 38, 39")
	assert.Contains(t, comment, "Finding count trend: 14, 20, 21, 24, 28, 28")
	marker, ok := parseReviewResultMarker(comment)
	require.True(t, ok)
	assert.Equal(t, fx.headSHA, marker.HeadSHA)
	assert.Equal(t, 39, marker.Cycle)
	assert.Empty(t, commentsContaining(fx.prSvc.comments, "Found 28 actionable issues"))
}

func TestReview_NonConvergenceContinueCreatesNormalReviewFixIssue(t *testing.T) {
	tests := []struct {
		name            string
		historyCounts   []int
		minCompleted    int
		currentFindings []agent.ReviewFinding
	}{
		{
			name:            "decreasing trend",
			historyCounts:   []int{28, 24, 21, 20, 14},
			minCompleted:    3,
			currentFindings: reviewNonConvergenceCurrentFindings(9),
		},
		{
			name:            "insufficient completed cycles",
			historyCounts:   []int{14, 20},
			minCompleted:    3,
			currentFindings: reviewNonConvergenceCurrentFindings(28),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newReviewNonConvergenceIntegrationFixture(t, tt.currentFindings)
			fx.cfg.Integrator.ReviewNonConvergence.MinCompletedCycles = tt.minCompleted
			fx.setHistory(t, tt.historyCounts)

			result, err := Review(context.Background(), fx.mock, fx.ag, fx.g, fx.cfg, ReviewParams{PRNumber: 849, RepoRoot: fx.dir})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, []int{9601}, result.FixIssues)
			require.Len(t, fx.createdIssues, 1)
			assert.Equal(t, fmt.Sprintf("Review fixes (cycle %d)", 34+len(tt.historyCounts)), fx.createdIssues[0].title)
			assert.NotContains(t, fx.createdIssues[0].labels, issues.ReviewNonConverging)
			require.Len(t, fx.wf.dispatched, 1)
			assert.Equal(t, "9601", fx.wf.dispatched[0]["issue_number"])
			assert.Empty(t, commentsContaining(fx.prSvc.comments, "Herd review is not converging"))
		})
	}
}

func TestReview_NonConvergenceDuplicateStrategyIssueDoesNotCreateOrDispatch(t *testing.T) {
	currentFindings := reviewNonConvergenceCurrentFindings(28)
	fx := newReviewNonConvergenceIntegrationFixture(t, currentFindings)
	analysis := fx.analysisForCurrent(t, currentFindings)
	duplicateBody := appendReviewNonConvergenceFingerprint(issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1, Batch: 111, Type: "fix", BatchPR: 849},
		Task:        "Existing strategy fix.",
	}), analysis.Cluster.Fingerprint)
	fx.issueSvc.listResult = append(fx.issueSvc.listResult, &platform.Issue{
		Number: 9700,
		State:  "open",
		Title:  buildStrategyFixIssueTitle(39, analysis.Cluster),
		Labels: []string{issues.ReviewNonConverging},
		Body:   duplicateBody,
	})

	result, err := Review(context.Background(), fx.mock, fx.ag, fx.g, fx.cfg, ReviewParams{PRNumber: 849, RepoRoot: fx.dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{9700}, result.FixIssues)
	assert.Equal(t, 39, result.FixCycle)
	assert.Equal(t, 1, result.FindingsCount)
	assert.Empty(t, fx.createdIssues)
	assert.Empty(t, fx.wf.dispatched)
	comment := requireCommentContaining(t, fx.prSvc.comments, "already being addressed by strategy issue #9700")
	assert.Contains(t, comment, "Strategy fix issue: #9700")
	assert.NotContains(t, strings.Join(fx.prSvc.comments, "\n"), "/herd fix")
	marker, ok := parseReviewResultMarker(comment)
	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, marker.Status)
	assert.Equal(t, 1, marker.FindingsCount)
	require.Len(t, fx.prSvc.reviews, 1)
	assert.Contains(t, fx.prSvc.reviews[0].body, "already in progress")
}

func TestReview_NonConvergenceDisabledUsesNormalReviewFixIssue(t *testing.T) {
	fx := newReviewNonConvergenceIntegrationFixture(t, reviewNonConvergenceCurrentFindings(28))
	fx.cfg.Integrator.ReviewNonConvergence.Enabled = false

	result, err := Review(context.Background(), fx.mock, fx.ag, fx.g, fx.cfg, ReviewParams{PRNumber: 849, RepoRoot: fx.dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int{9601}, result.FixIssues)
	require.Len(t, fx.createdIssues, 1)
	assert.Equal(t, "Review fixes (cycle 39)", fx.createdIssues[0].title)
	assert.NotContains(t, fx.createdIssues[0].labels, issues.ReviewNonConverging)
	require.Len(t, fx.wf.dispatched, 1)
	assert.Empty(t, commentsContaining(fx.prSvc.comments, "Herd review is not converging"))
}

func TestReview_BlockedByStableDisagreementLabel(t *testing.T) {
	// PR has the StableDisagreement label and params.Manual is false —
	// Review must early-return without calling the agent.
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main", Labels: []string{issues.StableDisagreement}},
		},
	}
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch"},
		},
	}

	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			return &platform.Issue{Number: 999, Title: title}, nil
		},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "Should never be evaluated"}},
		},
	}

	mock := &mockPlatform{
		issues:     mockCreate,
		prs:        prSvc,
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: msSvc,
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir, Manual: false})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.Equal(t, 0, ag.calls, "agent must NOT be called when StableDisagreement label blocks the review")
	assert.Equal(t, 0, createdIssues, "no fix issue must be created when blocked by label")
	assert.False(t, result.Approved)
	assert.False(t, result.StableDisagreement, "blocked-by-label is a different state than detection")
}

func TestReview_ManualBypassesStableDisagreementLabel(t *testing.T) {
	// Same setup as TestReview_BlockedByStableDisagreementLabel but
	// params.Manual = true. The agent IS called and the rest of the flow
	// proceeds.
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main", Labels: []string{issues.StableDisagreement}},
			},
		},
	}
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch"},
		},
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: msSvc,
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir, Manual: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, ag.calls, "agent MUST be called when Manual=true bypasses the label")
	assert.True(t, result.Approved)
}

func TestSummarizeVerdict(t *testing.T) {
	longBullet := strings.Repeat("a", 250)
	tests := []struct {
		name    string
		verdict string
		want    string
	}{
		{
			name:    "first bullet returned",
			verdict: "**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- first bullet text\n- second bullet text",
			want:    "first bullet text",
		},
		{
			name:    "conclusion line fallback",
			verdict: "**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\nNo bullets but a conclusion.\n\nConclusion: nothing to fix here",
			want:    "nothing to fix here",
		},
		{
			name:    "neither bullet nor conclusion returns empty",
			verdict: "**Worker #42 — no-op verdict**\n\nJust some prose with no recognizable structure",
			want:    "",
		},
		{
			name:    "long bullet truncated with ellipsis",
			verdict: "**Worker #42 — no-op verdict**\n\n- " + longBullet,
			want:    strings.Repeat("a", 200) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeVerdict(tt.verdict)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildStableDisagreementComment(t *testing.T) {
	blocked := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "First blocked description"},
		{Severity: "HIGH", Description: "Second blocked description"},
	}
	verdicts := []string{
		"**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- first verdict bullet",
		"**Worker #43 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- second verdict bullet",
	}
	verdictIdx := []int{0, 1}

	got := buildStableDisagreementComment(blocked, verdictIdx, verdicts)

	assert.Contains(t, got, "⚠️ **Stable disagreement detected**")
	assert.Contains(t, got, "First blocked description")
	assert.Contains(t, got, "Second blocked description")
	assert.Contains(t, got, "first verdict bullet")
	assert.Contains(t, got, "second verdict bullet")
	assert.Contains(t, got, "herd/stable-disagreement")
	// All three numbered resolution options must be present.
	assert.Contains(t, got, "1. ")
	assert.Contains(t, got, "2. ")
	assert.Contains(t, got, "3. ")
}

type reviewNonConvergenceIntegrationFixture struct {
	dir           string
	g             *git.Git
	cfg           *config.Config
	mock          *mockPlatform
	issueSvc      *mockIssueService
	prSvc         *mockCapturingPRService
	wf            *mockWorkflowService
	ag            *mockReviewAgent
	headSHA       string
	createdIssues []createdReviewIssue
}

type createdReviewIssue struct {
	title  string
	body   string
	labels []string
}

func newReviewNonConvergenceIntegrationFixture(t *testing.T, currentFindings []agent.ReviewFinding) *reviewNonConvergenceIntegrationFixture {
	t.Helper()
	dir, g := initTestRepo(t)
	runReviewTestGit(t, dir, "checkout", "-b", "herd/batch/111-batch")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal", "controlplane", "dispatch"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "internal", "controlplane", "dispatch", "review.go"), []byte("package dispatch\n\nfunc Review() {}\n"), 0644))
	runReviewTestGit(t, dir, "add", "internal/controlplane/dispatch/review.go")
	runReviewTestGit(t, dir, "commit", "-m", "batch review change")
	headSHA := reviewTestGitOutput(t, dir, "rev-parse", "HEAD")

	issueSvc := newMockIssueService()
	fx := &reviewNonConvergenceIntegrationFixture{
		dir:      dir,
		g:        g,
		issueSvc: issueSvc,
		headSHA:  headSHA,
	}
	fx.setHistory(t, []int{14, 20, 21, 24, 28})

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			getResult: map[int]*platform.PullRequest{
				849: {Number: 849, Title: "[herd] Batch 111", Head: "herd/batch/111-batch", Base: "main"},
			},
		},
	}
	wf := &mockWorkflowService{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			fx.createdIssues = append(fx.createdIssues, createdReviewIssue{
				title:  title,
				body:   body,
				labels: append([]string(nil), labels...),
			})
			return &platform.Issue{Number: 9601, Title: title, Body: body, Labels: labels}, nil
		},
	}
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.workflows = wf
	mock.repo = &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/111-batch": true},
		branchSHAs:    map[string]string{"herd/batch/111-batch": headSHA},
	}
	mock.milestones = &mockMilestoneService{getResult: map[int]*platform.Milestone{
		111: {Number: 111, Title: "Batch"},
	}}
	cfg := config.Default()
	cfg.Integrator.ReviewMaxFixCycles = 100
	cfg.Integrator.ReviewFixSeverity = "low"

	fx.cfg = cfg
	fx.mock = mock
	fx.prSvc = prSvc
	fx.wf = wf
	fx.ag = &mockReviewAgent{reviewResult: &agent.ReviewResult{
		Approved: false,
		Summary:  "review findings",
		Findings: currentFindings,
	}}
	return fx
}

func (fx *reviewNonConvergenceIntegrationFixture) setHistory(t *testing.T, counts []int) {
	t.Helper()
	headSHAs := make([]string, len(counts))
	for i := range counts {
		headSHAs[i] = fx.headSHA
	}
	fx.setHistoryWithHeadSHAs(t, counts, headSHAs)
}

func (fx *reviewNonConvergenceIntegrationFixture) setHistoryWithHeadSHAs(t *testing.T, counts []int, headSHAs []string) {
	t.Helper()
	require.Len(t, headSHAs, len(counts))
	fx.issueSvc.listCommentsResult = nil
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	for i, count := range counts {
		cycle := 34 + i
		fixIssue := 951 + i
		finding := fmt.Sprintf("internal/controlplane/dispatch/cycle_%d.go: durable mutation lacks idempotency before started workflow retry", cycle)
		fx.issueSvc.listCommentsResult = append(fx.issueSvc.listCommentsResult, reviewHistoryComment(t, headSHAs[i], cycle, count, finding, fixIssue))
		fx.issueSvc.listResult = append(fx.issueSvc.listResult, reviewFixIssue(fixIssue, cycle, issues.StatusDone, []string{fmt.Sprintf("internal/controlplane/dispatch/cycle_%d.go", cycle)}, "Validation success"))
	}
}

func (fx *reviewNonConvergenceIntegrationFixture) analysisForCurrent(t *testing.T, currentFindings []agent.ReviewFinding) reviewConvergenceAnalysis {
	t.Helper()
	high, medium, low, criteria := filterFindingsBySeverity(currentFindings)
	history := collectReviewHistoryFromComments(fx.issueSvc.listCommentsResult, fx.issueSvc.listResult, 849, 111, fx.headSHA, fx.cfg.Integrator.ReviewNonConvergence.Window)
	history = appendCurrentReviewHistoryCycleIfMissing(history, 39, fx.headSHA, currentFindings, currentFindings, high, medium, low, criteria)
	return analyzeReviewConvergence(history, fx.cfg.Integrator.ReviewNonConvergence.MinCompletedCycles)
}

func reviewNonConvergenceCurrentFindings(count int) []agent.ReviewFinding {
	findings := make([]agent.ReviewFinding, 0, count)
	for i := 1; i <= count; i++ {
		severity := "MEDIUM"
		if i <= 9 {
			severity = "HIGH"
		}
		findings = append(findings, agent.ReviewFinding{
			Severity:    severity,
			Description: fmt.Sprintf("internal/controlplane/dispatch/current_%02d.go: durable mutation lacks idempotency before started workflow retry", i),
		})
	}
	return findings
}

func commentsContaining(comments []string, needle string) []string {
	var out []string
	for _, comment := range comments {
		if strings.Contains(comment, needle) {
			out = append(out, comment)
		}
	}
	return out
}
