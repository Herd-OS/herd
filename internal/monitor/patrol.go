package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// PatrolResult holds the result of a monitor patrol.
type PatrolResult struct {
	StaleIssues       int
	FailedIssues      int
	RedispatchedCount int
	EscalatedCount    int
	StuckPRs              int
	CIFailures            int
	StaleReadyDispatched  int
	ConflictDetected      int
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

			if hasRetryPendingLabel(ctx, p, issue.Number) {
				continue
			}

			// Add the label BEFORE posting the comment so that a second
			// concurrent patrol run racing past the hasRetryPendingLabel
			// check sees the label and skips, rather than posting a
			// duplicate /herd retry comment.
			_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.RetryPending})
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
				_ = p.Issues().AddComment(ctx, pr.Number, fmt.Sprintf(
					"⚠️ **HerdOS Monitor Alert**\n\nThis batch PR has been open for over %d hours.\n\n%s",
					cfg.Monitor.MaxPRHAgeHours, buildMentions(cfg.Monitor.NotifyUsers)))
			}
			result.StuckPRs++
		}

		// CI failure detection on batch PRs
		if cfg.Integrator.RequireCI && strings.HasPrefix(pr.Head, "herd/batch/") {
			ciStatus, err := p.Checks().GetCombinedStatus(ctx, pr.Head)
			if err == nil {
				switch ciStatus {
				case "failure":
					if !hasCIFixPendingLabel(ctx, p, pr.Number) {
						// Add the label BEFORE posting the comment so that a second
						// concurrent patrol run racing past the hasCIFixPendingLabel
						// check sees the label and skips, rather than posting a
						// duplicate /herd fix-ci comment. The handler's beforeDispatch
						// will re-add the label idempotently when workers are dispatched.
						_ = p.Issues().AddLabels(ctx, pr.Number, []string{issues.CIFixPending})
						_ = p.Issues().AddComment(ctx, pr.Number, "/herd fix-ci")
					}
					result.CIFailures++
				case "success":
					deleteCIFixComments(ctx, p, pr.Number)
				}
			}
		}

		// Merge conflict detection on batch PRs
		if strings.HasPrefix(pr.Head, "herd/batch/") {
			// List endpoint doesn't populate Mergeable — call Get
			fullPR, getErr := p.PullRequests().Get(ctx, pr.Number)
			if getErr != nil {
				continue
			}
			if fullPR.Mergeable {
				// Conflict resolved — clean up label
				_ = p.Issues().RemoveLabels(ctx, pr.Number, []string{issues.RebasePending})
			} else {
				if hasRebasePendingLabel(ctx, p, pr.Number) {
					continue
				}

				// Parse milestone from batch branch name
				batchNum, parseErr := integrator.ParseBatchBranchMilestone(pr.Head)
				if parseErr != nil {
					continue
				}
				ms, msErr := p.Milestones().Get(ctx, batchNum)
				if msErr != nil {
					continue
				}

				defaultBranch, dbErr := p.Repository().GetDefaultBranch(ctx)
				if dbErr != nil {
					continue
				}

				// Add label BEFORE dispatching to prevent duplicate dispatches
				_ = p.Issues().AddLabels(ctx, pr.Number, []string{issues.RebasePending})

				// Dispatch rebase conflict resolution worker
				issueNum, resolveErr := dispatchRebaseConflictWorker(ctx, p, cfg, ms, pr.Head, defaultBranch)
				if resolveErr != nil {
					// Remove label if dispatch failed so next patrol retries
					_ = p.Issues().RemoveLabels(ctx, pr.Number, []string{issues.RebasePending})
					continue
				}

				if issueNum == 0 {
					// Cap reached — no worker was dispatched, remove label so next patrol retries after cap clears
					_ = p.Issues().RemoveLabels(ctx, pr.Number, []string{issues.RebasePending})
					continue
				}

				// Comment on the PR explaining what happened
				_ = p.Issues().AddComment(ctx, pr.Number, fmt.Sprintf(
					"⚠️ **HerdOS Monitor Alert**\n\nThis batch PR has merge conflicts with `%s`. A conflict resolution worker has been dispatched to rebase the branch.\n\n%s",
					defaultBranch, buildMentions(cfg.Monitor.NotifyUsers)))

				result.ConflictDetected++
			}
		}
	}

	// Dispatch stale ready issues
	if cfg.Monitor.AutoRedispatch {
		readyIssues, err := p.Issues().List(ctx, platform.IssueFilters{
			State:  "open",
			Labels: []string{issues.StatusReady},
		})
		if err != nil {
			return nil, fmt.Errorf("listing ready issues: %w", err)
		}

		// Re-fetch active runs for accurate concurrency count
		// (stale detection above may have cancelled some)
		currentActiveRuns, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress"})
		if err != nil {
			return nil, fmt.Errorf("listing active runs for ready dispatch: %w", err)
		}
		remaining := cfg.Workers.MaxConcurrent - len(currentActiveRuns)

		for _, issue := range readyIssues {
			if remaining <= 0 {
				break
			}
			if issue.Milestone == nil {
				continue
			}
			// Skip manual tasks
			if issues.HasLabel(issue.Labels, issues.TypeManual) {
				continue
			}
			// Skip if there is already an active run for this issue
			hasActiveRun := false
			for _, run := range currentActiveRuns {
				if run.Inputs["issue_number"] == fmt.Sprintf("%d", issue.Number) {
					hasActiveRun = true
					break
				}
			}
			if hasActiveRun {
				continue
			}
			// Check stale threshold: skip if updated recently
			if cfg.Monitor.StaleThresholdMinutes > 0 && time.Since(issue.UpdatedAt) < time.Duration(cfg.Monitor.StaleThresholdMinutes)*time.Minute {
				continue
			}
			// Check dependencies are all done
			parsed, err := issues.ParseBody(issue.Body)
			if err != nil {
				continue
			}
			depsComplete := true
			for _, dep := range parsed.FrontMatter.DependsOn {
				depIssue, err := p.Issues().Get(ctx, dep)
				if err != nil {
					depsComplete = false
					break
				}
				if !isDepComplete(depIssue) {
					depsComplete = false
					break
				}
			}
			if !depsComplete {
				continue
			}
			// Dispatch
			batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, slugify(issue.Milestone.Title))
			defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
			if err != nil {
				continue
			}
			_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusReady})
			_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusInProgress})
			_, err = p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
				"issue_number":    fmt.Sprintf("%d", issue.Number),
				"batch_branch":    batchBranch,
				"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
				"runner_label":    cfg.Workers.RunnerLabel,
			})
			if err != nil {
				_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusInProgress})
				_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
				continue
			}
			remaining--
			result.StaleReadyDispatched++
		}
	}

	return result, nil
}

