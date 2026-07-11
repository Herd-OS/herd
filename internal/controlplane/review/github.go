package review

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/platform"
)

type AppGitHubClient struct {
	TokenSource appauth.TokenSource
	NewClient   func(ctx context.Context, installationID int64) (*gh.Client, error)
}

func (c AppGitHubClient) CreateCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) error {
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return err
	}
	req := &gh.RepoStatus{
		State:       gh.Ptr(status.State),
		Context:     gh.Ptr(status.Context),
		Description: gh.Ptr(status.Description),
	}
	if status.TargetURL != "" {
		req.TargetURL = gh.Ptr(status.TargetURL)
	}
	_, _, err = client.Repositories.CreateStatus(ctx, owner, repo, sha, req)
	if err != nil {
		return fmt.Errorf("creating Herd Review commit status on %s/%s@%s: %w", owner, repo, sha, err)
	}
	return nil
}

func (c AppGitHubClient) GetPullRequest(ctx context.Context, installationID int64, owner, repo string, number int) (*platform.PullRequest, error) {
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("getting pull request #%d: %w", number, err)
	}
	return &platform.PullRequest{
		Number:  pr.GetNumber(),
		Title:   pr.GetTitle(),
		Body:    pr.GetBody(),
		State:   pr.GetState(),
		Head:    pr.GetHead().GetRef(),
		HeadSHA: pr.GetHead().GetSHA(),
		Base:    pr.GetBase().GetRef(),
		URL:     pr.GetHTMLURL(),
	}, nil
}

func (c AppGitHubClient) CreateReviewForCommit(ctx context.Context, installationID int64, owner, repo string, number int, body string, event platform.ReviewEvent, commitID string) error {
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return err
	}
	req := &gh.PullRequestReviewRequest{
		Body:  gh.Ptr(body),
		Event: gh.Ptr(string(event)),
	}
	if commitID != "" {
		req.CommitID = gh.Ptr(commitID)
	}
	_, _, err = client.PullRequests.CreateReview(ctx, owner, repo, number, req)
	if err != nil {
		return fmt.Errorf("creating App-authored review on pull request #%d: %w", number, err)
	}
	return nil
}

func (c AppGitHubClient) AddPullRequestComment(ctx context.Context, installationID int64, owner, repo string, number int, body string) error {
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return err
	}
	_, _, err = client.Issues.CreateComment(ctx, owner, repo, number, &gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return fmt.Errorf("adding comment to pull request #%d: %w", number, err)
	}
	return nil
}

func (c AppGitHubClient) installationClient(ctx context.Context, installationID int64) (*gh.Client, error) {
	if c.NewClient != nil {
		return c.NewClient(ctx, installationID)
	}
	if c.TokenSource == nil {
		return nil, fmt.Errorf("GitHub App token source is required")
	}
	client, _, err := appauth.NewInstallationClient(ctx, c.TokenSource, installationID)
	if err != nil {
		return nil, err
	}
	return client, nil
}
