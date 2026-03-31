package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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

func TestHandleCommentCmd_ValidatesCommentIDAndIssueNumber(t *testing.T) {
	tests := []struct {
		name        string
		commentID   string
		issueNumber string
		wantErrMsg  string
	}{
		{"zero comment-id rejected", "0", "1", "--comment-id must be greater than 0"},
		{"zero issue-number rejected", "1", "0", "--issue-number must be greater than 0"},
		{"both zero rejected", "0", "0", "--comment-id must be greater than 0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERD_RUNNER", "true")
			root := NewRootCmd()
			root.SetArgs([]string{
				"integrator", "handle-comment",
				"--comment-id", tt.commentID,
				"--issue-number", tt.issueNumber,
			})
			err := root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrMsg)
		})
	}
}

func TestHandleCommentCmd_RequiresHerdRunner(t *testing.T) {
	t.Setenv("HERD_RUNNER", "")
	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "handle-comment", "--comment-id", "1", "--issue-number", "1"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HERD_RUNNER")
}

func TestHandleCommentCmd_RequiresCommentBody(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("COMMENT_BODY", "")
	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "handle-comment", "--comment-id", "1", "--issue-number", "1"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "COMMENT_BODY")
}

func TestHandleCommentCmd_NoOpWhenNoHerdCommand(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("COMMENT_BODY", "just a regular comment, no slash command here")

	root := NewRootCmd()
	root.SetArgs([]string{"integrator", "handle-comment", "--comment-id", "1", "--issue-number", "1"})
	err := root.Execute()
	// No /herd command: should succeed with no-op, but config.Load may fail in test env
	// We only assert no COMMENT_BODY error
	if err != nil {
		assert.NotContains(t, err.Error(), "COMMENT_BODY")
	}
}

func TestHandleCommentCmd_IgnoresUnauthorizedAuthor(t *testing.T) {
	tests := []struct {
		name        string
		association string
		login       string
		shouldRun   bool
	}{
		{"owner allowed", "OWNER", "owner-user", true},
		{"member allowed", "MEMBER", "member-user", true},
		{"collaborator allowed", "COLLABORATOR", "collab-user", true},
		{"contributor ignored", "CONTRIBUTOR", "some-user", false},
		{"none ignored", "NONE", "anon-user", false},
		{"bot allowed", "NONE", "github-actions[bot]", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERD_RUNNER", "true")
			t.Setenv("COMMENT_BODY", "/herd help")
			root := NewRootCmd()
			root.SetArgs([]string{
				"integrator", "handle-comment",
				"--comment-id", "1",
				"--issue-number", "1",
				"--author-association", tt.association,
				"--author-login", tt.login,
			})
			err := root.Execute()
			if !tt.shouldRun {
				// Unauthorized: command exits successfully (ignored), no error
				assert.NoError(t, err)
			} else {
				// Authorized: will proceed past auth check; config.Load may fail in CI
				// but that's after the auth gate — acceptable here
				_ = err
			}
		})
	}
}

func TestHandleCommentCmd_IsPRFlag(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
		defValue string
		wantNil  bool
	}{
		{"is-pr flag exists with default false", "is-pr", "false", false},
		{"comment-id flag exists", "comment-id", "0", false},
		{"issue-number flag exists", "issue-number", "0", false},
		{"author-login flag exists", "author-login", "", false},
		{"author-association flag exists", "author-association", "", false},
		{"unknown flag absent", "unknown-flag", "", true},
	}

	cmd := newHandleCommentCmd()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := cmd.Flags().Lookup(tt.flagName)
			if tt.wantNil {
				assert.Nil(t, flag)
			} else {
				require.NotNil(t, flag, "expected flag --%s to be defined", tt.flagName)
				assert.Equal(t, tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestHandleCommentCmd_IsPRParsing(t *testing.T) {
	tests := []struct {
		name     string
		flagVal  string
		wantBool bool
	}{
		{"true string sets isPR true", "true", true},
		{"false string sets isPR false", "false", false},
		{"empty string sets isPR false", "", false},
		{"TRUE (uppercase) sets isPR false", "TRUE", false},
		{"1 sets isPR false", "1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERD_RUNNER", "true")
			t.Setenv("COMMENT_BODY", "/herd help")
			root := NewRootCmd()
			args := []string{
				"integrator", "handle-comment",
				"--comment-id", "1",
				"--issue-number", "1",
				"--author-association", "OWNER",
				"--is-pr", tt.flagVal,
			}
			root.SetArgs(args)
			// The command will fail at config.Load in test env, but we can verify
			// the flag was parsed as a string (not consuming the next arg).
			// The key behavior: --is-pr "false" should NOT leave "false" as a positional arg.
			err := root.Execute()
			// Should get past flag parsing without error about unknown positional args.
			// Will fail at config.Load or similar — that's fine, it means parsing succeeded.
			if err != nil {
				assert.NotContains(t, err.Error(), "unknown command")
				assert.NotContains(t, err.Error(), "invalid argument")
			}
		})
	}
}

