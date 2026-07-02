package integrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/dag"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// ConsolidateParams holds the parameters for consolidating a worker branch.
type ConsolidateParams struct {
	RunID    int64
	RepoRoot string
}

// ConsolidateResult holds the result of a consolidation.
type ConsolidateResult struct {
	IssueNumber      int
	WorkerBranch     string
	Merged           bool
	NoOp             bool
	ConflictDetected bool
	ConflictIssue    int
}

// AdvanceParams holds the parameters for advancing tiers.
type AdvanceParams struct {
	RunID    int64
	RepoRoot string
}

// AdvanceResult holds the result of a tier advancement.
type AdvanceResult struct {
	TierComplete    bool
	AllComplete     bool
	DispatchedCount int
	BatchPRNumber   int
}

// ReviewParams holds the parameters for reviewing a batch PR.
type ReviewParams struct {
	RunID             int64
	PRNumber          int // Alternative to RunID — used by pull_request_review trigger
	BatchNumber       int // Alternative to RunID — used by advance-on-close
	RepoRoot          string
	ExtraInstructions string // Optional extra instructions appended to the review system prompt
	// Manual is true when this review was triggered by a /herd review or
	// /herd integrate slash command. Manual runs bypass the
	// herd/stable-disagreement circuit breaker.
	Manual bool
}

// ReviewResult holds the result of a batch PR review.
type ReviewResult struct {
	Approved         bool
	FixIssues        []int
	FixCycle         int
	MaxCyclesHit     bool
	BatchPRNumber    int
	AllCreatesFailed bool // true when issues were found but every fix-issue Create call failed
	FindingsCount    int  // number of review findings (agent comments); set when AllCreatesFailed is true
	// SkippedDuplicateApprovedHead is true when an automatic review was skipped
	// because the current PR head SHA already has an approved Herd review result.
	SkippedDuplicateApprovedHead bool
	// SkipReason is a human-readable reason for a skipped review that the CLI can print.
	SkipReason string
	// HeadSHA is the PR head SHA considered by the review, set for duplicate-skip diagnostics when available.
	HeadSHA string
	// ManualInterventionNeeded is true when the agent produced
	// unparseable output on every attempt and the integrator gave up.
	// The user has been told to re-run `/herd review` manually.
	ManualInterventionNeeded bool
	// StableDisagreement is true when the integrator detected that the
	// reviewer is re-flagging findings that a previous fix worker already
	// determined to be no-ops. The batch PR is labelled
	// herd/stable-disagreement and a help-needed comment is posted; the
	// cycle is halted so the user can decide how to proceed.
	StableDisagreement bool
}

// ReviewStandaloneParams holds parameters for reviewing a non-batch PR.
type ReviewStandaloneParams struct {
	PRNumber          int
	RepoRoot          string
	ExtraInstructions string
}

// ReviewStandaloneResult holds the result of a standalone PR review.
type ReviewStandaloneResult struct {
	FindingsCount int
}

// Consolidate merges completed worker branches into the batch branch.
// params.RunID identifies the triggering run; consolidation processes ALL
// done-labeled issues in the milestone whose worker branches still exist
// on the remote, making the operation idempotent and self-healing.
// The returned *ConsolidateResult reflects the TRIGGERING issue's outcome;
// other workers in the milestone are processed for side effects only.
func Consolidate(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, params ConsolidateParams) (*ConsolidateResult, error) {
	run, err := p.Workflows().GetRun(ctx, params.RunID)
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", params.RunID, err)
	}

	issueNumStr, ok := run.Inputs["issue_number"]
	if !ok {
		return nil, fmt.Errorf("run %d has no issue_number input", params.RunID)
	}
	issueNumber, err := strconv.Atoi(issueNumStr)
	if err != nil {
		return nil, fmt.Errorf("invalid issue_number %q in run %d: %w", issueNumStr, params.RunID, err)
	}

	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", issueNumber, err)
	}
	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d has no milestone", issueNumber)
	}
	if isBatchComplete(issue.Milestone) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", issue.Milestone.Number)
		return &ConsolidateResult{IssueNumber: issueNumber, NoOp: true}, nil
	}

	triggerWorkerBranch := fmt.Sprintf("herd/worker/%d-%s", issueNumber, planner.Slugify(issue.Title))
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	// Failure/cancellation: relabel the trigger and return — do NOT scan other
	// candidates in this case (preserves the trigger semantics for failed runs).
	if run.Conclusion == "failure" || run.Conclusion == "cancelled" {
		status := issues.StatusLabel(issue.Labels)
		if status != issues.StatusFailed {
			_ = p.Issues().RemoveLabels(ctx, issueNumber, []string{status})
			_ = p.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
		}
		return &ConsolidateResult{IssueNumber: issueNumber, Merged: false}, nil
	}

	// If the trigger's own worker branch is gone AND it's a conflict-resolution
	// issue, run the existing close-stale + retry-original path before scanning.
	triggerBranchExists := true
	if _, err := p.Repository().GetBranchSHA(ctx, triggerWorkerBranch); err != nil {
		triggerBranchExists = false
	}
	if !triggerBranchExists {
		parsed, parseErr := issues.ParseBody(issue.Body)
		if parseErr == nil && parsed.FrontMatter.ConflictResolution {
			closeStaleConflictIssues(ctx, p, issue.Milestone)
			retryConflictOriginIssues(ctx, p, cfg, issue, batchBranch)
		}
	}

	// Build candidate list: all done-labeled issues in the milestone whose
	// worker branches still exist on the remote.
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &issue.Milestone.Number})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	type candidate struct {
		iss          *platform.Issue
		workerBranch string
	}
	seen := make(map[int]bool)
	var candidates []candidate
	addCandidate := func(iss *platform.Issue) {
		if iss == nil || seen[iss.Number] {
			return
		}
		if issues.StatusLabel(iss.Labels) != issues.StatusDone {
			return
		}
		wb := fmt.Sprintf("herd/worker/%d-%s", iss.Number, planner.Slugify(iss.Title))
		if _, err := p.Repository().GetBranchSHA(ctx, wb); err != nil {
			return
		}
		seen[iss.Number] = true
		candidates = append(candidates, candidate{iss: iss, workerBranch: wb})
	}
	for _, iss := range allIssues {
		addCandidate(iss)
	}
	// Defensive: ensure the triggering issue is included if it qualifies and the
	// list response somehow missed it.
	addCandidate(issue)

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].iss.Number < candidates[j].iss.Number
	})

	if len(candidates) == 0 {
		return &ConsolidateResult{IssueNumber: issueNumber, NoOp: true, Merged: false}, nil
	}

	if err := g.ConfigureIdentity("HerdOS Integrator", "herd@herd-os.com"); err != nil {
		return nil, fmt.Errorf("configuring git identity: %w", err)
	}
	if err := g.Fetch("origin"); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}

	var triggerResult *ConsolidateResult
	for _, c := range candidates {
		result, herr := consolidateWorkerBranch(ctx, p, g, cfg, params, c.iss, issue.Milestone, c.workerBranch, batchBranch)
		if herr != nil {
			if c.iss.Number == issueNumber {
				return nil, herr
			}
			fmt.Printf("Warning: failed to consolidate worker branch %s for issue #%d: %v\n", c.workerBranch, c.iss.Number, herr)
			continue
		}
		if c.iss.Number == issueNumber {
			triggerResult = result
		} else {
			fmt.Printf("Consolidated worker branch %s for issue #%d (merged=%v noop=%v conflict=%v)\n",
				c.workerBranch, c.iss.Number, result.Merged, result.NoOp, result.ConflictDetected)
		}
	}

	if triggerResult == nil {
		return &ConsolidateResult{IssueNumber: issueNumber, NoOp: true, Merged: false}, nil
	}
	return triggerResult, nil
}

