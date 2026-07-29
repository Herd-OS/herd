package github

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTokenFailsClosedInProductionRunner(t *testing.T) {
	for _, key := range []string{"HERD_RUNNER", "HERD_LOCAL_GITHUB_AUTH", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERD_RUNNER", "true")

	token, err := resolveToken()

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "local auth is disabled")
	assert.Contains(t, err.Error(), "HERD_RUNNER=true")
}

func TestResolveTokenRejectsLocalOverrideInRunner(t *testing.T) {
	for _, key := range []string{"HERD_RUNNER", "HERD_LOCAL_GITHUB_AUTH", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("HERD_LOCAL_GITHUB_AUTH", "true")
	t.Setenv("GITHUB_TOKEN", "ghp_local")

	token, err := resolveToken()

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "local auth is disabled")
}

func TestReviewInputClientDoesNotExposeGitHubMutations(t *testing.T) {
	client, err := NewReviewInputWithToken("octo", "widgets", "ghs_read")
	require.NoError(t, err)

	var raw any = client
	_, isPlatform := raw.(platform.Platform)
	_, hasWorkflows := raw.(interface {
		Workflows() platform.WorkflowService
	})
	_, hasRepository := raw.(interface {
		Repository() platform.RepositoryService
	})
	require.False(t, isPlatform)
	assert.False(t, hasWorkflows)
	assert.False(t, hasRepository)

	issues := client.Issues()
	pullRequests := client.PullRequests()
	checks := client.Checks()

	_, canAddIssueComment := any(issues).(interface {
		AddComment(context.Context, int, string) error
	})
	_, canCreateReview := any(pullRequests).(interface {
		CreateReview(context.Context, int, string, platform.ReviewEvent) error
	})
	_, canCreateReviewForCommit := any(pullRequests).(interface {
		CreateReviewForCommit(context.Context, int, string, platform.ReviewEvent, string) error
	})
	_, canCreateCommitStatus := any(checks).(interface {
		CreateCommitStatus(context.Context, string, platform.CommitStatus) error
	})
	assert.False(t, canAddIssueComment)
	assert.False(t, canCreateReview)
	assert.False(t, canCreateReviewForCommit)
	assert.False(t, canCreateCommitStatus)
}