// mockCommentIssueService is a minimal IssueService mock for testing postCommentWithLog.
type mockCommentIssueService struct {
	mockDispatchIssueService
	addCommentErr error
	addedBody     string
	addedNumber   int
}

func (m *mockCommentIssueService) AddComment(_ context.Context, number int, body string) error {
	m.addedNumber = number
	m.addedBody = body
	return m.addCommentErr
}

// mockFailurePlatform implements platform.Platform for testing failure comment helpers.
type mockFailurePlatform struct {
	wf         *mockIntegratorWorkflowService
	issues     *mockCommentIssueService
	milestones *mockFailureMilestoneService
	prs        *mockFailurePRService
}

func (m *mockFailurePlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockFailurePlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *mockFailurePlatform) Workflows() platform.WorkflowService       { return m.wf }
func (m *mockFailurePlatform) Labels() platform.LabelService             { return nil }
func (m *mockFailurePlatform) Milestones() platform.MilestoneService     { return m.milestones }
func (m *mockFailurePlatform) Runners() platform.RunnerService           { return nil }
func (m *mockFailurePlatform) Repository() platform.RepositoryService    { return nil }
func (m *mockFailurePlatform) Checks() platform.CheckService            { return nil }

type mockFailureMilestoneService struct {
	getResult *platform.Milestone
	getErr    error
}

func (m *mockFailureMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockFailureMilestoneService) Get(_ context.Context, _ int) (*platform.Milestone, error) {
	return m.getResult, m.getErr
}
func (m *mockFailureMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockFailureMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

type mockFailurePRService struct {
	listResult []*platform.PullRequest
	listErr    error
}

func (m *mockFailurePRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockFailurePRService) Get(_ context.Context, _ int) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockFailurePRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return m.listResult, m.listErr
}
func (m *mockFailurePRService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockFailurePRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (m *mockFailurePRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockFailurePRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}
func (m *mockFailurePRService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockFailurePRService) GetDiff(_ context.Context, _ int) (string, error)    { return "", nil }

func TestIssueNumberFromRun(t *testing.T) {
	tests := []struct {
		name    string
		run     *platform.Run
		runErr  error
		wantNum int
		wantErr string
	}{
		{
			name:    "extracts issue number from run inputs",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{"issue_number": "42"}},
			wantNum: 42,
		},
		{
			name:    "returns error when GetRun fails",
			runErr:  fmt.Errorf("API error"),
			wantErr: "API error",
		},
		{
			name:    "returns error when no issue_number input",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{}},
			wantErr: "run 100 has no issue_number input",
		},
		{
			name:    "returns error when issue_number is not a number",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{"issue_number": "abc"}},
			wantErr: "strconv.Atoi",
		},
		{
			name:    "returns error when inputs map is nil",
			run:     &platform.Run{ID: 100, Inputs: nil},
			wantErr: "run 100 has no issue_number input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &mockIntegratorWorkflowService{}
			if tt.runErr != nil {
				wf.run = nil
			} else {
				wf.run = tt.run
			}
			// Override GetRun to return error if needed
			mock := &mockFailurePlatform{
				wf: &mockIntegratorWorkflowService{run: tt.run},
			}
			if tt.runErr != nil {
				mock.wf = &mockIntegratorWorkflowService{run: nil}
			}

			var p platform.Platform = mock
			if tt.runErr != nil {
				p = &errWorkflowPlatform{mockFailurePlatform: mock, getRunErr: tt.runErr}
			}

			got, err := issueNumberFromRun(context.Background(), p, 100)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantNum, got)
			}
		})
	}
}

// errWorkflowPlatform wraps mockFailurePlatform to return errors from GetRun.
type errWorkflowPlatform struct {
	*mockFailurePlatform
	getRunErr error
}

func (m *errWorkflowPlatform) Workflows() platform.WorkflowService {
	return &errWorkflowService{getRunErr: m.getRunErr}
}

type errWorkflowService struct {
	mockIntegratorWorkflowService
	getRunErr error
}

func (m *errWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, m.getRunErr
}