// consolidateWorkerBranch performs a single worker-branch merge into the batch
// branch. It returns the per-branch result and a non-nil error only for fatal
// infrastructure errors that should abort the whole loop (e.g. failure to
// checkout the batch branch). Conflict and push failures are non-fatal: they
// label the issue failed (or dispatch a resolver) and return a normal result
// so the caller continues with the next worker.
func consolidateWorkerBranch(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, params ConsolidateParams, iss *platform.Issue, ms *platform.Milestone, workerBranch, batchBranch string) (*ConsolidateResult, error) {
	if err := g.CheckoutReset(batchBranch); err != nil {
		return nil, fmt.Errorf("checking out batch branch: %w", err)
	}

	// Skip if worker branch is already contained in batch branch
	// (merge base equals worker branch tip → already merged).
	workerSHA, shaErr := g.RevParse("origin/" + workerBranch)
	batchSHA, batchShaErr := g.RevParse(batchBranch)
	if shaErr == nil && batchShaErr == nil {
		mergeBase, mbErr := g.MergeBase("origin/"+workerBranch, batchBranch)
		if mbErr == nil && mergeBase == workerSHA {
			fmt.Printf("Worker branch %s is already contained in batch branch, skipping merge.\n", workerBranch)
			if err := p.Repository().DeleteBranch(ctx, workerBranch); err != nil {
				fmt.Printf("Warning: failed to delete already-merged worker branch %s: %v\n", workerBranch, err)
			}
			return &ConsolidateResult{
				IssueNumber:  iss.Number,
				WorkerBranch: workerBranch,
				Merged:       false,
				NoOp:         true,
			}, nil
		}
	}
	_ = batchSHA // used only for error check above

	if err := g.Merge("origin/" + workerBranch); err != nil {
		_ = g.AbortMerge()

		if cfg.Integrator.OnConflict == "dispatch-resolver" {
			return handleConflictResolution(ctx, p, cfg, iss, ms, workerBranch, batchBranch)
		}

		// Default: notify — comment on issue with @mentions and relabel as failed.
		mentions := ""
		if len(cfg.Monitor.NotifyUsers) > 0 {
			parts := make([]string, len(cfg.Monitor.NotifyUsers))
			for i, u := range cfg.Monitor.NotifyUsers {
				parts[i] = "@" + u
			}
			mentions = "\n\n/cc " + strings.Join(parts, " ")
		}
		_ = p.Issues().AddComment(ctx, iss.Number, fmt.Sprintf(
			"⚠️ **HerdOS Integrator**\n\nMerge conflict detected when consolidating `%s` into `%s`.\n\nManual resolution required.%s",
			workerBranch, batchBranch, mentions))
		_ = p.Issues().RemoveLabels(ctx, iss.Number, []string{issues.StatusDone})
		_ = p.Issues().AddLabels(ctx, iss.Number, []string{issues.StatusFailed})
		fmt.Printf("Warning: merge conflict for %s into %s (issue #%d relabeled as failed)\n", workerBranch, batchBranch, iss.Number)
		return &ConsolidateResult{
			IssueNumber:      iss.Number,
			WorkerBranch:     workerBranch,
			ConflictDetected: true,
		}, nil
	}

	// Clean up progress tracking artifacts (both new per-issue and legacy formats).
	needsCommit := false
	progressDir := filepath.Join(params.RepoRoot, ".herd", "progress")
	if _, statErr := os.Stat(progressDir); statErr == nil {
		if rmErr := g.RmDir(".herd/progress/"); rmErr != nil {
			fmt.Printf("Warning: failed to git rm .herd/progress/: %v\n", rmErr)
		} else {
			needsCommit = true
		}
	}
	legacyFile := filepath.Join(params.RepoRoot, "WORKER_PROGRESS.md")
	if _, statErr := os.Stat(legacyFile); statErr == nil {
		if rmErr := g.Rm("WORKER_PROGRESS.md"); rmErr != nil {
			fmt.Printf("Warning: failed to git rm WORKER_PROGRESS.md: %v\n", rmErr)
		} else {
			needsCommit = true
		}
	}
	if needsCommit {
		if commitErr := g.Commit("Remove worker progress tracking files"); commitErr != nil {
			fmt.Printf("Warning: failed to commit progress file removal: %v\n", commitErr)
			_ = g.ResetHead()
		}
	}

	if err := g.Push("origin", batchBranch); err != nil {
		_ = p.Issues().RemoveLabels(ctx, iss.Number, []string{issues.StatusDone})
		_ = p.Issues().AddLabels(ctx, iss.Number, []string{issues.StatusFailed})
		_ = p.Issues().AddComment(ctx, iss.Number, fmt.Sprintf(
			"⚠️ **HerdOS Integrator**\n\nCould not push consolidated batch branch `%s` (non-fast-forward). This issue will be retried.\n\nYou can also retry with `/herd integrate` on this issue.",
			batchBranch))
		fmt.Printf("Warning: push failed for batch branch %s: %v (issue #%d relabeled as failed)\n", batchBranch, err, iss.Number)
		return &ConsolidateResult{
			IssueNumber:  iss.Number,
			WorkerBranch: workerBranch,
			Merged:       false,
		}, nil
	}

	removeBatchPRRebasePending(ctx, p, batchBranch)
	closeStaleConflictIssues(ctx, p, ms)
	retryConflictOriginIssues(ctx, p, cfg, iss, batchBranch)

	if err := p.Repository().DeleteBranch(ctx, workerBranch); err != nil {
		fmt.Printf("Warning: failed to delete worker branch %s: %v\n", workerBranch, err)
	}

	return &ConsolidateResult{
		IssueNumber:  iss.Number,
		WorkerBranch: workerBranch,
		Merged:       true,
	}, nil
}

