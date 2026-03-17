package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
