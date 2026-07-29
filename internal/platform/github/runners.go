package github

import (
	"context"
	"fmt"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type runnerService struct{ c *Client }

// RunnerRegistrationToken is a short-lived GitHub token for registering a self-hosted runner.
type RunnerRegistrationToken struct {
	Token     string
	ExpiresAt time.Time
}

func CreateRunnerRegistrationToken(ctx context.Context, client *gh.Client, owner string, repo string) (RunnerRegistrationToken, error) {
	if client == nil {
		return RunnerRegistrationToken{}, fmt.Errorf("GitHub client is required")
	}
	token, _, err := client.Actions.CreateRegistrationToken(ctx, owner, repo)
	if err != nil {
		return RunnerRegistrationToken{}, fmt.Errorf("creating runner registration token for %s/%s: %w", owner, repo, err)
	}
	if token == nil || token.GetToken() == "" {
		return RunnerRegistrationToken{}, fmt.Errorf("creating runner registration token for %s/%s: empty response", owner, repo)
	}
	expiresAt := time.Time{}
	if token.ExpiresAt != nil {
		expiresAt = token.ExpiresAt.Time
	}
	if expiresAt.IsZero() {
		return RunnerRegistrationToken{}, fmt.Errorf("creating runner registration token for %s/%s: missing expires_at", owner, repo)
	}
	if !expiresAt.After(time.Now().UTC()) {
		return RunnerRegistrationToken{}, fmt.Errorf("creating runner registration token for %s/%s: expires_at is not in the future", owner, repo)
	}
	return RunnerRegistrationToken{
		Token:     token.GetToken(),
		ExpiresAt: expiresAt,
	}, nil
}

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