// Advance checks if the current tier is complete and dispatches the next tier.
// If all tiers are complete, it opens the batch PR.
func Advance(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, params AdvanceParams) (*AdvanceResult, error) {
	// Get the run to find the issue and milestone
	run, err := p.Workflows().GetRun(ctx, params.RunID)
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", params.RunID, err)
	}

	issueNumStr := run.Inputs["issue_number"]
	issueNumber, err := strconv.Atoi(issueNumStr)
	if err != nil {
		return nil, fmt.Errorf("invalid issue_number: %w", err)
	}

	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", issueNumber, err)
	}
	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d has no milestone", issueNumber)
	}

	ms := issue.Milestone
	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &AdvanceResult{}, nil
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	// List all issues in the milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	// Build DAG from issue dependencies
	tiers, err := buildTiersFromIssues(allIssues)
	if err != nil {
		return nil, fmt.Errorf("building tiers: %w", err)
	}

	// Find the tier that the triggering issue belongs to
	triggerTier := -1
	for t, tier := range tiers {
		for _, num := range tier {
			if num == issueNumber {
				triggerTier = t
				break
			}
		}
		if triggerTier >= 0 {
			break
		}
	}
	if triggerTier < 0 {
		fmt.Printf("Warning: issue #%d not found in any tier of milestone #%d (possibly partial API response); skipping advance\n", issueNumber, ms.Number)
		return &AdvanceResult{}, nil
	}

	// Check if the triggering issue's tier is complete
	tierComplete := true
	tierStuck := false
	for _, num := range tiers[triggerTier] {
		iss := findIssue(allIssues, num)
		if iss == nil {
			continue
		}
		status := issues.StatusLabel(iss.Labels)
		if status == issues.StatusFailed {
			tierStuck = true
			tierComplete = false
			break
		}
		if !isIssueComplete(iss) {
			tierComplete = false
		}
	}

	if tierStuck {
		return &AdvanceResult{TierComplete: false}, nil
	}

	if !tierComplete {
		// Tier not done yet — but there may be ready issues that weren't dispatched
		// due to concurrency limits on the previous advance. Try to dispatch them now.
		dispatched, err := dispatchReadyIssues(ctx, p, cfg, tiers[triggerTier], allIssues, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("dispatching remaining tier issues: %w", err)
		}
		if dispatched > 0 {
			fmt.Printf("Tier %d not yet complete. Dispatched %d remaining issues.\n", triggerTier, dispatched)
		} else {
			fmt.Printf("Tier %d not yet complete.\n", triggerTier)
		}
		return &AdvanceResult{TierComplete: false, DispatchedCount: dispatched}, nil
	}

	// Tier is complete — check if this was the last tier
	if triggerTier+1 >= len(tiers) {
		if !hasCompleteIssueList(allIssues, ms) {
			fmt.Printf("Warning: milestone #%d has %d expected issues (%d open + %d closed) but only %d were returned by the API; skipping batch PR to avoid premature open\n",
				ms.Number, ms.OpenIssues+ms.ClosedIssues, ms.OpenIssues, ms.ClosedIssues, len(allIssues))
			return &AdvanceResult{TierComplete: true}, nil
		}
		// All tiers done — open batch PR
		prNum, err := openBatchPR(ctx, p, g, cfg, ms, allIssues, tiers, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("opening batch PR: %w", err)
		}
		return &AdvanceResult{AllComplete: true, TierComplete: true, BatchPRNumber: prNum}, nil
	}

	// Dispatch next tier
	dispatched, err := dispatchReadyIssues(ctx, p, cfg, tiers[triggerTier+1], allIssues, batchBranch)
	if err != nil {
		return nil, fmt.Errorf("dispatching next tier: %w", err)
	}

	return &AdvanceResult{
		TierComplete:    true,
		DispatchedCount: dispatched,
	}, nil
}

// dispatchReadyIssues dispatches ready/blocked issues from a tier, respecting
// concurrency limits. Returns the number of issues dispatched.
func dispatchReadyIssues(ctx context.Context, p platform.Platform, cfg *config.Config, tierIssues []int, allIssues []*platform.Issue, batchBranch string) (int, error) {
	inProgress, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress", WorkflowFileName: "herd-worker.yml"})
	if err != nil {
		return 0, fmt.Errorf("counting active workers (in_progress): %w", err)
	}
	queued, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "queued", WorkflowFileName: "herd-worker.yml"})
	if err != nil {
		return 0, fmt.Errorf("counting active workers (queued): %w", err)
	}
	activeRuns := make([]*platform.Run, 0, len(inProgress)+len(queued))
	activeRuns = append(activeRuns, inProgress...)
	activeRuns = append(activeRuns, queued...)
	remaining := cfg.Workers.MaxConcurrent - len(activeRuns)

	// Build a set of issue numbers that already have an active run, to prevent
	// duplicate dispatch (e.g. if a previous dispatch retried after GitHub
	// queued the workflow but returned a 5xx).
	activeByIssue := make(map[string]bool, len(activeRuns))
	for _, r := range activeRuns {
		if n, ok := r.Inputs["issue_number"]; ok && n != "" {
			activeByIssue[n] = true
		}
	}

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting default branch: %w", err)
	}

	dispatched := 0
	for _, num := range tierIssues {
		issue := findIssue(allIssues, num)
		if issue == nil {
			continue
		}

		status := issues.StatusLabel(issue.Labels)
		// Only act on blocked or ready issues — skip done, in-progress, failed
		if status != issues.StatusBlocked && status != issues.StatusReady {
			continue
		}

		issueKey := fmt.Sprintf("%d", num)
		if activeByIssue[issueKey] {
			fmt.Printf("warning: issue #%d already has an active worker run; skipping dispatch\n", num)
			continue
		}

		// Unblock if still blocked
		if status == issues.StatusBlocked {
			_ = p.Issues().RemoveLabels(ctx, num, []string{issues.StatusBlocked})
		}

		// Manual tasks get unblocked but not dispatched
		if issues.HasLabel(issue.Labels, issues.TypeManual) {
			if status == issues.StatusBlocked {
				_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusReady})
			}
			continue
		}

		if dispatched >= remaining {
			// At capacity — just mark ready, don't dispatch
			if status == issues.StatusBlocked {
				_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusReady})
			}
			continue
		}

		// Forward any manual-task dependency findings into this issue's body so
		// the worker (which reads only its own issue body) sees them.
		injectManualDepFindings(ctx, p, issue, allIssues)

		// Dispatch: remove ready if present, add in-progress
		if status == issues.StatusReady {
			_ = p.Issues().RemoveLabels(ctx, num, []string{issues.StatusReady})
		}
		_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusInProgress})
		_, err := p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
			"issue_number":    fmt.Sprintf("%d", num),
			"batch_branch":    batchBranch,
			"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
			"runner_label":    cfg.Workers.RunnerLabel,
		})
		if err != nil {
			// Failed to dispatch — label as failed
			_ = p.Issues().RemoveLabels(ctx, num, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusFailed})
			continue
		}
		dispatched++
	}
	return dispatched, nil
}

