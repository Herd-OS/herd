package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Minimal Platform mock used only by Fetch tests. ---

type fakePlatform struct {
	issues     platform.IssueService
	prs        platform.PullRequestService
	workflows  platform.WorkflowService
	milestones platform.MilestoneService
	checks     platform.CheckService
}

func (f *fakePlatform) Issues() platform.IssueService            { return f.issues }
func (f *fakePlatform) PullRequests() platform.PullRequestService { return f.prs }
func (f *fakePlatform) Workflows() platform.WorkflowService      { return f.workflows }
func (f *fakePlatform) Labels() platform.LabelService            { return nil }
func (f *fakePlatform) Milestones() platform.MilestoneService    { return f.milestones }
func (f *fakePlatform) Runners() platform.RunnerService          { return nil }
func (f *fakePlatform) Repository() platform.RepositoryService   { return nil }
func (f *fakePlatform) Checks() platform.CheckService            { return f.checks }

type fakeIssueService struct {
	getResult    map[int]*platform.Issue
	getCalls     int
	listResult   []*platform.Issue
	listByFilter func(filters platform.IssueFilters) ([]*platform.Issue, error)
	listErr      error
}

func (s *fakeIssueService) Create(ctx context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeIssueService) Get(ctx context.Context, number int) (*platform.Issue, error) {
	s.getCalls++
	if iss, ok := s.getResult[number]; ok {
		return iss, nil
	}
	return nil, errors.New("not found")
}
func (s *fakeIssueService) List(ctx context.Context, filters platform.IssueFilters) ([]*platform.Issue, error) {
	if s.listByFilter != nil {
		return s.listByFilter(filters)
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listResult, nil
}
func (s *fakeIssueService) Update(ctx context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeIssueService) AddLabels(ctx context.Context, number int, labels []string) error {
	return nil
}
func (s *fakeIssueService) RemoveLabels(ctx context.Context, number int, labels []string) error {
	return nil
}
func (s *fakeIssueService) AddComment(ctx context.Context, number int, body string) error { return nil }
func (s *fakeIssueService) AddCommentReturningID(ctx context.Context, number int, body string) (int64, error) {
	return 0, nil
}
func (s *fakeIssueService) UpdateComment(ctx context.Context, commentID int64, body string) error {
	return nil
}
func (s *fakeIssueService) DeleteComment(ctx context.Context, commentID int64) error { return nil }
func (s *fakeIssueService) ListComments(ctx context.Context, number int) ([]*platform.Comment, error) {
	return nil, nil
}
func (s *fakeIssueService) CreateCommentReaction(ctx context.Context, commentID int64, reaction string) error {
	return nil
}

type fakePRService struct {
	listResult []*platform.PullRequest
	listErr    error
}

func (s *fakePRService) Create(ctx context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	return nil, errors.New("not implemented")
}
func (s *fakePRService) Get(ctx context.Context, number int) (*platform.PullRequest, error) {
	return nil, errors.New("not implemented")
}
func (s *fakePRService) List(ctx context.Context, filters platform.PRFilters) ([]*platform.PullRequest, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listResult, nil
}
func (s *fakePRService) Update(ctx context.Context, number int, title, body *string) (*platform.PullRequest, error) {
	return nil, errors.New("not implemented")
}
func (s *fakePRService) Merge(ctx context.Context, number int, method platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, errors.New("not implemented")
}
func (s *fakePRService) UpdateBranch(ctx context.Context, number int) error { return nil }
func (s *fakePRService) CreateReview(ctx context.Context, number int, body string, event platform.ReviewEvent) error {
	return nil
}
func (s *fakePRService) AddComment(ctx context.Context, number int, body string) error { return nil }
func (s *fakePRService) ListReviewComments(ctx context.Context, number int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (s *fakePRService) GetDiff(ctx context.Context, number int) (string, error) { return "", nil }
func (s *fakePRService) Close(ctx context.Context, number int) error             { return nil }

type fakeWorkflowService struct {
	listRunsResult []*platform.Run
	listRunsErr    error
}

func (s *fakeWorkflowService) GetWorkflow(ctx context.Context, filename string) (int64, error) {
	return 0, nil
}
func (s *fakeWorkflowService) Dispatch(ctx context.Context, workflowFile, ref string, inputs map[string]string) (*platform.Run, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeWorkflowService) GetRun(ctx context.Context, runID int64) (*platform.Run, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeWorkflowService) ListRuns(ctx context.Context, filters platform.RunFilters) ([]*platform.Run, error) {
	if s.listRunsErr != nil {
		return nil, s.listRunsErr
	}
	return s.listRunsResult, nil
}
func (s *fakeWorkflowService) CancelRun(ctx context.Context, runID int64) error { return nil }

type fakeMilestoneService struct {
	listResult []*platform.Milestone
	listErr    error
}

func (s *fakeMilestoneService) Create(ctx context.Context, title, description string, dueDate *time.Time) (*platform.Milestone, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeMilestoneService) Get(ctx context.Context, number int) (*platform.Milestone, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeMilestoneService) List(ctx context.Context) ([]*platform.Milestone, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listResult, nil
}
func (s *fakeMilestoneService) Update(ctx context.Context, number int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, errors.New("not implemented")
}

type fakeCheckService struct {
	status string
	err    error
}

func (s *fakeCheckService) GetCombinedStatus(ctx context.Context, ref string) (string, error) {
	return s.status, s.err
}
func (s *fakeCheckService) RerunFailedChecks(ctx context.Context, ref string) error { return nil }

// --- Tests ---

func TestFetchState_PartialFailure(t *testing.T) {
	now := time.Now()
	milestone := &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"}

	mile1 := 1
	issuesList := []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{"herd/type:feature", issues.StatusDone}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
		{Number: 11, Title: "Task B", Labels: []string{"herd/type:feature", issues.StatusInProgress}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil && *filters.Milestone == 1 {
				return issuesList, nil
			}
			// "list failures" call: state=all, labels=[StatusFailed]
			return nil, nil
		},
	}

	platformMock := &fakePlatform{
		issues: issueSvc,
		prs:    &fakePRService{listResult: nil},
		workflows: &fakeWorkflowService{
			listRunsErr: errors.New("workflows down"),
		},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{status: ""},
	}

	state, errStr := Fetch(context.Background(), platformMock, "owner", "repo")

	assert.NotEmpty(t, errStr, "expected non-empty error string from partial failure")
	assert.Contains(t, errStr, "list worker runs")
	assert.Empty(t, state.Workers, "no workers should be populated when ListRuns fails")
	require.Len(t, state.Batches, 1, "milestone-derived batch should still be populated")
	assert.Equal(t, 1, state.Batches[0].MilestoneNumber)
	assert.Equal(t, "Test Batch", state.Batches[0].MilestoneTitle)
	assert.Equal(t, 1, state.Batches[0].Done)
	assert.Equal(t, 1, state.Batches[0].InProgress)
	assert.Equal(t, "owner", state.Owner)
	assert.Equal(t, "repo", state.Repo)
	assert.False(t, state.LastRefresh.IsZero())
	// MilestoneURL should be populated using owner/repo, not empty placeholders.
	assert.Equal(t, "https://github.com/owner/repo/milestone/1", state.Batches[0].MilestoneURL)
}

func TestFetch_PopulatesWorkersFromRuns(t *testing.T) {
	now := time.Now()
	runs := []*platform.Run{
		{ID: 100, URL: "https://example/run/100", CreatedAt: now, Inputs: map[string]string{"issue_number": "42"}},
		{ID: 101, URL: "https://example/run/101", CreatedAt: now, Inputs: map[string]string{}},
	}

	issueSvc := &fakeIssueService{
		getResult: map[int]*platform.Issue{
			42: {Number: 42, Title: "Important task"},
		},
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{listRunsResult: runs},
		milestones: &fakeMilestoneService{listResult: nil},
		checks:     &fakeCheckService{},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Workers, 2)
	assert.Equal(t, int64(100), state.Workers[0].RunID)
	assert.Equal(t, 42, state.Workers[0].IssueNumber)
	assert.Equal(t, "Important task", state.Workers[0].IssueTitle)
	assert.Equal(t, int64(101), state.Workers[1].RunID)
	assert.Equal(t, 0, state.Workers[1].IssueNumber)
	assert.Equal(t, "", state.Workers[1].IssueTitle)
}

func TestFetch_RecentFailures(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	failedIssues := []*platform.Issue{
		{Number: 1, Title: "old failure", Labels: []string{"herd/type:feature", issues.StatusFailed}, UpdatedAt: old},
		{Number: 2, Title: "recent failure", Labels: []string{"herd/type:bugfix", issues.StatusFailed}, UpdatedAt: recent},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil {
				return nil, nil
			}
			// failures listing
			for _, l := range filters.Labels {
				if l == issues.StatusFailed {
					return failedIssues, nil
				}
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{listRunsResult: nil},
		milestones: &fakeMilestoneService{listResult: nil},
		checks:     &fakeCheckService{},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Failures, 1, "only failures within 24h should be included")
	assert.Equal(t, 2, state.Failures[0].Number)
	assert.Equal(t, "recent failure", state.Failures[0].Title)
	assert.Equal(t, "herd/type:bugfix", state.Failures[0].Label)
}

func TestFetch_BatchWithPRAndCI(t *testing.T) {
	now := time.Now()
	mile1 := 7
	milestone := &platform.Milestone{Number: 7, Title: "Auth flow", State: "open"}
	issuesList := []*platform.Issue{
		{Number: 70, Labels: []string{"herd/type:feature", issues.StatusFailed}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}
	pr := &platform.PullRequest{Number: 200, URL: "https://example/pr/200", Head: "herd/batch/7-auth-flow"}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil && *filters.Milestone == 7 {
				return issuesList, nil
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{listResult: []*platform.PullRequest{pr}},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{status: "failure"},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Batches, 1)
	be := state.Batches[0]
	assert.Equal(t, 200, be.PRNumber)
	assert.Equal(t, "https://example/pr/200", be.PRURL)
	assert.Equal(t, "failure", be.CIStatus)
	assert.True(t, be.HasAttention, "attention should be set when CI failed or issue failed")
	assert.Equal(t, 1, be.Failed)
}

func TestFetch_SkipsMilestonesWithoutHerdLabels(t *testing.T) {
	now := time.Now()
	mile1 := 9
	milestone := &platform.Milestone{Number: 9, Title: "Plain", State: "open"}
	issuesList := []*platform.Issue{
		{Number: 1, Labels: []string{"bug"}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil {
				return issuesList, nil
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	assert.Empty(t, state.Batches, "milestones without herd/* issues should be skipped")
}

func TestFetch_SkipsClosedMilestones(t *testing.T) {
	closed := &platform.Milestone{Number: 1, Title: "Closed", State: "closed"}
	platformMock := &fakePlatform{
		issues:     &fakeIssueService{listByFilter: func(platform.IssueFilters) ([]*platform.Issue, error) { return nil, nil }},
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{closed}},
		checks:     &fakeCheckService{},
	}
	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	assert.Empty(t, state.Batches)
}

func TestFetch_PopulatesIssueNumberFromRunInputs(t *testing.T) {
	now := time.Now()
	runs := []*platform.Run{
		{ID: 100, URL: "https://example/run/100", CreatedAt: now, Inputs: map[string]string{"issue_number": "42"}},
	}
	issueSvc := &fakeIssueService{
		getResult: map[int]*platform.Issue{
			42: {Number: 42, Title: "Add auth"},
		},
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) { return nil, nil },
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{listRunsResult: runs},
		milestones: &fakeMilestoneService{listResult: nil},
		checks:     &fakeCheckService{},
	}
	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Workers, 1)
	assert.Equal(t, 42, state.Workers[0].IssueNumber)
	assert.Equal(t, "Add auth", state.Workers[0].IssueTitle)
}

func TestFetch_HandlesMissingIssueNumber(t *testing.T) {
	now := time.Now()
	runs := []*platform.Run{
		{ID: 101, URL: "https://example/run/101", CreatedAt: now, Inputs: map[string]string{}},
	}
	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) { return nil, nil },
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{},
		workflows:  &fakeWorkflowService{listRunsResult: runs},
		milestones: &fakeMilestoneService{listResult: nil},
		checks:     &fakeCheckService{},
	}
	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Workers, 1)
	assert.Equal(t, 0, state.Workers[0].IssueNumber)
	assert.Equal(t, "", state.Workers[0].IssueTitle)
	assert.Equal(t, 0, issueSvc.getCalls, "Issues().Get should not be called when IssueNumber is 0")
}

