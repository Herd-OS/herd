//go:build integration

package github

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	owner := os.Getenv("HERD_TEST_OWNER")
	repo := os.Getenv("HERD_TEST_REPO")
	if owner == "" || repo == "" {
		t.Skip("HERD_TEST_OWNER and HERD_TEST_REPO required for integration tests")
	}
	if os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("GH_TOKEN") == "" {
		t.Skip("GITHUB_TOKEN or GH_TOKEN required for integration tests")
	}
	client, err := New(owner, repo)
	require.NoError(t, err)
	return client
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestIntegration_Labels(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	labelSvc := client.Labels()

	name := uniqueName("herd-test-label")
	color := "0E8A16"
	desc := "Integration test label"

	// Create
	err := labelSvc.Create(ctx, name, color, desc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = labelSvc.Delete(ctx, name) })

	// List and verify
	labels, err := labelSvc.List(ctx)
	require.NoError(t, err)
	found := false
	for _, l := range labels {
		if l.Name == name {
			found = true
			assert.Equal(t, color, l.Color)
			break
		}
	}
	assert.True(t, found, "label %s not found in list", name)

	// Delete
	err = labelSvc.Delete(ctx, name)
	require.NoError(t, err)

	// Verify deleted
	labels, err = labelSvc.List(ctx)
	require.NoError(t, err)
	for _, l := range labels {
		assert.NotEqual(t, name, l.Name)
	}
}

func TestIntegration_Issues(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	issueSvc := client.Issues()

	title := uniqueName("herd-test-issue")
	body := "Integration test issue body"

	// Create
	issue, err := issueSvc.Create(ctx, title, body, []string{}, nil)
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, title, issue.Title)
	t.Cleanup(func() {
		closed := "closed"
		_, _ = issueSvc.Update(ctx, issue.Number, platform.IssueUpdate{State: &closed})
	})

	// Get
	got, err := issueSvc.Get(ctx, issue.Number)
	require.NoError(t, err)
	assert.Equal(t, title, got.Title)
	assert.Equal(t, body, got.Body)

	// Add comment
	err = issueSvc.AddComment(ctx, issue.Number, "Test comment")
	require.NoError(t, err)

	// Update (close)
	closed := "closed"
	updated, err := issueSvc.Update(ctx, issue.Number, platform.IssueUpdate{State: &closed})
	require.NoError(t, err)
	assert.NotNil(t, updated)
}

func TestIntegration_Milestones(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	msSvc := client.Milestones()

	title := uniqueName("herd-test-milestone")

	// Create
	ms, err := msSvc.Create(ctx, title, "Integration test", nil)
	require.NoError(t, err)
	require.NotNil(t, ms)
	assert.Equal(t, title, ms.Title)
	t.Cleanup(func() {
		closed := "closed"
		_, _ = msSvc.Update(ctx, ms.Number, platform.MilestoneUpdate{State: &closed})
	})

	// Get
	got, err := msSvc.Get(ctx, ms.Number)
	require.NoError(t, err)
	assert.Equal(t, title, got.Title)

	// List
	all, err := msSvc.List(ctx)
	require.NoError(t, err)
	found := false
	for _, m := range all {
		if m.Number == ms.Number {
			found = true
			break
		}
	}
	assert.True(t, found, "milestone not found in list")

	// Update (close)
	closed := "closed"
	_, err = msSvc.Update(ctx, ms.Number, platform.MilestoneUpdate{State: &closed})
	require.NoError(t, err)
}

func TestIntegration_Repository(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	repoSvc := client.Repository()

	// GetInfo
	info, err := repoSvc.GetInfo(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, info.Owner)
	assert.NotEmpty(t, info.Name)
	assert.NotEmpty(t, info.DefaultBranch)

	// GetDefaultBranch
	branch, err := repoSvc.GetDefaultBranch(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, branch)

	// GetBranchSHA
	sha, err := repoSvc.GetBranchSHA(ctx, branch)
	require.NoError(t, err)
	assert.Len(t, sha, 40)

	// CreateBranch
	branchName := uniqueName("herd-test-branch")
	err = repoSvc.CreateBranch(ctx, branchName, sha)
	require.NoError(t, err)
	t.Cleanup(func() { _ = repoSvc.DeleteBranch(ctx, branchName) })

	// GetBranchSHA for new branch
	newSHA, err := repoSvc.GetBranchSHA(ctx, branchName)
	require.NoError(t, err)
	assert.Equal(t, sha, newSHA)

	// DeleteBranch
	err = repoSvc.DeleteBranch(ctx, branchName)
	require.NoError(t, err)
}