// injectManualDepFindings fetches the manual-task dependencies of the given
// issue, extracts each complete (closed or done) manual dep's findings, and
// idempotently injects them into the issue body. It persists the updated body
// via Issues().Update only if at least one new block was added. Body-size is
// guarded against issues.MaxIssueBodyChars: an injection that would push the
// body over the limit is skipped with a warning rather than failing dispatch.
// Failures are logged and non-fatal (dispatch proceeds).
func injectManualDepFindings(ctx context.Context, p platform.Platform, issue *platform.Issue, allIssues []*platform.Issue) {
	parsed, err := issues.ParseBody(issue.Body)
	if err != nil || len(parsed.FrontMatter.DependsOn) == 0 {
		return
	}
	body := issue.Body
	changedAny := false
	for _, depNum := range parsed.FrontMatter.DependsOn {
		dep := findIssue(allIssues, depNum)
		if dep == nil {
			d, gerr := p.Issues().Get(ctx, depNum)
			if gerr != nil || d == nil {
				continue
			}
			dep = d
		}
		if !issues.HasLabel(dep.Labels, issues.TypeManual) {
			continue
		}
		if !isIssueComplete(dep) {
			continue
		}
		if strings.Contains(body, injectedFindingsMarker(dep.Number)) {
			continue
		}
		full, gerr := p.Issues().Get(ctx, dep.Number)
		if gerr != nil || full == nil {
			continue
		}
		comments, cerr := p.Issues().ListComments(ctx, dep.Number)
		if cerr != nil {
			comments = nil
		}
		findings, ok := extractFindings(dep.Number, full.Body, comments)
		if !ok {
			continue
		}
		newBody, changed := injectFindings(body, dep.Number, findings)
		if !changed {
			continue
		}
		if len(newBody) > issues.MaxIssueBodyChars {
			fmt.Printf("warning: skipping findings injection from manual #%d into #%d — would exceed issue body size limit\n", dep.Number, issue.Number)
			continue
		}
		body = newBody
		changedAny = true
	}
	if changedAny {
		if _, uerr := p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{Body: &body}); uerr != nil {
			fmt.Printf("warning: failed to update issue #%d with injected manual-task findings: %v\n", issue.Number, uerr)
			return
		}
		issue.Body = body
	}
}

// buildTiersFromIssues parses issue front matter to build a DAG and compute tiers.
// Returns tiers as slices of issue numbers.
func buildTiersFromIssues(allIssues []*platform.Issue) ([][]int, error) {
	d := dag.New()
	for _, issue := range allIssues {
		d.AddNode(issue.Number)
	}

	for _, issue := range allIssues {
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue // Skip unparseable issues
		}
		for _, dep := range parsed.FrontMatter.DependsOn {
			d.AddEdge(issue.Number, dep)
		}
	}

	return d.Tiers()
}

// isIssueComplete returns true if the issue is closed or has the herd/status:done label.
// Manual tasks are completed by closing them rather than adding labels.
func isIssueComplete(issue *platform.Issue) bool {
	return issue.State == "closed" || issues.HasLabel(issue.Labels, issues.StatusDone)
}

// AdvanceByBatch triggers tier advancement for a batch by milestone number.
// Used when an issue is closed (e.g., manual tasks) rather than via workflow run.
func AdvanceByBatch(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, batchNumber int) (*AdvanceResult, error) {
	ms, err := p.Milestones().Get(ctx, batchNumber)
	if err != nil {
		return nil, fmt.Errorf("getting milestone #%d: %w", batchNumber, err)
	}
	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &AdvanceResult{}, nil
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	// List all issues in the milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	// Build DAG from issue dependencies
	tiers, err := buildTiersFromIssues(allIssues)
	if err != nil {
		return nil, fmt.Errorf("building tiers: %w", err)
	}

	// Find the first incomplete tier
	incompleteTier := -1
	for t, tier := range tiers {
		tierDone := true
		tierStuck := false
		for _, num := range tier {
			iss := findIssue(allIssues, num)
			if iss == nil {
				continue
			}
			status := issues.StatusLabel(iss.Labels)
			if status == issues.StatusFailed {
				tierStuck = true
				tierDone = false
				break
			}
			if !isIssueComplete(iss) {
				tierDone = false
			}
		}
		if tierStuck {
			return &AdvanceResult{TierComplete: false}, nil
		}
		if !tierDone {
			incompleteTier = t
			break
		}
	}

	// All tiers complete
	if incompleteTier == -1 {
		if !hasCompleteIssueList(allIssues, ms) {
			fmt.Printf("Warning: milestone #%d has %d expected issues (%d open + %d closed) but only %d were returned by the API; skipping batch PR to avoid premature open\n",
				ms.Number, ms.OpenIssues+ms.ClosedIssues, ms.OpenIssues, ms.ClosedIssues, len(allIssues))
			return &AdvanceResult{TierComplete: true}, nil
		}
		prNum, err := openBatchPR(ctx, p, g, cfg, ms, allIssues, tiers, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("opening batch PR: %w", err)
		}
		return &AdvanceResult{AllComplete: true, TierComplete: true, BatchPRNumber: prNum}, nil
	}

	// Dispatch incomplete tier's blocked/ready issues
	dispatched, err := dispatchReadyIssues(ctx, p, cfg, tiers[incompleteTier], allIssues, batchBranch)
	if err != nil {
		return nil, fmt.Errorf("dispatching tier issues: %w", err)
	}

	return &AdvanceResult{
		TierComplete:    true,
		DispatchedCount: dispatched,
	}, nil
}

// isBatchComplete checks if the milestone is closed (batch already merged).
func isBatchComplete(ms *platform.Milestone) bool {
	return ms != nil && ms.State == "closed"
}

