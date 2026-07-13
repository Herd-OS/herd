package integrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

type reviewStateFilterStats struct {
	StalePRStateFindingsIgnored int
	CascadeLabelWasStale        bool
	CascadeLabelRemoved         bool
	CascadeLabelRemoveError     string
}

func reconcileReviewFindingsWithLivePRState(ctx context.Context, issueSvc platform.IssueService, pr *platform.PullRequest, findings []agent.ReviewFinding) ([]agent.ReviewFinding, reviewStateFilterStats) {
	state := livePRMergeState(pr)
	stats := cleanupStaleCascadeLabel(ctx, issueSvc, pr, state)
	if !state.Clean {
		return findings, stats
	}

	filtered := make([]agent.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if shouldIgnoreCascadeFinding(state, pr, finding) {
			stats.StalePRStateFindingsIgnored++
			continue
		}
		filtered = append(filtered, finding)
	}
	return filtered, stats
}

func isCascadeOrMergeConflictFinding(f agent.ReviewFinding) bool {
	text := strings.ToLower(strings.TrimSpace(f.Description))
	if text == "" {
		return false
	}

	phrases := []string{
		issues.CascadeFailed,
		"cascade failed",
		"cascade-failed",
		"conflict-resolution cascade",
		"conflict resolution cascade",
		"cascade state",
		"unresolved merge conflict",
		"unresolved conflict",
		"merge conflict",
		"merge-conflict",
		"branch conflict",
		"branch-conflict",
		"resolve conflicts",
		"resolve merge conflicts",
	}
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}

	return strings.Contains(text, "conflict") &&
		(strings.Contains(text, "base branch") ||
			strings.Contains(text, "batch branch") ||
			strings.Contains(text, "worker branch") ||
			strings.Contains(text, "github reports") ||
			strings.Contains(text, "not mergeable"))
}

func shouldIgnoreCascadeFinding(state prMergeState, _ *platform.PullRequest, f agent.ReviewFinding) bool {
	return state.Clean && isCascadeOrMergeConflictFinding(f)
}

func cleanupStaleCascadeLabel(ctx context.Context, issueSvc platform.IssueService, pr *platform.PullRequest, state prMergeState) reviewStateFilterStats {
	var stats reviewStateFilterStats
	if pr == nil || issueSvc == nil || !state.Clean || !issues.HasLabel(pr.Labels, issues.CascadeFailed) {
		return stats
	}

	stats.CascadeLabelWasStale = true
	if err := issueSvc.RemoveLabels(ctx, pr.Number, []string{issues.CascadeFailed}); err != nil {
		stats.CascadeLabelRemoveError = err.Error()
		fmt.Printf("Warning: failed to remove stale %s label from PR #%d: %s\n", issues.CascadeFailed, pr.Number, err)
		return stats
	}
	stats.CascadeLabelRemoved = true
	return stats
}

func buildStalePRStateFindingsIgnoredComment(stats reviewStateFilterStats) string {
	var b strings.Builder
	b.WriteString("✅ **HerdOS Agent Review**\n\n")
	b.WriteString("Stale PR-state findings ignored: GitHub currently reports this PR as clean/mergeable, so Herd ignored historical cascade/merge-conflict metadata and did not dispatch a fix worker.")
	return appendStalePRStateCleanupFailureNote(b.String(), stats)
}

func appendStalePRStateCleanupFailureNote(comment string, stats reviewStateFilterStats) string {
	if stats.StalePRStateFindingsIgnored == 0 || stats.CascadeLabelRemoveError == "" {
		return comment
	}
	return strings.TrimRight(comment, "\n") + fmt.Sprintf("\n\nTried to remove stale %s label but cleanup failed: %s. Stale finding was still ignored because live GitHub mergeability is clean.", issues.CascadeFailed, stats.CascadeLabelRemoveError)
}