// hasRetryPendingLabel returns true if the herd/retry-pending label is present on the issue.
// Fails open (returns false on error) so a broken labels API doesn't silence future retry triggers.
func hasRetryPendingLabel(ctx context.Context, p platform.Platform, issueNumber int) bool {
	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return false
	}
	for _, label := range issue.Labels {
		if label == issues.RetryPending {
			return true
		}
	}
	return false
}

// hasCIFixPendingLabel returns true if the herd/ci-fix-pending label is present on the PR.
// Fails open (returns false on error) so a broken labels API doesn't silence future fix triggers.
// The patrol adds the label before posting the /herd fix-ci comment so that a concurrent
// patrol run sees the label and skips, narrowing the duplicate-comment race window.
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

// deleteCIFixComments removes all /herd fix-ci comments from the PR and removes
// the CIFixPending label, resetting state so future CI failures can trigger a new fix cycle.
func deleteCIFixComments(ctx context.Context, p platform.Platform, prNumber int) {
	defer func() {
		_ = p.Issues().RemoveLabels(ctx, prNumber, []string{issues.CIFixPending})
	}()
	comments, err := p.Issues().ListComments(ctx, prNumber)
	if err != nil {
		return
	}
	for _, c := range comments {
		if strings.TrimSpace(c.Body) == "/herd fix-ci" {
			_ = p.Issues().DeleteComment(ctx, c.ID)
		}
	}
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

// hasRebasePendingLabel returns true if the herd/rebase-pending label is present on the PR.
// Fails open (returns false on error) so a broken labels API doesn't silence future rebase triggers.
func hasRebasePendingLabel(ctx context.Context, p platform.Platform, prNumber int) bool {
	issue, err := p.Issues().Get(ctx, prNumber)
	if err != nil {
		return false
	}
	for _, label := range issue.Labels {
		if label == issues.RebasePending {
			return true
		}
	}
	return false
}

func dispatchRebaseConflictWorker(ctx context.Context, p platform.Platform, cfg *config.Config, ms *platform.Milestone, batchBranch, defaultBranch string) (int, error) {
	return integrator.DispatchRebaseConflictWorker(ctx, p, cfg, ms, batchBranch, defaultBranch)
}

func isDepComplete(issue *platform.Issue) bool {
	return issue.State == "closed" || issues.HasLabel(issue.Labels, issues.StatusDone)
}

// slugify converts a string to a URL-friendly slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var result []rune
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			result = append(result, r)
		} else if r == ' ' || r == '_' || r == '-' {
			result = append(result, '-')
		}
	}
	// Collapse multiple dashes
	parts := strings.Split(string(result), "-")
	var filtered []string
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	slug := strings.Join(filtered, "-")
	// Truncate to reasonable length
	if len(slug) > 50 {
		slug = slug[:50]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}