// hasCompleteIssueList reports whether the listed issues match the milestone's
// expected issue count. It guards against partial GitHub API responses that
// could otherwise make a batch look complete when issues are missing.
// Returns true when len(allIssues) >= ms.OpenIssues + ms.ClosedIssues.
func hasCompleteIssueList(allIssues []*platform.Issue, ms *platform.Milestone) bool {
	if ms == nil {
		return true
	}
	expected := ms.OpenIssues + ms.ClosedIssues
	return len(allIssues) >= expected
}

func findIssue(allIssues []*platform.Issue, number int) *platform.Issue {
	for _, issue := range allIssues {
		if issue.Number == number {
			return issue
		}
	}
	return nil
}

// closeStaleConflictIssues closes open conflict/rebase resolution issues in the
// milestone whose worker branches have already been consolidated or deleted.
// This prevents the monitor from retrying stale issues that would fail with
// non-fast-forward errors.
// removeBatchPRRebasePending removes the rebase-pending label from the batch PR
// after a successful consolidation push.
func removeBatchPRRebasePending(ctx context.Context, p platform.Platform, batchBranch string) {
	prSvc := p.PullRequests()
	if prSvc == nil {
		return
	}
	prs, err := prSvc.List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err != nil || len(prs) == 0 {
		return
	}
	_ = p.Issues().RemoveLabels(ctx, prs[0].Number, []string{issues.RebasePending})
}

// retryConflictOriginIssues checks if the consolidated issue was a conflict
// resolution issue, and if so, re-dispatches the original failed issue whose
// worker branch caused the conflict.
func retryConflictOriginIssues(ctx context.Context, p platform.Platform, cfg *config.Config, issue *platform.Issue, batchBranch string) {
	parsed, err := issues.ParseBody(issue.Body)
	if err != nil || !parsed.FrontMatter.ConflictResolution {
		return
	}

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to get default branch for conflict retry: %v\n", err)
		return
	}

	for _, branch := range parsed.FrontMatter.ConflictingBranches {
		if !strings.HasPrefix(branch, "herd/worker/") {
			continue
		}
		// Extract issue number from branch name: herd/worker/471-slug → 471
		parts := strings.SplitN(strings.TrimPrefix(branch, "herd/worker/"), "-", 2)
		if len(parts) == 0 {
			continue
		}
		origNum, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil {
			continue
		}

		origIssue, getErr := p.Issues().Get(ctx, origNum)
		if getErr != nil {
			continue
		}
		if issues.StatusLabel(origIssue.Labels) != issues.StatusFailed {
			continue
		}

		fmt.Printf("Conflict resolved — re-dispatching original issue #%d.\n", origNum)
		_ = p.Issues().RemoveLabels(ctx, origNum, []string{issues.StatusFailed})
		_ = p.Issues().AddLabels(ctx, origNum, []string{issues.StatusInProgress})
		_, dispatchErr := p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
			"issue_number":    fmt.Sprintf("%d", origNum),
			"batch_branch":    batchBranch,
			"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
			"runner_label":    cfg.Workers.RunnerLabel,
		})
		if dispatchErr != nil {
			_ = p.Issues().RemoveLabels(ctx, origNum, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, origNum, []string{issues.StatusFailed})
			fmt.Printf("Warning: failed to re-dispatch #%d after conflict resolution: %v\n", origNum, dispatchErr)
		}
	}
}

func closeStaleConflictIssues(ctx context.Context, p platform.Platform, ms *platform.Milestone) {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &ms.Number,
	})
	if err != nil {
		fmt.Printf("Warning: failed to list issues for stale conflict cleanup: %v\n", err)
		return
	}

	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if !parsed.FrontMatter.ConflictResolution {
			continue
		}
		// Check if any conflicting worker branch is gone
		branchGone := false
		for _, branch := range parsed.FrontMatter.ConflictingBranches {
			if strings.HasPrefix(branch, "herd/worker/") {
				if _, err := p.Repository().GetBranchSHA(ctx, branch); err != nil {
					branchGone = true
					break
				}
			}
		}
		if !branchGone {
			continue
		}

		// Close the stale issue
		fmt.Printf("Closing stale conflict issue #%d — worker branch already consolidated.\n", iss.Number)
		_ = p.Issues().AddComment(ctx, iss.Number, "Automatically closed — batch branch is already up to date.")
		state := "closed"
		_, _ = p.Issues().Update(ctx, iss.Number, platform.IssueUpdate{State: &state})
	}
}

// findBatchPR returns the open batch PR for a milestone, or nil if none.
func findBatchPR(ctx context.Context, p platform.Platform, ms *platform.Milestone) (*platform.PullRequest, error) {
	if ms == nil {
		return nil, nil
	}
	prSvc := p.PullRequests()
	if prSvc == nil {
		return nil, nil
	}
	branch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
	prs, err := prSvc.List(ctx, platform.PRFilters{State: "open", Head: branch})
	if err != nil {
		return nil, fmt.Errorf("listing batch PRs for %s: %w", branch, err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0], nil
}

// parseWorkerBranchNumber extracts the numeric issue id from a worker branch
// name of the form `herd/worker/<num>-<slug>`. Returns 0 if the branch does
// not match this shape. Using exact-number parsing (instead of prefix
// matching) avoids the ambiguous case where one issue's branch prefix
// (e.g. `herd/worker/10-`) is itself a prefix of another's (`herd/worker/100-foo`).
func parseWorkerBranchNumber(branch string) int {
	const prefix = "herd/worker/"
	if !strings.HasPrefix(branch, prefix) {
		return 0
	}
	rest := branch[len(prefix):]
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:dash])
	if err != nil {
		return 0
	}
	return n
}

// buildCascadeChain walks the chain of conflict-resolution issues in the
// milestone, starting from the current failing issue and following
// ConflictingBranches back to the original failing worker. Returns issue
// numbers in chronological order (oldest first, current failing issue last).
func buildCascadeChain(ctx context.Context, p platform.Platform, ms *platform.Milestone, currentIssue *platform.Issue) ([]int, error) {
	if currentIssue == nil {
		return nil, nil
	}
	if ms == nil {
		return []int{currentIssue.Number}, nil
	}
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &ms.Number})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}
	byNumber := map[int]*platform.Issue{}
	for _, iss := range allIssues {
		byNumber[iss.Number] = iss
	}
	findParent := func(parentWorkerBranch string) int {
		num := parseWorkerBranchNumber(parentWorkerBranch)
		if num == 0 {
			return 0
		}
		if _, ok := byNumber[num]; !ok {
			return 0
		}
		return num
	}

	chain := []int{currentIssue.Number}
	visited := map[int]bool{currentIssue.Number: true}
	cursor := currentIssue
	for {
		parsed, parseErr := issues.ParseBody(cursor.Body)
		if parseErr != nil || !parsed.FrontMatter.ConflictResolution || len(parsed.FrontMatter.ConflictingBranches) == 0 {
			break
		}
		parentNum := findParent(parsed.FrontMatter.ConflictingBranches[0])
		if parentNum == 0 || visited[parentNum] {
			break
		}
		visited[parentNum] = true
		chain = append([]int{parentNum}, chain...)
		parent, ok := byNumber[parentNum]
		if !ok {
			break
		}
		cursor = parent
	}
	return chain, nil
}

