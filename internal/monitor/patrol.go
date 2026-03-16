package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// PatrolResult holds the result of a monitor patrol.
type PatrolResult struct {
	StaleIssues       int
	FailedIssues      int
	RedispatchedCount int
	EscalatedCount    int
	StuckPRs          int
	CIFailures        int
}

const monitorCommentSignature = "**HerdOS Monitor Alert**"

// hasMonitorComment checks if a HerdOS Monitor comment already exists on an issue.
// Fails open (returns false on error) so a broken comments API doesn't silence the monitor.
func hasMonitorComment(ctx context.Context, p platform.Platform, number int) bool {
	comments, err := p.Issues().ListComments(ctx, number)
	if err != nil {
		return false
	}
	for _, c := range comments {
		if strings.Contains(c.Body, monitorCommentSignature) {
			return true
		}
	}
	return false
}

// Patrol checks for stale, failed, or stuck work and takes corrective action.
func Patrol(ctx context.Context, p platform.Platform, cfg *config.Config) (*PatrolResult, error) {
	result := &PatrolResult{}

	// List in-progress and failed issues
	inProgress, err := p.Issues().List(ctx, platform.IssueFilters{
		State:  "open",
		Labels: []string{issues.StatusInProgress},
	})
	if err != nil {
		return nil, fmt.Errorf("listing in-progress issues: %w", err)
	}

	failed, err := p.Issues().List(ctx, platform.IssueFilters{
		State:  "open",
		Labels: []string{issues.StatusFailed},
	})
	if err != nil {
		return nil, fmt.Errorf("listing failed issues: %w", err)
	}

	// Get active runs for stale detection
	activeRuns, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress"})
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}

	// Check for stale in-progress issues
	for _, issue := range inProgress {
		hasRun := false
		for _, run := range activeRuns {
			if run.Inputs["issue_number"] == fmt.Sprintf("%d", issue.Number) {
				hasRun = true
				// Check if run exceeds timeout
				if cfg.Workers.TimeoutMinutes > 0 && time.Since(run.CreatedAt) > time.Duration(cfg.Workers.TimeoutMinutes)*time.Minute {
					_ = p.Workflows().CancelRun(ctx, run.ID)
					_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusInProgress})
					_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
					if !hasMonitorComment(ctx, p, issue.Number) {
						_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
							"⚠️ **HerdOS Monitor Alert**\n\nWorker run exceeded timeout (%d minutes). Run cancelled.\n\n%s",
							cfg.Workers.TimeoutMinutes, buildMentions(cfg.Monitor.NotifyUsers)))
					}
					result.StaleIssues++
				}
				break
			}
		}
		if !hasRun {
			result.StaleIssues++
			if !hasMonitorComment(ctx, p, issue.Number) {
				_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
					"⚠️ **HerdOS Monitor Alert**\n\nIssue #%d is in-progress with no active workflow run. Marking as failed for redispatch.\n\n%s",
					issue.Number, buildMentions(cfg.Monitor.NotifyUsers)))
			}
			_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
		}
	}

	// Handle failed issues
	if cfg.Monitor.AutoRedispatch {
		completedRuns, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "completed"})
		if err != nil {
			return nil, fmt.Errorf("listing completed runs: %w", err)
		}

		for _, issue := range failed {
			result.FailedIssues++

			failureCount, lastFailedRun := countFailures(completedRuns, issue.Number)

			if failureCount >= cfg.Monitor.MaxRedispatchAttempts {
				if !hasMonitorComment(ctx, p, issue.Number) {
					_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
						"⚠️ **HerdOS Monitor Alert**\n\nIssue #%d has failed %d times. Max re-dispatch attempts reached.\n\nManual intervention needed.\n\n%s",
						issue.Number, failureCount, buildMentions(cfg.Monitor.NotifyUsers)))
				}
				result.EscalatedCount++
				continue
			}

			if lastFailedRun != nil && time.Since(lastFailedRun.CreatedAt) < BackoffDelay(failureCount) {
				continue // Backoff not elapsed
			}

			if issue.Milestone == nil {
				continue
			}

			// Post /herd retry command — the comment handler will dispatch
			_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
				"/herd retry %d", issue.Number))
			result.RedispatchedCount++
		}
	} else {
		result.FailedIssues = len(failed)
	}

	// Stuck PR detection
	openPRs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open"})
	if err != nil {
		return nil, fmt.Errorf("listing open PRs: %w", err)
	}
	for _, pr := range openPRs {
		if !strings.HasPrefix(pr.Title, "[herd]") {
			continue
		}
		if cfg.Monitor.MaxPRHAgeHours > 0 && time.Since(pr.CreatedAt) > time.Duration(cfg.Monitor.MaxPRHAgeHours)*time.Hour {
			if !hasMonitorComment(ctx, p, pr.Number) {
				_ = p.PullRequests().AddComment(ctx, pr.Number, fmt.Sprintf(
					"⚠️ **HerdOS Monitor Alert**\n\nThis batch PR has been open for over %d hours.\n\n%s",
					cfg.Monitor.MaxPRHAgeHours, buildMentions(cfg.Monitor.NotifyUsers)))
			}
			result.StuckPRs++
		}

		// CI failure detection on batch PRs
		if cfg.Integrator.RequireCI && strings.HasPrefix(pr.Head, "herd/batch/") {
			ciStatus, err := p.Checks().GetCombinedStatus(ctx, pr.Head)
			if err == nil && ciStatus == "failure" {
				if !hasCIFixPendingLabel(ctx, p, pr.Number) {
					_ = p.PullRequests().AddComment(ctx, pr.Number, "/herd fix-ci")
					_ = p.Issues().AddLabels(ctx, pr.Number, []string{issues.CIFixPending})
				}
				result.CIFailures++
			} else if err == nil && ciStatus == "success" {
				// CI passed — clear the fix-pending label so a future failure can re-trigger.
				_ = p.Issues().RemoveLabels(ctx, pr.Number, []string{issues.CIFixPending})
			}
		}
	}

	return result, nil
}

// hasCIFixPendingLabel returns true if the herd/ci-fix-pending label is present
// on the PR, indicating that a /herd fix-ci command was already posted for the
// current failure cycle. The label is removed when CI passes, allowing future
// failures to re-trigger the command. Fails open (returns false on error) so a
// broken API does not silence future fix triggers.
func hasCIFixPendingLabel(ctx context.Context, p platform.Platform, prNumber int) bool {
	issue, err := p.Issues().Get(ctx, prNumber)
	if err != nil {
		return false
	}
	for _, label := range issue.Labels {
		if label == issues.CIFixPending {
			return true
		}
	}
	return false
}

// BackoffDelay returns the backoff delay for a given failure count.
func BackoffDelay(failureCount int) time.Duration {
	switch failureCount {
	case 1:
		return 0
	case 2:
		return 15 * time.Minute
	default:
		return 1 * time.Hour
	}
}

func countFailures(runs []*platform.Run, issueNumber int) (int, *platform.Run) {
	count := 0
	var latest *platform.Run
	numStr := fmt.Sprintf("%d", issueNumber)
	for _, run := range runs {
		if run.Inputs["issue_number"] == numStr && run.Conclusion == "failure" {
			count++
			if latest == nil || run.CreatedAt.After(latest.CreatedAt) {
				latest = run
			}
		}
	}
	return count, latest
}

func buildMentions(users []string) string {
	if len(users) == 0 {
		return ""
	}
	mentions := make([]string, len(users))
	for i, u := range users {
		mentions[i] = "@" + u
	}
	return "/cc " + strings.Join(mentions, " ")
}