func TestIntegration_PullRequests(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	repoSvc := client.Repository()
	prSvc := client.PullRequests()

	// Create a test branch
	defaultBranch, err := repoSvc.GetDefaultBranch(ctx)
	require.NoError(t, err)
	sha, err := repoSvc.GetBranchSHA(ctx, defaultBranch)
	require.NoError(t, err)

	branchName := uniqueName("herd-test-pr-branch")
	err = repoSvc.CreateBranch(ctx, branchName, sha)
	require.NoError(t, err)
	t.Cleanup(func() { _ = repoSvc.DeleteBranch(ctx, branchName) })

	// Add a commit so the branch is ahead of main
	fileName := uniqueName("test-file") + ".txt"
	content := []byte("integration test file")
	msg := "test commit for PR"
	_, _, err = client.gh.Repositories.CreateFile(ctx, client.owner, client.repo, fileName, &gh.RepositoryContentFileOptions{
		Message: &msg,
		Content: content,
		Branch:  &branchName,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Delete the test file
		fc, _, _, _ := client.gh.Repositories.GetContents(ctx, client.owner, client.repo, fileName, &gh.RepositoryContentGetOptions{Ref: branchName})
		if fc != nil {
			delMsg := "cleanup test file"
			_, _, _ = client.gh.Repositories.DeleteFile(ctx, client.owner, client.repo, fileName, &gh.RepositoryContentFileOptions{
				Message: &delMsg,
				SHA:     fc.SHA,
				Branch:  &branchName,
			})
		}
	})

	// Create PR
	title := uniqueName("herd-test-pr")
	pr, err := prSvc.Create(ctx, title, "Test PR body", branchName, defaultBranch)
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, title, pr.Title)
	assert.Equal(t, branchName, pr.Head)
	t.Cleanup(func() {
		closed := "closed"
		_, _ = prSvc.Update(ctx, pr.Number, nil, nil)
		// Close by updating state — PRs don't have a direct close, use Update
		_ = closed
	})

	// Get
	got, err := prSvc.Get(ctx, pr.Number)
	require.NoError(t, err)
	assert.Equal(t, pr.Number, got.Number)

	// List
	prs, err := prSvc.List(ctx, platform.PRFilters{State: "open"})
	require.NoError(t, err)
	found := false
	for _, p := range prs {
		if p.Number == pr.Number {
			found = true
			break
		}
	}
	assert.True(t, found, "PR not found in list")

	// Update title
	newTitle := title + "-updated"
	updated, err := prSvc.Update(ctx, pr.Number, &newTitle, nil)
	require.NoError(t, err)
	assert.Equal(t, newTitle, updated.Title)

	// AddComment
	err = prSvc.AddComment(ctx, pr.Number, "Test comment on PR")
	require.NoError(t, err)

	// CreateReview (comment — non-destructive)
	err = prSvc.CreateReview(ctx, pr.Number, "Integration test review", platform.ReviewComment)
	require.NoError(t, err)
}

func TestIntegration_Workflows(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	wfSvc := client.Workflows()

	// List completed runs (read-only)
	runs, err := wfSvc.ListRuns(ctx, platform.RunFilters{Status: "completed"})
	require.NoError(t, err)

	if len(runs) == 0 {
		t.Skip("No completed workflow runs found")
	}

	// Verify first run has expected fields
	assert.NotZero(t, runs[0].ID)
	assert.NotEmpty(t, runs[0].Status)
}

func TestIntegration_Runners(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	runnerSvc := client.Runners()

	runners, err := runnerSvc.List(ctx)
	require.NoError(t, err)

	if len(runners) == 0 {
		t.Log("No runners found — skipping field assertions")
		return
	}

	assert.NotEmpty(t, runners[0].Name)
	assert.NotEmpty(t, runners[0].Status)
}
