package review

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/platform"
)

type AppGitHubClient struct {
	TokenSource appauth.TokenSource
	NewClient   func(ctx context.Context, installationID int64) (*gh.Client, error)
	AppLogin    string
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

func (c AppGitHubClient) FindCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) (bool, error) {
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return false, err
	}
	opts := &gh.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := client.Repositories.ListStatuses(ctx, owner, repo, sha, opts)
		if err != nil {
			return false, fmt.Errorf("listing commit statuses on %s/%s@%s: %w", owner, repo, sha, err)
		}
		for _, existing := range statuses {
			if existing.GetContext() == status.Context &&
				existing.GetState() == status.State &&
				existing.GetTargetURL() == status.TargetURL &&
				existing.GetDescription() == status.Description {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
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

func (c AppGitHubClient) FindReviewForCommit(ctx context.Context, installationID int64, owner, repo string, number int, body string, event platform.ReviewEvent, commitID string) (bool, error) {
	if strings.TrimSpace(c.AppLogin) == "" {
		return false, fmt.Errorf("herd GitHub App login is required for review repair")
	}
	client, err := c.installationClient(ctx, installationID)
	if err != nil {
		return false, err
	}
	wantState := reviewEventState(event)
	opts := &gh.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, number, opts)
		if err != nil {
			return false, fmt.Errorf("listing reviews on pull request #%d: %w", number, err)
		}
		for _, review := range reviews {
			if review.GetCommitID() == commitID &&
				strings.TrimSpace(review.GetBody()) == strings.TrimSpace(body) &&
				strings.EqualFold(review.GetState(), wantState) &&
				c.matchesAppActor(review.GetUser().GetLogin()) {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c AppGitHubClient) matchesAppActor(login string) bool {
	want := strings.TrimSpace(c.AppLogin)
	if want == "" {
		return false
	}
	if strings.EqualFold(login, want) {
		return true
	}
	if !strings.HasSuffix(strings.ToLower(want), "[bot]") && strings.EqualFold(login, want+"[bot]") {
		return true
	}
	return false
}

func reviewEventState(event platform.ReviewEvent) string {
	switch event {
	case platform.ReviewApprove:
		return "APPROVED"
	case platform.ReviewRequestChanges:
		return "CHANGES_REQUESTED"
	case platform.ReviewCommentEvent:
		return "COMMENTED"
	default:
		return string(event)
	}
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
