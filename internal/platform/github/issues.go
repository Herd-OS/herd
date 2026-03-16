package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type issueService struct{ c *Client }

func (s *issueService) Create(ctx context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	req := &gh.IssueRequest{
		Title:  gh.Ptr(title),
		Body:   gh.Ptr(body),
		Labels: &labels,
	}
	if milestone != nil {
		req.Milestone = milestone
	}

	issue, _, err := s.c.gh.Issues.Create(ctx, s.c.owner, s.c.repo, req)
	if err != nil {
		return nil, fmt.Errorf("creating issue: %w", err)
	}
	return mapIssue(issue), nil
}

func (s *issueService) Get(ctx context.Context, number int) (*platform.Issue, error) {
	issue, _, err := s.c.gh.Issues.Get(ctx, s.c.owner, s.c.repo, number)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", number, err)
	}
	return mapIssue(issue), nil
}

func (s *issueService) List(ctx context.Context, filters platform.IssueFilters) ([]*platform.Issue, error) {
	opts := &gh.IssueListByRepoOptions{
		State: filters.State,
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}
	if opts.State == "" {
		opts.State = "open"
	}
	if len(filters.Labels) > 0 {
		opts.Labels = filters.Labels
	}
	if filters.Milestone != nil {
		opts.Milestone = fmt.Sprintf("%d", *filters.Milestone)
	}

	var result []*platform.Issue
	for {
		issues, resp, err := s.c.gh.Issues.ListByRepo(ctx, s.c.owner, s.c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing issues: %w", err)
		}
		for _, issue := range issues {
			// GitHub's Issues API returns PRs too — filter them out
			if issue.PullRequestLinks != nil {
				continue
			}
			result = append(result, mapIssue(issue))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return result, nil
}

func (s *issueService) Update(ctx context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	req := &gh.IssueRequest{}
	if changes.Title != nil {
		req.Title = changes.Title
	}
	if changes.Body != nil {
		req.Body = changes.Body
	}
	if changes.State != nil {
		req.State = changes.State
	}
	if changes.Milestone != nil {
		req.Milestone = changes.Milestone
	}

	issue, _, err := s.c.gh.Issues.Edit(ctx, s.c.owner, s.c.repo, number, req)
	if err != nil {
		return nil, fmt.Errorf("updating issue #%d: %w", number, err)
	}
	return mapIssue(issue), nil
}

func (s *issueService) AddLabels(ctx context.Context, number int, labels []string) error {
	_, _, err := s.c.gh.Issues.AddLabelsToIssue(ctx, s.c.owner, s.c.repo, number, labels)
	if err != nil {
		return fmt.Errorf("adding labels to issue #%d: %w", number, err)
	}
	return nil
}

func (s *issueService) RemoveLabels(ctx context.Context, number int, labels []string) error {
	for _, label := range labels {
		_, err := s.c.gh.Issues.RemoveLabelForIssue(ctx, s.c.owner, s.c.repo, number, label)
		if err != nil {
			// Ignore 404 — label may not be on the issue
			if !strings.Contains(err.Error(), "404") {
				return fmt.Errorf("removing label %q from issue #%d: %w", label, number, err)
			}
		}
	}
	return nil
}

func (s *issueService) AddComment(ctx context.Context, number int, body string) error {
	comment := &gh.IssueComment{Body: gh.Ptr(body)}
	_, _, err := s.c.gh.Issues.CreateComment(ctx, s.c.owner, s.c.repo, number, comment)
	if err != nil {
		return fmt.Errorf("adding comment to issue #%d: %w", number, err)
	}
	return nil
}

func (s *issueService) ListComments(ctx context.Context, number int) ([]*platform.Comment, error) {
	opts := &gh.IssueListCommentsOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	var result []*platform.Comment
	for {
		comments, resp, err := s.c.gh.Issues.ListComments(ctx, s.c.owner, s.c.repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("listing comments for issue #%d: %w", number, err)
		}
		for _, c := range comments {
			result = append(result, &platform.Comment{
				ID:                c.GetID(),
				Body:              c.GetBody(),
				AuthorLogin:       c.GetUser().GetLogin(),
				AuthorAssociation: c.GetAuthorAssociation(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return result, nil
}

func (s *issueService) CreateCommentReaction(ctx context.Context, commentID int64, reaction string) error {
	_, _, err := s.c.gh.Reactions.CreateIssueCommentReaction(ctx, s.c.owner, s.c.repo, commentID, reaction)
	if err != nil {
		return fmt.Errorf("creating reaction on comment %d: %w", commentID, err)
	}
	return nil
}

func mapIssue(i *gh.Issue) *platform.Issue {
	labels := make([]string, len(i.Labels))
	for j, l := range i.Labels {
		labels[j] = l.GetName()
	}
	assignees := make([]string, len(i.Assignees))
	for j, a := range i.Assignees {
		assignees[j] = a.GetLogin()
	}
	result := &platform.Issue{
		Number:    i.GetNumber(),
		Title:     i.GetTitle(),
		Body:      i.GetBody(),
		State:     i.GetState(),
		Labels:    labels,
		Assignees: assignees,
		URL:       i.GetHTMLURL(),
	}
	if i.Milestone != nil {
		result.Milestone = mapMilestone(i.Milestone)
	}
	return result
}
