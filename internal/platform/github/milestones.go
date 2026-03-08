package github

import (
	"context"
	"fmt"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type milestoneService struct{ c *Client }

func (s *milestoneService) Create(ctx context.Context, title, description string, dueDate *time.Time) (*platform.Milestone, error) {
	m := &gh.Milestone{
		Title:       gh.Ptr(title),
		Description: gh.Ptr(description),
	}
	if dueDate != nil {
		ghTime := gh.Timestamp{Time: *dueDate}
		m.DueOn = &ghTime
	}

	milestone, _, err := s.c.gh.Issues.CreateMilestone(ctx, s.c.owner, s.c.repo, m)
	if err != nil {
		return nil, fmt.Errorf("creating milestone: %w", err)
	}
	return mapMilestone(milestone), nil
}

func (s *milestoneService) Get(ctx context.Context, number int) (*platform.Milestone, error) {
	milestone, _, err := s.c.gh.Issues.GetMilestone(ctx, s.c.owner, s.c.repo, number)
	if err != nil {
		return nil, fmt.Errorf("getting milestone #%d: %w", number, err)
	}
	return mapMilestone(milestone), nil
}

func (s *milestoneService) List(ctx context.Context) ([]*platform.Milestone, error) {
	opts := &gh.MilestoneListOptions{
		State: "open",
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}

	var result []*platform.Milestone
	for {
		milestones, resp, err := s.c.gh.Issues.ListMilestones(ctx, s.c.owner, s.c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing milestones: %w", err)
		}
		for _, m := range milestones {
			result = append(result, mapMilestone(m))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return result, nil
}

func (s *milestoneService) Update(ctx context.Context, number int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	m := &gh.Milestone{}
	if changes.Title != nil {
		m.Title = changes.Title
	}
	if changes.Description != nil {
		m.Description = changes.Description
	}
	if changes.State != nil {
		m.State = changes.State
	}
	if changes.DueDate != nil {
		ghTime := gh.Timestamp{Time: *changes.DueDate}
		m.DueOn = &ghTime
	}

	milestone, _, err := s.c.gh.Issues.EditMilestone(ctx, s.c.owner, s.c.repo, number, m)
	if err != nil {
		return nil, fmt.Errorf("updating milestone #%d: %w", number, err)
	}
	return mapMilestone(milestone), nil
}

func mapMilestone(m *gh.Milestone) *platform.Milestone {
	result := &platform.Milestone{
		Number:       m.GetNumber(),
		Title:        m.GetTitle(),
		Description:  m.GetDescription(),
		State:        m.GetState(),
		OpenIssues:   m.GetOpenIssues(),
		ClosedIssues: m.GetClosedIssues(),
	}
	if m.DueOn != nil {
		t := m.DueOn.Time
		result.DueDate = &t
	}
	return result
}
