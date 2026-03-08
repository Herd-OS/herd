package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type runnerService struct{ c *Client }

func (s *runnerService) List(ctx context.Context) ([]*platform.Runner, error) {
	opts := &gh.ListRunnersOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}

	runners, _, err := s.c.gh.Actions.ListRunners(ctx, s.c.owner, s.c.repo, opts)
	if err != nil {
		return nil, fmt.Errorf("listing runners: %w", err)
	}

	var result []*platform.Runner
	for _, r := range runners.Runners {
		result = append(result, mapRunner(r))
	}
	return result, nil
}

func (s *runnerService) Get(ctx context.Context, id int64) (*platform.Runner, error) {
	runner, _, err := s.c.gh.Actions.GetRunner(ctx, s.c.owner, s.c.repo, id)
	if err != nil {
		return nil, fmt.Errorf("getting runner %d: %w", id, err)
	}
	return mapRunner(runner), nil
}

func mapRunner(r *gh.Runner) *platform.Runner {
	labels := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		labels[i] = l.GetName()
	}
	return &platform.Runner{
		ID:     r.GetID(),
		Name:   r.GetName(),
		Status: r.GetStatus(),
		Labels: labels,
		Busy:   r.GetBusy(),
	}
}