// cascadeKind distinguishes the two scenarios that can exhaust the conflict
// resolution cap. It controls the wording of the recovery instructions in
// markCascadeFailed — the merge path tells the user to fix a worker branch,
// the rebase path tells them to fix the batch branch against the default.
type cascadeKind int

const (
	cascadeKindMerge  cascadeKind = iota // worker → batch
	cascadeKindRebase                    // batch → default
)

// markCascadeFailed performs all cascade-failure side effects: label the
// failing issue as failed, label the batch PR with herd/cascade-failed,
// and post a detailed notify comment on the batch PR. If the batch PR
// cannot be found, falls back to a comment on the failing issue.
//
// failingBranch is the worker branch the user must inspect/fix. The design
// forbids force-pushing the batch branch, so both cascade kinds instruct
// the user to fix a worker branch and force-push that. targetBranch is what
// failingBranch must merge cleanly into (batch branch for merge cascades,
// default branch for rebase cascades).
func markCascadeFailed(ctx context.Context, p platform.Platform, cfg *config.Config, ms *platform.Milestone, issue *platform.Issue, failingBranch, targetBranch string, kind cascadeKind) {
	// Relabel the failing issue from done → failed.
	if issue != nil {
		_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusDone})
		_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
	}

	pr, prErr := findBatchPR(ctx, p, ms)
	if prErr != nil {
		// Don't swallow lookup errors: a transient API failure must be
		// visible in logs or the rich PR comment will be silently lost.
		fmt.Printf("Warning: failed to look up batch PR for cascade-failed notification: %v\n", prErr)
	}
	if prErr != nil || pr == nil {
		// Fall back to on-issue comment so cascade failure is not silent.
		if issue != nil {
			_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
				"⚠️ **HerdOS Integrator**\n\nMerge conflict cascade exhausted (%d attempts). Manual intervention required.",
				cfg.Integrator.MaxConflictResolutionAttempts))
		}
		return
	}

	_ = p.Issues().AddLabels(ctx, pr.Number, []string{issues.CascadeFailed})

	var chain []int
	if issue != nil {
		chain, _ = buildCascadeChain(ctx, p, ms, issue)
	}

	parts := make([]string, len(chain))
	for i, n := range chain {
		if i == len(chain)-1 {
			parts[i] = fmt.Sprintf("#%d (failed)", n)
		} else {
			parts[i] = fmt.Sprintf("#%d", n)
		}
	}
	chainStr := strings.Join(parts, " → ")

	mentions := ""
	if len(cfg.Monitor.NotifyUsers) > 0 {
		us := make([]string, len(cfg.Monitor.NotifyUsers))
		for i, u := range cfg.Monitor.NotifyUsers {
			us[i] = "@" + u
		}
		mentions = "\n\n/cc " + strings.Join(us, " ")
	}

	origNum := 0
	if len(chain) > 0 {
		origNum = chain[0]
	} else if issue != nil {
		origNum = issue.Number
	}
	closeStep := ""
	if origNum > 0 {
		closeStep = fmt.Sprintf(
			"\n3. Or close the original failing issue (#%d) if the work is no longer needed, then post `/herd integrate` to advance past it.",
			origNum)
	}

	var recovery string
	switch kind {
	case cascadeKindRebase:
		// Rebase-against-main path: instruct the user to fix the failing
		// resolver's worker branch, not the batch branch. Force-pushing the
		// batch branch is forbidden by the design.
		recovery = fmt.Sprintf(
			"1. Inspect the failing worker branch locally:\n"+
				"   ```\n   git fetch origin && git checkout %s\n   ```\n"+
				"2. Either merge the latest `%s` into the worker branch and resolve conflicts manually, then:\n"+
				"   ```\n   git push --force origin %s\n   ```\n"+
				"   and post `/herd integrate` on this PR.%s",
			failingBranch, targetBranch, failingBranch, closeStep)
	default:
		recovery = fmt.Sprintf(
			"1. Inspect the failing worker branch locally:\n"+
				"   ```\n   git fetch origin && git checkout %s\n   ```\n"+
				"2. Either rebase the worker branch onto the current batch branch and resolve conflicts manually, then:\n"+
				"   ```\n   git push --force origin %s\n   ```\n"+
				"   and post `/herd integrate` on this PR.%s",
			failingBranch, failingBranch, closeStep)
	}

	body := fmt.Sprintf(
		"⚠️ Conflict resolution cascade failed\n\n"+
			"The integrator attempted %d times to resolve a merge conflict and could not produce a clean merge.\n\n"+
			"**Cascade chain:** %s\n\n"+
			"**Suggested next steps (in order):**\n\n"+
			"%s\n\n"+
			"Herd will not retry conflict resolution while the `herd/cascade-failed` label is present on this PR. Remove the label and post `/herd integrate` once you've handled the underlying issue.%s",
		cfg.Integrator.MaxConflictResolutionAttempts, chainStr, recovery, mentions)

	_ = p.Issues().AddComment(ctx, pr.Number, body)
}

// cascadePausedComment is the body posted on the batch PR when conflict
// resolution is paused (i.e. the cascade-failed label is set). It is kept as
// a package-level constant so the rate-limiter helper can detect prior
// instances by exact prefix match instead of fragile substring guessing.
const cascadePausedComment = "⚠️ Conflict resolution is paused — batch is in cascade-failed state. Resolve the existing failure manually, then remove the `herd/cascade-failed` label and post `/herd integrate` to resume."

// cascadePausedMarker is a stable prefix used to detect an existing paused
// comment in the PR's comment history.
const cascadePausedMarker = "⚠️ Conflict resolution is paused"

// cascadeFailedMarker is a stable prefix of the rich markCascadeFailed comment
// body. We use it as a "cascade event" boundary when deciding whether the
// paused notice has already been posted *for the current cascade event*.
const cascadeFailedMarker = "⚠️ Conflict resolution cascade failed"

