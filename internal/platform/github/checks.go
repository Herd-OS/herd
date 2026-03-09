package github

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/platform"
)

// Compile-time check.
var _ platform.CheckService = (*checkService)(nil)

type checkService struct{ c *Client }

func (s *checkService) GetCombinedStatus(ctx context.Context, ref string) (string, error) {
	status, _, err := s.c.gh.Repositories.GetCombinedStatus(ctx, s.c.owner, s.c.repo, ref, nil)
	if err != nil {
		return "", fmt.Errorf("getting combined status for %s: %w", ref, err)
	}
	return status.GetState(), nil
}

func (s *checkService) RerunFailedChecks(ctx context.Context, ref string) error {
	suites, _, err := s.c.gh.Checks.ListCheckSuitesForRef(ctx, s.c.owner, s.c.repo, ref, nil)
	if err != nil {
		return fmt.Errorf("listing check suites for %s: %w", ref, err)
	}

	for _, suite := range suites.CheckSuites {
		if suite.GetConclusion() == "failure" {
			_, err := s.c.gh.Checks.ReRequestCheckSuite(ctx, s.c.owner, s.c.repo, suite.GetID())
			if err != nil {
				return fmt.Errorf("re-requesting check suite %d: %w", suite.GetID(), err)
			}
		}
	}
	return nil
}