func TestBuildBatchEntry_SetsCascadeFailedFromPRLabel(t *testing.T) {
	now := time.Now()
	mile1 := 12
	milestone := &platform.Milestone{Number: 12, Title: "Auth", State: "open"}
	issuesList := []*platform.Issue{
		{Number: 120, Labels: []string{"herd/type:feature", issues.StatusDone}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}
	pr := &platform.PullRequest{
		Number: 300,
		URL:    "https://example/pr/300",
		Head:   "herd/batch/12-auth",
		Labels: []string{issues.CascadeFailed},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil && *filters.Milestone == 12 {
				return issuesList, nil
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{listResult: []*platform.PullRequest{pr}},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{status: "success"},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Batches, 1)
	be := state.Batches[0]
	assert.True(t, be.CascadeFailed, "CascadeFailed should be set when PR carries herd/cascade-failed label")
	assert.True(t, be.HasAttention, "HasAttention should follow CascadeFailed")
}

func TestFetch_PopulatesStableDisagreement(t *testing.T) {
	now := time.Now()
	mile1 := 14
	milestone := &platform.Milestone{Number: 14, Title: "Disagree", State: "open"}
	issuesList := []*platform.Issue{
		{Number: 140, Labels: []string{"herd/type:feature", issues.StatusDone}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}
	pr := &platform.PullRequest{
		Number: 400,
		URL:    "https://example/pr/400",
		Head:   "herd/batch/14-disagree",
		Labels: []string{issues.StableDisagreement},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil && *filters.Milestone == 14 {
				return issuesList, nil
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{listResult: []*platform.PullRequest{pr}},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{status: "success"},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Batches, 1)
	be := state.Batches[0]
	assert.True(t, be.StableDisagreement, "StableDisagreement should be set when PR carries the label")
	assert.True(t, be.HasAttention, "HasAttention should follow StableDisagreement")
}

func TestBuildBatchEntry_NoPRCascadeFailedStaysFalse(t *testing.T) {
	now := time.Now()
	mile1 := 13
	milestone := &platform.Milestone{Number: 13, Title: "NoPR", State: "open"}
	issuesList := []*platform.Issue{
		{Number: 130, Labels: []string{"herd/type:feature", issues.StatusDone}, UpdatedAt: now, Milestone: &platform.Milestone{Number: mile1}},
	}

	issueSvc := &fakeIssueService{
		listByFilter: func(filters platform.IssueFilters) ([]*platform.Issue, error) {
			if filters.Milestone != nil && *filters.Milestone == 13 {
				return issuesList, nil
			}
			return nil, nil
		},
	}
	platformMock := &fakePlatform{
		issues:     issueSvc,
		prs:        &fakePRService{listResult: nil},
		workflows:  &fakeWorkflowService{},
		milestones: &fakeMilestoneService{listResult: []*platform.Milestone{milestone}},
		checks:     &fakeCheckService{},
	}

	state, errStr := Fetch(context.Background(), platformMock, "o", "r")
	assert.Empty(t, errStr)
	require.Len(t, state.Batches, 1)
	be := state.Batches[0]
	assert.False(t, be.CascadeFailed, "a batch with no PR cannot be cascade-failed")
	assert.Equal(t, 0, be.PRNumber)
}

func TestPrimaryTypeLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"none", []string{"bug", "good first issue"}, ""},
		{"feature", []string{"herd/status:ready", "herd/type:feature"}, "herd/type:feature"},
		{"bugfix wins over status", []string{"herd/type:bugfix"}, "herd/type:bugfix"},
		{"empty list", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, primaryTypeLabel(tt.labels))
		})
	}
}