// cascadeFailedPR returns the batch PR if it currently carries the
// herd/cascade-failed label, signalling that the conflict-resolution cascade
// is paused. Returns nil if the label is absent, the PR cannot be found, or
// the lookup fails. Callers use this as the single circuit-breaker check
// across handleConflictResolution, DispatchRebaseConflictWorker, and
// handleRebaseConflictResolution.
func cascadeFailedPR(ctx context.Context, p platform.Platform, ms *platform.Milestone) *platform.PullRequest {
	pr, err := findBatchPR(ctx, p, ms)
	if err != nil || pr == nil {
		return nil
	}
	if !issues.HasLabel(pr.Labels, issues.CascadeFailed) {
		return nil
	}
	return pr
}

// postCascadePausedNotice posts the paused-state notice on the batch PR, but
// only if we haven't already posted one for the current cascade event. A
// "cascade event" is anchored by the rich comment that markCascadeFailed
// emits when the budget is exhausted; we walk comments newest-first and
// suppress the notice if we encounter another paused notice before that
// cascade-failed marker. Errors fetching comments are non-fatal — the worst
// case is a duplicate comment, which is what we are trying to avoid but is
// still safer than dropping the notice entirely.
func postCascadePausedNotice(ctx context.Context, p platform.Platform, prNumber int) {
	if comments, err := p.Issues().ListComments(ctx, prNumber); err == nil {
		for i := len(comments) - 1; i >= 0; i-- {
			body := comments[i].Body
			if strings.HasPrefix(body, cascadeFailedMarker) {
				break
			}
			if strings.HasPrefix(body, cascadePausedMarker) {
				return
			}
		}
	}
	_ = p.Issues().AddComment(ctx, prNumber, cascadePausedComment)
}

func handleConflictResolution(ctx context.Context, p platform.Platform, cfg *config.Config, issue *platform.Issue, ms *platform.Milestone, workerBranch, batchBranch string) (*ConsolidateResult, error) {
	// Count existing conflict-resolution issues in this milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	conflictCount := 0
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.ConflictResolution {
			conflictCount++
		}
	}

	// Circuit breaker: if the batch PR is in cascade-failed state, refuse to
	// create any new conflict-resolution issues. The label is removed manually
	// by a human after they handle the underlying problem.
	if pr := cascadeFailedPR(ctx, p, ms); pr != nil {
		postCascadePausedNotice(ctx, p, pr.Number)
		_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusDone})
		_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
		return &ConsolidateResult{
			IssueNumber:      issue.Number,
			WorkerBranch:     workerBranch,
			ConflictDetected: true,
		}, nil
	}

	if conflictCount >= cfg.Integrator.MaxConflictResolutionAttempts {
		markCascadeFailed(ctx, p, cfg, ms, issue, workerBranch, batchBranch, cascadeKindMerge)
		return &ConsolidateResult{
			IssueNumber:      issue.Number,
			WorkerBranch:     workerBranch,
			ConflictDetected: true,
		}, nil
	}

	// Create conflict-resolution issue
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:             1,
			Batch:               ms.Number,
			Type:                "fix",
			ConflictResolution:  true,
			ConflictingBranches: []string{workerBranch, batchBranch},
		},
		Task: fmt.Sprintf("Resolve merge conflict between `%s` and `%s`.\n\n"+
			"**IMPORTANT:** You are already on your own worker branch (`herd/worker/<this-issue>-<slug>`). Do NOT checkout `%s` or any other branch — your commits must land on your worker branch so the worker framework can push them. The integrator will then merge your worker branch into `%s`.\n\n"+
			"Follow these steps exactly:\n"+
			"1. `git fetch origin`\n"+
			"2. Stay on your current worker branch — do NOT run `git checkout %s`.\n"+
			"3. `git merge origin/%s`\n"+
			"4. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch — only fix the conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) produced by git.\n"+
			"5. `git add <resolved files>`\n"+
			"6. `git commit` (accept the default merge commit message).\n"+
			"7. Do NOT push — the worker framework handles pushing your worker branch.",
			workerBranch, batchBranch, batchBranch, batchBranch, batchBranch, workerBranch),
		Context: fmt.Sprintf("Worker branch `%s` (from issue #%d) conflicts with the batch branch `%s`.", workerBranch, issue.Number, batchBranch),
	})

	truncatedBody, overflow := issues.TruncateIssueBody(body)
	fixIssue, err := p.Issues().Create(ctx,
		fmt.Sprintf("Resolve conflict: #%d (%s)", issue.Number, truncate(issue.Title, 40)),
		truncatedBody,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return nil, fmt.Errorf("creating conflict-resolution issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on conflict-resolution issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	// Relabel original issue from done → failed to block tier advancement
	_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{issues.StatusDone})
	_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})

	// Dispatch resolver worker
	defaultBranch, _ := p.Repository().GetDefaultBranch(ctx)
	_, _ = p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})

	return &ConsolidateResult{
		IssueNumber:      issue.Number,
		WorkerBranch:     workerBranch,
		ConflictDetected: true,
		ConflictIssue:    fixIssue.Number,
	}, nil
}

func openBatchPR(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, ms *platform.Milestone, allIssues []*platform.Issue, tiers [][]int, batchBranch string) (int, error) {
	// Check if PR already exists
	existing, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err == nil && len(existing) > 0 {
		return existing[0].Number, nil // PR already opened
	}

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting default branch: %w", err)
	}

	// Configure git identity for rebase
	if err := g.ConfigureIdentity("HerdOS Integrator", "herd@herd-os.com"); err != nil {
		return 0, fmt.Errorf("configuring git identity: %w", err)
	}

	// Rebase batch branch onto main
	if err := g.Fetch("origin"); err != nil {
		return 0, fmt.Errorf("fetching: %w", err)
	}
	if err := g.Checkout(batchBranch); err != nil {
		return 0, fmt.Errorf("checking out batch branch: %w", err)
	}
	if err := g.Rebase("origin/" + defaultBranch); err != nil {
		_ = g.AbortRebase() // Clean up failed rebase state

		if cfg.Integrator.OnConflict == "dispatch-resolver" {
			if resolveErr := handleRebaseConflictResolution(ctx, p, cfg, ms, batchBranch, defaultBranch); resolveErr != nil {
				fmt.Printf("Warning: failed to dispatch rebase resolver: %v\n", resolveErr)
			}
		}
		// Open the PR un-rebased regardless (notify or dispatch-resolver)
		fmt.Printf("Warning: rebase onto %s failed, opening PR without rebase: %v\n", defaultBranch, err)
	} else {
		// Force push rebased branch (batch branch is HerdOS-owned)
		if err := g.ForcePush("origin", batchBranch); err != nil {
			return 0, fmt.Errorf("force-pushing rebased batch branch: %w", err)
		}
	}

	// Build PR title and body
	title := fmt.Sprintf("[herd] %s (%d tasks)", ms.Title, len(allIssues))
	body := buildBatchPRBody(ms, allIssues, tiers)

	pr, err := p.PullRequests().Create(ctx, title, body, batchBranch, defaultBranch)
	if err != nil {
		// Handle race condition: another integrator run created the PR between our List and Create.
		// GitHub returns 422 with "A pull request already exists" in this case.
		if strings.Contains(strings.ToLower(err.Error()), "pull request already exists") {
			existing, listErr := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
			if listErr == nil && len(existing) > 0 {
				return existing[0].Number, nil
			}
		}
		return 0, fmt.Errorf("creating batch PR: %w", err)
	}

	return pr.Number, nil
}