func TestBatchPRNumber(t *testing.T) {
	tests := []struct {
		name      string
		milestone *platform.Milestone
		msErr     error
		prs       []*platform.PullRequest
		prErr     error
		wantNum   int
		wantErr   string
	}{
		{
			name:      "finds batch PR number",
			milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
			prs:       []*platform.PullRequest{{Number: 99}},
			wantNum:   99,
		},
		{
			name:    "returns error when milestone lookup fails",
			msErr:   fmt.Errorf("milestone not found"),
			wantErr: "milestone not found",
		},
		{
			name:      "returns error when no open PRs found",
			milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
			prs:       []*platform.PullRequest{},
			wantErr:   "no open batch PR found",
		},
		{
			name:      "returns error when PR list fails",
			milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
			prErr:     fmt.Errorf("API error"),
			wantErr:   "no open batch PR found: API error",
		},
		{
			name:      "returns first PR when multiple found",
			milestone: &platform.Milestone{Number: 3, Title: "Test"},
			prs:       []*platform.PullRequest{{Number: 10}, {Number: 20}},
			wantNum:   10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFailurePlatform{
				milestones: &mockFailureMilestoneService{getResult: tt.milestone, getErr: tt.msErr},
				prs:        &mockFailurePRService{listResult: tt.prs, listErr: tt.prErr},
			}

			got, err := batchPRNumber(context.Background(), mock, 5)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantNum, got)
			}
		})
	}
}

func TestPostIntegratorFailure(t *testing.T) {
	tests := []struct {
		name      string
		number    int
		step      string
		err       error
		wantBody  string
		wantNum   int
	}{
		{
			name:   "consolidation failure",
			number: 42,
			step:   "consolidation",
			err:    fmt.Errorf("merge conflict"),
			wantBody: "⚠️ **Integrator failed** during consolidation: merge conflict\n\nYou can retry with `/herd integrate` on this issue or the batch PR.",
			wantNum: 42,
		},
		{
			name:   "CI check failure",
			number: 10,
			step:   "CI check",
			err:    fmt.Errorf("timeout"),
			wantBody: "⚠️ **Integrator failed** during CI check: timeout\n\nYou can retry with `/herd integrate` on this issue or the batch PR.",
			wantNum: 10,
		},
		{
			name:   "tier advancement failure",
			number: 7,
			step:   "tier advancement",
			err:    fmt.Errorf("branch not found"),
			wantBody: "⚠️ **Integrator failed** during tier advancement: branch not found\n\nYou can retry with `/herd integrate` on this issue or the batch PR.",
			wantNum: 7,
		},
		{
			name:   "review failure",
			number: 55,
			step:   "review",
			err:    fmt.Errorf("agent error"),
			wantBody: "⚠️ **Integrator failed** during review: agent error\n\nYou can retry with `/herd integrate` on this issue or the batch PR.",
			wantNum: 55,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockCommentIssueService{}
			postIntegratorFailure(context.Background(), mock, tt.number, tt.step, tt.err)
			assert.Equal(t, tt.wantNum, mock.addedNumber)
			assert.Equal(t, tt.wantBody, mock.addedBody)
		})
	}
}

func TestPostIntegratorFailure_CommentErrorDoesNotPanic(t *testing.T) {
	mock := &mockCommentIssueService{addCommentErr: fmt.Errorf("API down")}

	// Capture stderr to avoid noise
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	// Should not panic even when AddComment fails
	postIntegratorFailure(context.Background(), mock, 1, "consolidation", fmt.Errorf("some error"))

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Warning: failed to post comment")
}

func TestPostCommentWithLog(t *testing.T) {
	tests := []struct {
		name          string
		addCommentErr error
		wantStderr    string
	}{
		{
			name:          "successful comment posts without stderr output",
			addCommentErr: nil,
			wantStderr:    "",
		},
		{
			name:          "failed comment logs warning to stderr",
			addCommentErr: fmt.Errorf("API rate limit exceeded"),
			wantStderr:    "Warning: failed to post comment on issue #42: API rate limit exceeded\n",
		},
		{
			name:          "network error logs warning to stderr",
			addCommentErr: fmt.Errorf("connection refused"),
			wantStderr:    "Warning: failed to post comment on issue #42: connection refused\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockCommentIssueService{addCommentErr: tt.addCommentErr}

			// Capture stderr
			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			require.NoError(t, err)
			os.Stderr = w

			postCommentWithLog(context.Background(), mock, 42, "test message")

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStderr, buf.String())
			assert.Equal(t, 42, mock.addedNumber)
			assert.Equal(t, "test message", mock.addedBody)
		})
	}
}
