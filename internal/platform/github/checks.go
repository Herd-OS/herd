package github

import (
	"context"
	"errors"
	"fmt"
	"os"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

// Compile-time check.
var _ platform.CheckService = (*checkService)(nil)

type checkService struct{ c *Client }

func (s *checkService) GetCombinedStatus(ctx context.Context, ref string) (string, error) {
	// Check both commit statuses (older API) and check runs (newer API, used by
	// external apps like Cloudflare). The combined result is the worst of the two.

	// 1. Commit statuses
	var permissionDenied bool
	commitStatus, _, err := s.c.gh.Repositories.GetCombinedStatus(ctx, s.c.owner, s.c.repo, ref, nil)
	var statusState string
	if err != nil {
		var errResp *gh.ErrorResponse
		if errors.As(err, &errResp) && (errResp.Response.StatusCode == 403 || errResp.Response.StatusCode == 404) {
			statusState = ""
			permissionDenied = true
		} else {
			return "", fmt.Errorf("getting combined status for %s: %w", ref, err)
		}
	} else {
		statusState = commitStatus.GetState() // "success", "pending", "failure", or ""
	}

	// 2. Check runs
	checkRuns, _, err := s.c.gh.Checks.ListCheckRunsForRef(ctx, s.c.owner, s.c.repo, ref, nil)
	var checksState string
	if err != nil {
		var errResp *gh.ErrorResponse
		if errors.As(err, &errResp) && (errResp.Response.StatusCode == 403 || errResp.Response.StatusCode == 404) {
			checksState = ""
			permissionDenied = true
		} else {
			return "", fmt.Errorf("listing check runs for %s: %w", ref, err)
		}
	} else {
		checksState = ""
		if checkRuns.GetTotal() > 0 {
			checksState = "success"
			for _, cr := range checkRuns.CheckRuns {
				if cr.GetStatus() != "completed" {
					checksState = "pending"
					break
				}
				if cr.GetConclusion() == "failure" || cr.GetConclusion() == "cancelled" {
					checksState = "failure"
					break
				}
			}
		}
	}

	// If both endpoints returned nothing, treat as no CI available.
	if statusState == "" && checksState == "" {
		if permissionDenied {
			fmt.Fprintf(os.Stderr, "warning: CI status unavailable for %s (insufficient permissions), treating as success\n", ref)
		}
		return "success", nil
	}

	// Combine: failure wins, then pending, then success
	if statusState == "failure" || checksState == "failure" {
		return "failure", nil
	}
	if statusState == "pending" || checksState == "pending" {
		return "pending", nil
	}
	if statusState == "success" || checksState == "success" {
		return "success", nil
	}
	// No statuses or check runs at all
	return "success", nil
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