// buildBatchPRBody creates the markdown body for the batch PR.
func buildBatchPRBody(ms *platform.Milestone, allIssues []*platform.Issue, tiers [][]int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Summary\n\nBatch **%s** — %d tasks across %d tiers.\n\n", ms.Title, len(allIssues), len(tiers))

	// Tasks table
	b.WriteString("## Tasks\n\n")
	b.WriteString("| Issue | Title | Tier | Status |\n")
	b.WriteString("|-------|-------|------|--------|\n")

	for _, issue := range allIssues {
		tier := tierForIssue(issue.Number, tiers)
		status := issues.StatusLabel(issue.Labels)
		if status == "" {
			status = "unknown"
		} else {
			// Strip the prefix for readability
			status = strings.TrimPrefix(status, "herd/status:")
		}
		fmt.Fprintf(&b, "| #%d | %s | %d | %s |\n", issue.Number, issue.Title, tier, status)
	}

	// Worker branches
	b.WriteString("\n## Worker branches\n\n")
	for _, issue := range allIssues {
		branch := fmt.Sprintf("herd/worker/%d-%s", issue.Number, planner.Slugify(issue.Title))
		fmt.Fprintf(&b, "- `%s`\n", branch)
	}

	return b.String()
}

// DispatchRebaseConflictWorker creates a conflict-resolution issue and dispatches
// a worker to rebase the batch branch onto the default branch. It respects the
// max conflict resolution attempts cap. Returns the created issue number, or 0
// if the cap was reached.
func DispatchRebaseConflictWorker(ctx context.Context, p platform.Platform, cfg *config.Config, ms *platform.Milestone, batchBranch, defaultBranch string) (int, error) {
	// Circuit breaker: if the batch PR is in cascade-failed state, refuse to
	// create any new conflict-resolution issues.
	if pr := cascadeFailedPR(ctx, p, ms); pr != nil {
		postCascadePausedNotice(ctx, p, pr.Number)
		return 0, nil
	}

	// Count existing conflict-resolution issues in this milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return 0, fmt.Errorf("listing milestone issues: %w", err)
	}

	conflictCount := 0
	var lastConflictIssue *platform.Issue
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.ConflictResolution {
			conflictCount++
			lastConflictIssue = iss
		}
	}

	if conflictCount >= cfg.Integrator.MaxConflictResolutionAttempts {
		// The design forbids force-pushing the batch branch. When the
		// rebase-against-main cascade exhausts its budget, point the user
		// at the last resolver's worker branch so the recovery
		// instructions tell them to fix and force-push that, not the
		// batch branch. If lastConflictIssue is somehow nil (e.g. cap
		// is 0), fall back to the batch branch — but emit a warning.
		failingBranch := batchBranch
		if lastConflictIssue != nil {
			failingBranch = fmt.Sprintf("herd/worker/%d-%s", lastConflictIssue.Number, planner.Slugify(lastConflictIssue.Title))
		} else {
			fmt.Printf("Warning: cascade-failed (rebase) reached with no conflict-resolution issue to reference; recovery instructions will point at the batch branch\n")
		}
		markCascadeFailed(ctx, p, cfg, ms, lastConflictIssue, failingBranch, defaultBranch, cascadeKindRebase)
		return 0, nil // At cap
	}

	// Create conflict-resolution issue
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:             1,
			Batch:               ms.Number,
			Type:                "fix",
			ConflictResolution:  true,
			ConflictingBranches: []string{batchBranch, defaultBranch},
		},
		Task: fmt.Sprintf("Resolve the conflict between batch branch `%s` and the latest `%s`.\n\n"+
			"**IMPORTANT:** You are already on your own worker branch (`herd/worker/<this-issue>-<slug>`). Do NOT checkout `%s` or `%s` — your commits must land on your worker branch so the worker framework can push them. The integrator will then merge your worker branch into `%s`.\n\n"+
			"Follow these steps exactly:\n"+
			"1. `git fetch origin`\n"+
			"2. Stay on your current worker branch — do NOT run `git checkout %s` or `git checkout %s`.\n"+
			"3. `git merge origin/%s` (this brings the latest default-branch commits into your worker branch).\n"+
			"4. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch — only fix the conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) produced by git.\n"+
			"5. `git add <resolved files>`\n"+
			"6. `git commit` (accept the default merge commit message).\n"+
			"7. Do NOT push — the worker framework handles pushing your worker branch.",
			batchBranch, defaultBranch, batchBranch, defaultBranch, batchBranch, batchBranch, defaultBranch, defaultBranch),
		Context: fmt.Sprintf("Automatic rebase of batch branch `%s` onto `%s` failed due to conflicts.", batchBranch, defaultBranch),
	})

	truncatedBody, overflow := issues.TruncateIssueBody(body)
	fixIssue, err := p.Issues().Create(ctx,
		fmt.Sprintf("Resolve rebase conflict: %s onto %s", batchBranch, defaultBranch),
		truncatedBody,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return 0, fmt.Errorf("creating rebase conflict-resolution issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on rebase conflict-resolution issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	// Dispatch resolver worker
	refBranch, _ := p.Repository().GetDefaultBranch(ctx)
	_, _ = p.Workflows().Dispatch(ctx, "herd-worker.yml", refBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    defaultBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})

	return fixIssue.Number, nil
}

func handleRebaseConflictResolution(ctx context.Context, p platform.Platform, cfg *config.Config, ms *platform.Milestone, batchBranch, defaultBranch string) error {
	// The cascade-failed circuit breaker is enforced inside
	// DispatchRebaseConflictWorker so the two paths cannot diverge.
	_, err := DispatchRebaseConflictWorker(ctx, p, cfg, ms, batchBranch, defaultBranch)
	return err
}

func tierForIssue(number int, tiers [][]int) int {
	for t, tier := range tiers {
		for _, n := range tier {
			if n == number {
				return t
			}
		}
	}
	return 0
}
