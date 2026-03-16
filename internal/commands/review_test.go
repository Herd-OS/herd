package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildReviewMock returns a mockPlatform with a batch PR at the given PR number.
func buildReviewMock(prNumber int, head string) *mockPlatform {
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			prNumber: {Number: prNumber, Head: head},
		},
	}
	return &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
	}
}

func reviewConfig() *config.Config {
	return &config.Config{
		Agent: config.Agent{Binary: "claude", Model: "claude-opus-4-6"},
		Integrator: config.Integrator{
			Review:             false, // disabled so integrator.Review returns early without git/agent
			ReviewMaxFixCycles: 3,
		},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}
}

// withReviewFn replaces the global reviewFn for the duration of the test and restores it.
func withReviewFn(t *testing.T, fn func(context.Context, platform.Platform, agent.Agent, *git.Git, *config.Config, integrator.ReviewParams) (*integrator.ReviewResult, error)) {
	t.Helper()
	orig := reviewFn
	reviewFn = fn
	t.Cleanup(func() { reviewFn = orig })
}

func TestHandleReview_Register(t *testing.T) {
	_, ok := Registry["review"]
	assert.True(t, ok, "review should be registered in Registry")
}

func TestHandleReview_NoPR(t *testing.T) {
	mock := buildReviewMock(0, "")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 0}
	cmd := &Command{Name: "review"}

	_, err := handleReview(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "review can only be used on batch PRs")
}

func TestHandleReview_NonBatchBranch(t *testing.T) {
	mock := buildReviewMock(42, "feat/my-feature")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 42}
	cmd := &Command{Name: "review"}

	_, err := handleReview(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "review can only be used on batch PRs")
}

func TestHandleReview_PRGetError(t *testing.T) {
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{getResult: map[int]*platform.PullRequest{}} // PR 99 not in map
	mock := &mockPlatform{issues: issueSvc, prs: prSvc}
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 99}
	cmd := &Command{Name: "review"}

	_, err := handleReview(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting PR #99")
}

func TestHandleReview_Outcomes(t *testing.T) {
	tests := []struct {
		name         string
		result       *integrator.ReviewResult
		wantPrefix   string
		wantContains string
	}{
		{
			name:         "approved",
			result:       &integrator.ReviewResult{Approved: true},
			wantPrefix:   "✅",
			wantContains: "All acceptance criteria met",
		},
		{
			name:         "fix dispatched",
			result:       &integrator.ReviewResult{FixIssues: []int{101, 102}, FixCycle: 1},
			wantPrefix:   "🔍",
			wantContains: "Dispatched fix workers",
		},
		{
			name:         "fix dispatched single",
			result:       &integrator.ReviewResult{FixIssues: []int{55}, FixCycle: 2},
			wantPrefix:   "🔍",
			wantContains: "#55",
		},
		{
			name:         "max cycles",
			result:       &integrator.ReviewResult{MaxCyclesHit: true},
			wantPrefix:   "⚠️",
			wantContains: "max fix cycles reached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := buildReviewMock(50, "herd/batch/1-my-batch")
			hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 50}
			cmd := &Command{Name: "review"}

			result := tt.result
			withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, _ integrator.ReviewParams) (*integrator.ReviewResult, error) {
				return result, nil
			})

			resp, err := handleReview(context.Background(), hctx, cmd)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(resp, tt.wantPrefix), "response %q should start with %q", resp, tt.wantPrefix)
			assert.Contains(t, resp, tt.wantContains)
		})
	}
}

func TestHandleReview_FixDispatchedFormatting(t *testing.T) {
	mock := buildReviewMock(50, "herd/batch/1-my-batch")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 50}
	cmd := &Command{Name: "review"}

	withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, _ integrator.ReviewParams) (*integrator.ReviewResult, error) {
		return &integrator.ReviewResult{FixIssues: []int{10, 20}, FixCycle: 3}, nil
	})

	resp, err := handleReview(context.Background(), hctx, cmd)
	require.NoError(t, err)
	assert.Contains(t, resp, "#10, #20")
	assert.Contains(t, resp, "(cycle 3)")
}

func TestHandleReview_ReviewFnError(t *testing.T) {
	mock := buildReviewMock(50, "herd/batch/1-my-batch")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 50}
	cmd := &Command{Name: "review"}

	withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, _ integrator.ReviewParams) (*integrator.ReviewResult, error) {
		return nil, fmt.Errorf("agent exploded")
	})

	_, err := handleReview(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running review")
	assert.Contains(t, err.Error(), "agent exploded")
}

func TestHandleReview_PromptPassthrough(t *testing.T) {
	mock := buildReviewMock(50, "herd/batch/1-my-batch")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 50, RepoRoot: t.TempDir()}
	cmd := &Command{Name: "review", Prompt: "focus on security"}

	var capturedParams integrator.ReviewParams
	withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, params integrator.ReviewParams) (*integrator.ReviewResult, error) {
		capturedParams = params
		return &integrator.ReviewResult{Approved: true}, nil
	})

	resp, err := handleReview(context.Background(), hctx, cmd)
	require.NoError(t, err)
	assert.Contains(t, resp, "LGTM")
	assert.Contains(t, capturedParams.SystemPrompt, "Additional Review Instructions")
	assert.Contains(t, capturedParams.SystemPrompt, "focus on security")
}

func TestHandleReview_NoPromptNoSystemPrompt(t *testing.T) {
	mock := buildReviewMock(50, "herd/batch/1-my-batch")
	hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 50, RepoRoot: t.TempDir()}
	cmd := &Command{Name: "review"} // no prompt

	var capturedParams integrator.ReviewParams
	withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, params integrator.ReviewParams) (*integrator.ReviewResult, error) {
		capturedParams = params
		return &integrator.ReviewResult{Approved: true}, nil
	})

	_, err := handleReview(context.Background(), hctx, cmd)
	require.NoError(t, err)
	assert.Empty(t, capturedParams.SystemPrompt, "SystemPrompt should be empty when no prompt is given")
}

func TestHandleReview_BatchBranchValidation(t *testing.T) {
	tests := []struct {
		name    string
		head    string
		wantErr bool
	}{
		{"valid batch branch", "herd/batch/1-my-batch", false},
		{"valid batch branch large number", "herd/batch/42-some-long-slug", false},
		{"worker branch", "herd/worker/5-fix-something", true},
		{"main branch", "main", true},
		{"feature branch", "feat/add-something", true},
		{"empty branch", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := buildReviewMock(100, tt.head)
			hctx := &HandlerContext{Platform: mock, Config: reviewConfig(), PRNumber: 100}
			cmd := &Command{Name: "review"}

			if !tt.wantErr {
				withReviewFn(t, func(_ context.Context, _ platform.Platform, _ agent.Agent, _ *git.Git, _ *config.Config, _ integrator.ReviewParams) (*integrator.ReviewResult, error) {
					return &integrator.ReviewResult{Approved: true}, nil
				})
			}

			_, err := handleReview(context.Background(), hctx, cmd)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "review can only be used on batch PRs")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
