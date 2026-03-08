package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type labelService struct{ c *Client }

func (s *labelService) Create(ctx context.Context, name, color, description string) error {
	// Strip leading # from color if present
	color = strings.TrimPrefix(color, "#")

	label := &gh.Label{
		Name:        gh.Ptr(name),
		Color:       gh.Ptr(color),
		Description: gh.Ptr(description),
	}
	_, _, err := s.c.gh.Issues.CreateLabel(ctx, s.c.owner, s.c.repo, label)
	if err != nil {
		return fmt.Errorf("creating label %s: %w", name, err)
	}
	return nil
}

func (s *labelService) List(ctx context.Context) ([]*platform.Label, error) {
	opts := &gh.ListOptions{PerPage: 100}

	var result []*platform.Label
	for {
		labels, resp, err := s.c.gh.Issues.ListLabels(ctx, s.c.owner, s.c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing labels: %w", err)
		}
		for _, l := range labels {
			result = append(result, mapLabel(l))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return result, nil
}

func (s *labelService) Delete(ctx context.Context, name string) error {
	_, err := s.c.gh.Issues.DeleteLabel(ctx, s.c.owner, s.c.repo, name)
	if err != nil {
		return fmt.Errorf("deleting label %s: %w", name, err)
	}
	return nil
}

func mapLabel(l *gh.Label) *platform.Label {
	return &platform.Label{
		Name:        l.GetName(),
		Color:       l.GetColor(),
		Description: l.GetDescription(),
	}
}
