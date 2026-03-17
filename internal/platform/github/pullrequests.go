package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type pullRequestService struct{ c *Client }

func (s *pullRequestService) Create(ctx context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	pr, _, err := s.c.gh.PullRequests.Create(ctx, s.c.owner, s.c.repo, &gh.NewPullRequest{
		Title: gh.Ptr(title),
		Body:  gh.Ptr(body),
		Head:  gh.Ptr(head),
		Base:  gh.Ptr(base),
	})
	if err != nil {
		return nil, fmt.Errorf("creating pull request: %w", err)
	}
	return mapPullRequest(pr), nil
}

func (s *pullRequestService) Get(ctx context.Context, number int) (*platform.PullRequest, error) {
	pr, _, err := s.c.gh.PullRequests.Get(ctx, s.c.owner, s.c.repo, number)
	if err != nil {
		return nil, fmt.Errorf("getting pull request #%d: %w", number, err)
	}
	return mapPullRequest(pr), nil
}

func (s *pullRequestService) List(ctx context.Context, filters platform.PRFilters) ([]*platform.PullRequest, error) {
	// GitHub API requires "owner:branch" format for head filter
	head := filters.Head
	if head != "" && !strings.Contains(head, ":") {
		head = s.c.owner + ":" + head
	}

	opts := &gh.PullRequestListOptions{
		State: filters.State,
		Head:  head,
		Base:  filters.Base,
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}
	if opts.State == "" {
		opts.State = "open"
	}

	var result []*platform.PullRequest
	for {
		prs, resp, err := s.c.gh.PullRequests.List(ctx, s.c.owner, s.c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing pull requests: %w", err)
		}
		for _, pr := range prs {
			result = append(result, mapPullRequest(pr))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return result, nil
}

func (s *pullRequestService) Update(ctx context.Context, number int, title, body *string) (*platform.PullRequest, error) {
	update := &gh.PullRequest{}
	if title != nil {
		update.Title = title
	}
	if body != nil {
		update.Body = body
	}

	pr, _, err := s.c.gh.PullRequests.Edit(ctx, s.c.owner, s.c.repo, number, update)
	if err != nil {
		return nil, fmt.Errorf("updating pull request #%d: %w", number, err)
	}
	return mapPullRequest(pr), nil
}

func (s *pullRequestService) Merge(ctx context.Context, number int, method platform.MergeMethod) (*platform.MergeResult, error) {
	result, _, err := s.c.gh.PullRequests.Merge(ctx, s.c.owner, s.c.repo, number, "", &gh.PullRequestOptions{
		MergeMethod: string(method),
	})
	if err != nil {
		return nil, fmt.Errorf("merging pull request #%d: %w", number, err)
	}
	return &platform.MergeResult{
		SHA:     result.GetSHA(),
		Merged:  result.GetMerged(),
		Message: result.GetMessage(),
	}, nil
}

func (s *pullRequestService) UpdateBranch(ctx context.Context, number int) error {
	_, _, err := s.c.gh.PullRequests.UpdateBranch(ctx, s.c.owner, s.c.repo, number, nil)
	if err != nil {
		// GitHub returns 202 Accepted for this endpoint, which go-github
		// surfaces as an AcceptedError. This is not a real error.
		if _, ok := err.(*gh.AcceptedError); ok {
			return nil
		}
		return fmt.Errorf("updating branch for pull request #%d: %w", number, err)
	}
	return nil
}

func (s *pullRequestService) CreateReview(ctx context.Context, number int, body string, event platform.ReviewEvent) error {
	_, _, err := s.c.gh.PullRequests.CreateReview(ctx, s.c.owner, s.c.repo, number, &gh.PullRequestReviewRequest{
		Body:  gh.Ptr(body),
		Event: gh.Ptr(string(event)),
	})
	if err != nil {
		return fmt.Errorf("creating review on pull request #%d: %w", number, err)
	}
	return nil
}

func (s *pullRequestService) AddComment(ctx context.Context, number int, body string) error {
	_, _, err := s.c.gh.Issues.CreateComment(ctx, s.c.owner, s.c.repo, number, &gh.IssueComment{
		Body: gh.Ptr(body),
	})
	if err != nil {
		return fmt.Errorf("adding comment to pull request #%d: %w", number, err)
	}
	return nil
}

func (s *pullRequestService) GetDiff(ctx context.Context, number int) (string, error) {
	diff, _, err := s.c.gh.PullRequests.GetRaw(ctx, s.c.owner, s.c.repo, number, gh.RawOptions{Type: gh.Diff})
	if err != nil {
		return "", fmt.Errorf("getting diff for pull request #%d: %w", number, err)
	}
	return diff, nil
}

func mapPullRequest(pr *gh.PullRequest) *platform.PullRequest {
	return &platform.PullRequest{
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		Body:      pr.GetBody(),
		State:     pr.GetState(),
		Head:      pr.GetHead().GetRef(),
		Base:      pr.GetBase().GetRef(),
		Mergeable: pr.GetMergeable(),
		URL:       pr.GetHTMLURL(),
		CreatedAt: pr.GetCreatedAt().Time,
	}
}
