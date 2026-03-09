package integrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
)

// MergeApprovedParams holds parameters for merging an approved batch PR.
type MergeApprovedParams struct {
	PRNumber int
}

// MergeApprovedResult holds the result of a merge attempt.
type MergeApprovedResult struct {
	Merged  bool
	Skipped bool
	Reason  string
}

// MergeApproved merges a batch PR that has been approved by a human reviewer.
// It verifies the PR is a herd batch PR (title starts with "[herd]"),
// then merges using the configured strategy and runs post-merge cleanup.
// Non-herd PRs and already-closed PRs are silently skipped.
func MergeApproved(ctx context.Context, p platform.Platform, cfg *config.Config, params MergeApprovedParams) (*MergeApprovedResult, error) {
	pr, err := p.PullRequests().Get(ctx, params.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("getting PR #%d: %w", params.PRNumber, err)
	}

	// Skip non-herd PRs
	if !strings.HasPrefix(pr.Title, "[herd]") {
		return &MergeApprovedResult{Skipped: true, Reason: "not a herd batch PR"}, nil
	}

	// Skip already-merged or closed PRs
	if pr.State != "open" {
		return &MergeApprovedResult{Skipped: true, Reason: "PR is " + pr.State}, nil
	}

	// Merge using configured strategy
	if _, err := p.PullRequests().Merge(ctx, pr.Number, platform.MergeMethod(cfg.Integrator.Strategy)); err != nil {
		return nil, fmt.Errorf("merging batch PR #%d: %w", pr.Number, err)
	}

	// Post-merge cleanup: close issues, close milestone, delete branch
	msNumber, err := parseBatchBranchMilestone(pr.Head)
	if err != nil {
		// Merged successfully but can't parse milestone — not fatal
		return &MergeApprovedResult{Merged: true}, nil
	}

	if err := postMergeCleanup(ctx, p, msNumber, pr.Head); err != nil {
		return nil, fmt.Errorf("post-merge cleanup: %w", err)
	}

	return &MergeApprovedResult{Merged: true}, nil
}

// CleanupParams holds parameters for post-merge cleanup of externally merged PRs.
type CleanupParams struct {
	PRNumber int
}

// CleanupMerged handles post-merge cleanup for a batch PR that was merged
// externally (not by HerdOS auto-merge). It closes all milestone issues,
// closes the milestone, and deletes the batch branch.
// Non-herd PRs, non-merged PRs, and unparseable branches are silently skipped.
func CleanupMerged(ctx context.Context, p platform.Platform, params CleanupParams) error {
	pr, err := p.PullRequests().Get(ctx, params.PRNumber)
	if err != nil {
		return fmt.Errorf("getting PR #%d: %w", params.PRNumber, err)
	}

	// Only handle merged herd batch PRs
	if !strings.HasPrefix(pr.Title, "[herd]") {
		return nil
	}
	if pr.State != "closed" {
		return nil
	}

	msNumber, err := parseBatchBranchMilestone(pr.Head)
	if err != nil {
		return nil // Not a batch branch, skip
	}

	return postMergeCleanup(ctx, p, msNumber, pr.Head)
}
