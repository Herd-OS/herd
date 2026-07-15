package integrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/reviewdiff"
)

const safetyValveLimit = 10

var errManualInterventionNeeded = errors.New("manual intervention needed")

const manualInterventionReviewComment = "⚠️ **HerdOS Integrator** — Agent review failed to produce valid output after 2 attempts. Run `/herd review` manually to retry."

type authenticatedLoginProvider interface {
	AuthenticatedLogin(ctx context.Context) (string, error)
}

type aggregatedReview struct {
	Result             *agent.ReviewResult
	ChunksReviewed     int
	ManualIntervention bool
	AgentError         error
	ChunkStats         []chunkReviewStats
	DedupeStats        reviewFindingDedupeStats
}

type chunkReviewStats struct {
	ChunkIndex        int
	TotalChunks       int
	HighFindingCount  int
	TotalFindingCount int
	Findings          []agent.ReviewFinding
}

type prMergeState struct {
	MergeableKnown   bool
	Mergeable        bool
	MergeStateStatus string
	Clean            bool
	Blocking         bool
	Unknown          bool
}

func livePRMergeState(pr *platform.PullRequest) prMergeState {
	if pr == nil {
		return prMergeState{Unknown: true}
	}

	status := strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus))
	state := prMergeState{
		MergeableKnown:   pr.MergeableKnown,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: status,
		Clean:            (status == "" && pr.MergeableKnown && pr.Mergeable) || (status == "CLEAN" && (!pr.MergeableKnown || pr.Mergeable)),
		Unknown:          (!pr.MergeableKnown && status == "") || status == "UNKNOWN",
	}
	switch status {
	case "DIRTY", "BLOCKED", "BEHIND", "UNKNOWN", "UNSTABLE", "HAS_HOOKS":
		state.Blocking = true
	}
	return state
}

func duplicateApprovedReviewSkipReason(prNumber int, headSHA string) string {
	return fmt.Sprintf("Skipping agent review for PR #%d: head %s already has an approved Herd review result.", prNumber, headSHA)
}

// Review runs an agent review on the batch PR.
// If approved, it optionally auto-merges. If changes are requested,
// it creates fix issues and dispatches fix workers.
func Review(ctx context.Context, p platform.Platform, ag agent.Agent, g *git.Git, cfg *config.Config, params ReviewParams) (*ReviewResult, error) {
	var pr *platform.PullRequest
	var ms *platform.Milestone
	var batchBranch string
	var prComments []*platform.Comment
	prCommentsLoaded := false

	if params.PRNumber > 0 {
		// Direct PR lookup — used by pull_request_review trigger
		got, err := p.PullRequests().Get(ctx, params.PRNumber)
		if err != nil {
			return nil, fmt.Errorf("getting PR #%d: %w", params.PRNumber, err)
		}
		pr = got
		batchBranch = pr.Head

		msNumber, err := ParseBatchBranchMilestone(batchBranch)
		if err != nil {
			return nil, fmt.Errorf("parsing milestone from branch %s: %w", batchBranch, err)
		}
		got_ms, err := p.Milestones().Get(ctx, msNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", msNumber, err)
		}
		ms = got_ms
	} else if params.BatchNumber > 0 {
		// Batch-based lookup — used by advance-on-close
		got_ms, err := p.Milestones().Get(ctx, params.BatchNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", params.BatchNumber, err)
		}
		ms = got_ms
		batchBranch = fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

		prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if err != nil {
			return nil, fmt.Errorf("listing batch PRs: %w", err)
		}
		if len(prs) == 0 {
			return &ReviewResult{}, nil // No batch PR yet
		}
		pr = prs[0]
	} else {
		// Run-based lookup — used by workflow_run trigger
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

		ms = issue.Milestone
		batchBranch = fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

		// Find batch PR
		prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if err != nil {
			return nil, fmt.Errorf("listing batch PRs: %w", err)
		}
		if len(prs) == 0 {
			return &ReviewResult{}, nil // No batch PR yet
		}
		pr = prs[0]
	}

	// Skip if batch already complete
	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}

	var reviewedHeadSHA string
	if cfg.Integrator.Review {
		var err error
		reviewedHeadSHA, err = p.Repository().GetBranchSHA(ctx, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("getting branch SHA for review lock on %s: %w", batchBranch, err)
		}
		reviewLock, acquired, err := acquireReviewLock(ctx, p.Issues(), p.Repository(), pr.Number, ms.Number, params.RunID, reviewedHeadSHA, time.Now())
		if err != nil {
			return nil, fmt.Errorf("acquiring review lock for PR #%d: %w", pr.Number, err)
		}
		if !acquired {
			currentHeadSHA, currentErr := p.Repository().GetBranchSHA(ctx, batchBranch)
			if currentErr != nil {
				fmt.Printf("Warning: failed to get current branch SHA for active review lock diagnostics on %s: %s\n", batchBranch, currentErr)
			}
			state, ok, describeErr := describeReviewLock(ctx, p.Repository(), pr.Number)
			if describeErr != nil {
				fmt.Printf("Warning: failed to describe active review lock for PR #%d: %s\n", pr.Number, describeErr)
			}
			if ok {
				fmt.Printf("Review already in progress for PR #%d; skipping duplicate review trigger. lock_status=%s lock_id=%s owner=%s acquired_at=%s expires_at=%s recorded_head_sha=%s current_head_sha=%s\n",
					pr.Number, state.Status, state.LockID, state.Owner, formatReviewLockTime(state.AcquiredAt), formatReviewLockTime(state.ExpiresAt), state.BatchBranchSHA, currentHeadSHA)
				if params.Manual {
					if err := p.PullRequests().AddComment(ctx, pr.Number, buildActiveReviewLockSkipComment(state, currentHeadSHA)); err != nil {
						fmt.Printf("Warning: failed to post active review lock skip comment for PR #%d: %s\n", pr.Number, err)
					}
				}
			} else {
				fmt.Printf("Review already in progress for PR #%d; skipping duplicate review trigger. current_head_sha=%s\n", pr.Number, currentHeadSHA)
			}
			return &ReviewResult{BatchPRNumber: pr.Number}, nil
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := releaseReviewLock(releaseCtx, p.Issues(), p.Repository(), reviewLock); err != nil {
				fmt.Printf("Warning: failed to release review lock for PR #%d: %s\n", pr.Number, err)
			}
		}()
		if params.Manual && reviewLock != nil && reviewLock.reclaimedStale != nil {
			if err := p.PullRequests().AddComment(ctx, pr.Number, buildReclaimedStaleReviewLockComment(*reviewLock.reclaimedStale, reviewedHeadSHA)); err != nil {
				fmt.Printf("Warning: failed to post stale review lock informational comment for PR #%d: %s\n", pr.Number, err)
			}
		}

		currentHeadSHA, err := p.Repository().GetBranchSHA(ctx, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("getting current branch SHA for review idempotency on %s: %w", batchBranch, err)
		}
		if currentHeadSHA != reviewedHeadSHA {
			reviewedHeadSHA = currentHeadSHA
		}

		comments, commentErr := p.Issues().ListComments(ctx, pr.Number)
		if commentErr != nil {
			if !params.Manual {
				return nil, fmt.Errorf("listing PR comments for review idempotency: %w", commentErr)
			}
			fmt.Printf("Warning: failed to list PR comments for review idempotency: %s\n", commentErr)
		} else {
			prComments = comments
			prCommentsLoaded = true
			if !params.Manual {
				if marker, ok := latestReviewResultMarker(prComments, pr.Number, ms.Number, reviewedHeadSHA, trustedReviewResultMarkerHumanLogins(ctx, p)...); ok && marker.Status == reviewResultStatusApproved {
					reason := duplicateApprovedReviewSkipReason(pr.Number, reviewedHeadSHA)
					return &ReviewResult{
						BatchPRNumber:                pr.Number,
						SkippedDuplicateApprovedHead: true,
						SkipReason:                   reason,
						HeadSHA:                      reviewedHeadSHA,
					}, nil
				}
			}
		}
	}

	// Stable-disagreement circuit breaker: once the integrator has detected
	// a reviewer↔worker stalemate, automatic review is suspended until the
	// user removes the label (or runs /herd review or /herd integrate
	// manually, which call into here regardless).
	if issues.HasLabel(pr.Labels, issues.StableDisagreement) && !params.Manual {
		fmt.Printf("Skipping review: PR #%d has %s label.\n", pr.Number, issues.StableDisagreement)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}

	// Check if review is enabled
	if !cfg.Integrator.Review {
		// Skip review, just check auto-merge
		if cfg.PullRequests.AutoMerge {
			if _, err := p.PullRequests().Merge(ctx, pr.Number, platform.MergeMethod(cfg.Integrator.Strategy)); err != nil {
				return nil, fmt.Errorf("auto-merging batch PR #%d: %w", pr.Number, err)
			}
			if err := postMergeCleanup(ctx, p, ms.Number, batchBranch); err != nil {
				return nil, fmt.Errorf("post-merge cleanup: %w", err)
			}
		}
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	preparedDiff, err := reviewdiff.PrepareForReview(ctx, reviewdiff.PrepareRequest{
		PRNumber:     pr.Number,
		BaseRef:      pr.Base,
		HeadRef:      pr.Head,
		HeadSHA:      reviewedHeadSHA,
		RepoRoot:     params.RepoRoot,
		Git:          g,
		PullRequests: p.PullRequests(),
	})
	if err != nil {
		return nil, fmt.Errorf("preparing PR diff for review: %w", err)
	}
	plan := reviewdiff.ChunkForReview(preparedDiff.DiffSet, chunkOptionsFromConfig(cfg))
	preparedDiff.Chunks = plan.Chunks
	preparedDiff.Coverage = plan.Coverage
	logDiffCoverageIfLimited(preparedDiff)

	// Collect acceptance criteria from all milestone issues
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	// Skip review if fix workers from a previous cycle are still running.
	// Without this gate, every fix worker completion triggers a new review
	// that sees the remaining unfixed issues and creates duplicate fix issues.
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.Type == "fix" || parsed.FrontMatter.CIFixCycle > 0 {
			status := issues.StatusLabel(iss.Labels)
			if status == issues.StatusInProgress || status == issues.StatusReady {
				fmt.Printf("Skipping review: fix issue #%d is still %s\n", iss.Number, status)
				return &ReviewResult{BatchPRNumber: pr.Number}, nil
			}
		}
	}

	var allCriteria []string
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		allCriteria = append(allCriteria, parsed.Criteria...)
	}

	// Fetch PR comments once for fix requests and prior review context
	if !prCommentsLoaded {
		comments, commentErr := p.Issues().ListComments(ctx, pr.Number)
		if commentErr != nil {
			fmt.Printf("Warning: failed to list PR comments: %s\n", commentErr)
		} else {
			prComments = comments
		}
	}
	prComments = filterReviewLockComments(prComments)
	for _, fix := range collectFixRequestsFromComments(prComments) {
		allCriteria = append(allCriteria, "User requested: "+fix)
	}
	priorReviewComments := collectPriorReviewComments(prComments)
	userFeedback := collectUserFeedbackComments(prComments)
	workerNoOpVerdicts := collectWorkerNoOpVerdicts(prComments)
	ciStatus := currentPRCIStatus(ctx, p, reviewedHeadSHA, pr.Head)

	// Run agent review
	reviewOpts := agent.ReviewOptions{
		AcceptanceCriteria:   allCriteria,
		RepoRoot:             params.RepoRoot,
		Strictness:           cfg.Integrator.ReviewStrictness,
		MinFixSeverity:       cfg.Integrator.ReviewFixSeverity,
		CurrentPRMetadata:    buildCurrentPRMetadata(pr, reviewedHeadSHA, pr.Labels, ciStatus),
		PriorReviewComments:  priorReviewComments,
		UserFeedbackComments: userFeedback,
		WorkerNoOpVerdicts:   workerNoOpVerdicts,
	}

	// Load integrator role instructions
	ri, readErr := os.ReadFile(filepath.Join(params.RepoRoot, ".herd", "integrator.md"))
	if readErr == nil {
		reviewOpts.SystemPrompt = string(ri)
	}

	if params.ExtraInstructions != "" {
		if reviewOpts.SystemPrompt != "" {
			reviewOpts.SystemPrompt += "\n\n"
		}
		reviewOpts.SystemPrompt += params.ExtraInstructions
	}

	aggregate, err := runChunkedReviewWithRetry(ctx, ag, p, plan, reviewOpts, pr.Number)
	if errors.Is(err, errManualInterventionNeeded) {
		_ = postManualInterventionReviewComment(ctx, p, pr.Number)
		return &ReviewResult{
			BatchPRNumber:            pr.Number,
			ManualInterventionNeeded: true,
		}, nil
	}
	if err != nil {
		// Agent failed (e.g., API error, suspicious output). Don't propagate the
		// error — return a neutral result so the workflow succeeds and the review
		// retries on the next trigger.
		fmt.Printf("Review agent failed: %s. Will retry on next trigger.\n", err)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}
	if aggregate == nil || aggregate.Result == nil {
		return &ReviewResult{
			BatchPRNumber:            pr.Number,
			ManualInterventionNeeded: true,
		}, nil
	}
	reviewResult := aggregate.Result

	postReviewCtx, postReviewCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer postReviewCancel()
	currentHeadSHA, err := p.Repository().GetBranchSHA(postReviewCtx, batchBranch)
	if err != nil {
		return nil, fmt.Errorf("getting current branch SHA after review for %s: %w", batchBranch, err)
	}
	if currentHeadSHA != reviewedHeadSHA {
		if err := p.PullRequests().AddComment(postReviewCtx, pr.Number, buildStaleReviewResultComment(reviewedHeadSHA, currentHeadSHA)); err != nil {
			fmt.Printf("Warning: failed to post stale review result comment for PR #%d: %s\n", pr.Number, err)
		}
		fmt.Printf("Discarded stale review result for PR #%d because head SHA changed while review was running: reviewed_head_sha=%s current_head_sha=%s\n", pr.Number, reviewedHeadSHA, currentHeadSHA)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}
	livePR, err := p.PullRequests().Get(postReviewCtx, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("refreshing PR #%d after review for current merge state: %w", pr.Number, err)
	}
	livePR, err = refreshedPRWithOriginalIdentity(pr, livePR)
	if err != nil {
		return nil, fmt.Errorf("refreshing PR #%d after review for current merge state: %w", pr.Number, err)
	}
	pr = livePR
	markReviewResult := func(comment, status string, cycle, findingsCount int) (string, error) {
		marker := newReviewResultMarker(pr.Number, ms.Number, currentHeadSHA, status, cycle, findingsCount, time.Now())
		markedComment, markerErr := appendReviewResultMarker(comment, marker)
		if markerErr != nil {
			return "", fmt.Errorf("appending review result marker: %w", markerErr)
		}
		return markedComment, nil
	}
	coverageBlocked := coverageBlocksApproval(plan)

	finalDedupedFindings, finalDedupeStats := dedupeReviewFindings(reviewResult.Findings)
	reviewResult.Findings = finalDedupedFindings
	reviewResult.Comments = reviewCommentsFromFindings(finalDedupedFindings)
	if aggregate.DedupeStats.RawFindings == 0 {
		aggregate.DedupeStats = finalDedupeStats
	}

	originalFindingsCount := len(reviewResult.Findings)
	reconciledFindings, stateFilterStats := reconcileReviewFindingsWithLivePRState(postReviewCtx, p.Issues(), pr, reviewResult.Findings)
	appendReviewMetadata := func(comment string) string {
		return appendReviewAggregationMetadata(comment, aggregate.DedupeStats, stateFilterStats)
	}
	appendReviewMetadataAndCoverage := func(comment string) string {
		return appendDiffCoverageIfLimited(appendReviewMetadata(comment), preparedDiff)
	}
	reviewResult.Findings = reconciledFindings
	reviewResult.Comments = reviewCommentsFromFindings(reconciledFindings)
	staleStateOnlyFindings := originalFindingsCount > 0 &&
		stateFilterStats.StalePRStateFindingsIgnored == originalFindingsCount &&
		len(reviewResult.Findings) == 0

	// Partition findings by severity
	highFindings, mediumFindings, lowFindings, criteriaFindings := filterFindingsBySeverity(reviewResult.Findings)

	// Handle approved
	if reviewResult.Approved {
		if coverageBlocked {
			comment := buildCoverageApprovalBlockedBody(plan)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, findMaxFixCycle(allIssues), len(reviewResult.Findings))
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "Review coverage is partial; not all material files were reviewed.", platform.ReviewRequestChanges)
			return &ReviewResult{BatchPRNumber: pr.Number, FindingsCount: len(reviewResult.Findings)}, nil
		}
		summaryComment := buildBatchSummaryComment(allIssues, reviewResult.Summary)
		summaryComment = appendReviewMetadataAndCoverage(summaryComment)
		summaryComment, err = markReviewResult(summaryComment, reviewResultStatusApproved, findMaxFixCycle(allIssues), 0)
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, summaryComment)
		_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "", platform.ReviewApprove)
		if cfg.PullRequests.AutoMerge {
			if _, err := p.PullRequests().Merge(postReviewCtx, pr.Number, platform.MergeMethod(cfg.Integrator.Strategy)); err != nil {
				return nil, fmt.Errorf("auto-merging batch PR #%d: %w", pr.Number, err)
			}
			if err := postMergeCleanup(postReviewCtx, p, ms.Number, batchBranch); err != nil {
				return nil, fmt.Errorf("post-merge cleanup: %w", err)
			}
		}
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Handle changes requested — determine fix cycle
	currentCycle := findMaxFixCycle(allIssues)

	if cfg.Integrator.ReviewMaxFixCycles > 0 && currentCycle >= cfg.Integrator.ReviewMaxFixCycles {
		comment := fmt.Sprintf("⚠️ **HerdOS Integrator**\n\nAgent review found issues but max fix cycles (%d) reached. Manual intervention needed:\n\n",
			cfg.Integrator.ReviewMaxFixCycles)
		for _, f := range reviewResult.Findings {
			comment += fmt.Sprintf("- **%s** %s\n", f.Severity, f.Description)
		}
		comment = appendReviewMetadataAndCoverage(comment)
		comment, err = markReviewResult(comment, reviewResultStatusMaxCyclesHit, currentCycle, len(reviewResult.Findings))
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// Safety valve: a suspicious number of HIGH findings from one review pass
	// stops automated fix dispatch. In chunked mode each chunk is an independent
	// bounded review pass, so aggregate HIGH findings across chunks are expected on
	// large PRs and must not trip this guard by themselves.
	hasChunkedStats := false
	for _, stat := range aggregate.ChunkStats {
		if stat.TotalChunks > 1 {
			hasChunkedStats = true
			break
		}
	}
	if hasChunkedStats {
		if stat, ok := offendingChunkSafetyValveStats(aggregate.ChunkStats, safetyValveLimit); ok {
			comment := buildChunkReviewSafetyValveComment(stat, safetyValveLimit)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusMaxCyclesHit, currentCycle, stat.HighFindingCount)
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
		}
	} else if len(highFindings) > safetyValveLimit {
		comment := buildReviewSafetyValveComment(len(highFindings), safetyValveLimit)
		comment = appendReviewMetadataAndCoverage(comment)
		comment, err = markReviewResult(comment, reviewResultStatusMaxCyclesHit, currentCycle, len(highFindings))
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// Combine findings for fix dispatch based on configured minimum severity
	actionableFindings := make([]agent.ReviewFinding, 0, len(highFindings)+len(mediumFindings)+len(lowFindings))
	actionableFindings = append(actionableFindings, highFindings...)
	minSeverity := strings.ToLower(cfg.Integrator.ReviewFixSeverity)
	if minSeverity == "" {
		minSeverity = "medium"
	}
	if minSeverity == "medium" || minSeverity == "low" {
		actionableFindings = append(actionableFindings, mediumFindings...)
	}
	if minSeverity == "low" {
		actionableFindings = append(actionableFindings, lowFindings...)
	}

	// No actionable findings — approve with informational comment and batch summary
	if len(actionableFindings) == 0 {
		if coverageBlocked {
			comment := buildCoverageApprovalBlockedBody(plan)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, currentCycle, len(reviewResult.Findings))
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "Review coverage is partial; not all material files were reviewed.", platform.ReviewRequestChanges)
			return &ReviewResult{BatchPRNumber: pr.Number, FindingsCount: len(reviewResult.Findings)}, nil
		}
		if staleStateOnlyFindings {
			comment := buildStalePRStateFindingsIgnoredComment()
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusApproved, currentCycle, 0)
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "", platform.ReviewApprove)
			return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
		} else if stateFilterStats.CascadeLabelRemoveError != "" {
			comment := buildReviewCycleComment(0, cfg.Integrator.ReviewMaxFixCycles, nil, highFindings, mediumFindings, lowFindings, criteriaFindings)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, currentCycle, len(reviewResult.Findings))
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "Stale cascade label cleanup failed; manual cleanup is required before approval.", platform.ReviewRequestChanges)
			return &ReviewResult{BatchPRNumber: pr.Number, FindingsCount: len(reviewResult.Findings)}, nil
		}
		comment := buildReviewCycleComment(0, cfg.Integrator.ReviewMaxFixCycles, nil, highFindings, mediumFindings, lowFindings, criteriaFindings)
		comment = appendReviewMetadataAndCoverage(comment)
		comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, currentCycle, len(reviewResult.Findings))
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
		summaryComment := buildBatchSummaryComment(allIssues, reviewResult.Summary)
		summaryComment = appendDiffCoverageIfLimited(summaryComment, preparedDiff)
		summaryComment, err = markReviewResult(summaryComment, reviewResultStatusApproved, currentCycle, 0)
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, summaryComment)
		_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "", platform.ReviewApprove)
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Collect open fix issues for dedup. Only fix issues that are actively
	// in-progress or ready suppress new findings — done/failed issues are past
	// attempts and recurring findings against them must produce a fresh fix.
	openFixIssues := activeFixIssues(allIssues)

	actionableFindings = dedupFindings(actionableFindings, openFixIssues)
	if len(actionableFindings) == 0 {
		if coverageBlocked {
			comment := buildCoverageApprovalBlockedBody(plan)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, currentCycle, len(reviewResult.Findings))
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "Review coverage is partial; not all material files were reviewed.", platform.ReviewRequestChanges)
			return &ReviewResult{BatchPRNumber: pr.Number, FindingsCount: len(reviewResult.Findings)}, nil
		}
		// All findings are covered by existing fix issues — approve to unblock
		// any previous REQUEST_CHANGES review and post an informational comment.
		fmt.Println("All actionable findings are duplicates of existing fix issues, approving.")
		comment := "✅ **HerdOS Agent Review**\n\nAll findings are already covered by existing fix workers. Approving to unblock the PR."
		comment = appendReviewMetadataAndCoverage(comment)
		comment, err = markReviewResult(comment, reviewResultStatusApproved, currentCycle, 0)
		if err != nil {
			return nil, err
		}
		_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
		_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, "", platform.ReviewApprove)
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Stable-disagreement detection: if any of the actionable findings match a
	// previous worker no-op verdict, halt the entire cycle. Any findings that
	// are genuinely new (kept) are dropped on the floor by design — the user
	// must intervene before further automated work happens.
	if len(workerNoOpVerdicts) > 0 {
		blocked, _, verdictIdx := stableDisagreementBlocked(actionableFindings, workerNoOpVerdicts)
		if len(blocked) > 0 {
			_ = p.Issues().AddLabels(postReviewCtx, pr.Number, []string{issues.StableDisagreement})
			comment := buildStableDisagreementComment(blocked, verdictIdx, workerNoOpVerdicts)
			comment = appendReviewMetadataAndCoverage(comment)
			comment, err = markReviewResult(comment, reviewResultStatusChangesRequested, currentCycle, len(blocked))
			if err != nil {
				return nil, err
			}
			_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, comment)
			return &ReviewResult{
				BatchPRNumber:      pr.Number,
				StableDisagreement: true,
				FindingsCount:      len(blocked),
			}, nil
		}
	}

	// Create single batched fix issue with all actionable findings
	nextCycle := currentCycle + 1

	var fixTaskBuilder strings.Builder
	fixTaskBuilder.WriteString("Fix the following issues found during agent review:\n\n")
	for i, f := range actionableFindings {
		fixTaskBuilder.WriteString(fmt.Sprintf("%d. **[%s]** %s\n", i+1, f.Severity, f.Description))
	}

	fixBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    ms.Number,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  pr.Number,
		},
		Task:    fixTaskBuilder.String(),
		Context: fmt.Sprintf("Found during agent review of batch PR #%d ([herd] %s), cycle %d.", pr.Number, ms.Title, nextCycle),
	})

	fixTitle := fmt.Sprintf("Review fixes (cycle %d)", nextCycle)

	defaultBranchForDispatch, _ := p.Repository().GetDefaultBranch(postReviewCtx)

	truncatedBody, overflow := issues.TruncateIssueBody(fixBody)
	fixIssue, err := p.Issues().Create(postReviewCtx, fixTitle, truncatedBody,
		[]string{issues.TypeFix, issues.StatusInProgress}, &ms.Number)
	if err != nil {
		// Failed to create the fix issue
		return &ReviewResult{BatchPRNumber: pr.Number, AllCreatesFailed: true, FindingsCount: len(actionableFindings)}, nil
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(postReviewCtx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on fix issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	// Dispatch single fix worker
	_, _ = p.Workflows().Dispatch(postReviewCtx, "herd-worker.yml", defaultBranchForDispatch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})

	fixIssueNums := []int{fixIssue.Number}

	// Post structured findings comment
	findingsComment := buildReviewCycleComment(nextCycle, cfg.Integrator.ReviewMaxFixCycles, fixIssueNums, highFindings, mediumFindings, lowFindings, criteriaFindings)
	if coverageBlocked {
		findingsComment = appendCoverageApprovalBlockedSection(findingsComment, plan)
	}
	findingsComment = appendReviewMetadataAndCoverage(findingsComment)
	findingsComment, err = markReviewResult(findingsComment, reviewResultStatusChangesRequested, nextCycle, len(actionableFindings))
	if err != nil {
		return nil, err
	}
	_ = p.PullRequests().AddComment(postReviewCtx, pr.Number, findingsComment)

	// Block merge with Request Changes review
	reviewBody := fmt.Sprintf("Found %d actionable issues. Fix worker dispatched → #%d.", len(actionableFindings), fixIssue.Number)
	_ = p.PullRequests().CreateReview(postReviewCtx, pr.Number, reviewBody, platform.ReviewRequestChanges)

	return &ReviewResult{
		FixIssues:     fixIssueNums,
		FixCycle:      nextCycle,
		BatchPRNumber: pr.Number,
	}, nil
}

func refreshedPRWithOriginalIdentity(original, refreshed *platform.PullRequest) (*platform.PullRequest, error) {
	if refreshed == nil {
		if original == nil {
			return nil, errors.New("platform returned nil PR")
		}
		return nil, fmt.Errorf("platform returned nil PR for #%d", original.Number)
	}
	if original == nil {
		return refreshed, nil
	}
	if refreshed.Number == 0 {
		refreshed.Number = original.Number
	} else if original.Number != 0 && refreshed.Number != original.Number {
		return nil, fmt.Errorf("platform returned PR #%d while refreshing PR #%d", refreshed.Number, original.Number)
	}
	if refreshed.Head == "" {
		refreshed.Head = original.Head
	}
	if refreshed.Base == "" {
		refreshed.Base = original.Base
	}
	return refreshed, nil
}

func runChunkedReviewWithRetry(ctx context.Context, ag agent.Agent, p platform.Platform, plan reviewdiff.ChunkPlan, baseOpts agent.ReviewOptions, prNumber int) (*aggregatedReview, error) {
	fmt.Printf("Running agent review in %d chunk(s)\n", len(plan.Chunks))
	aggregate := aggregatedReview{}
	if len(plan.Chunks) == 0 {
		aggregate.Result = &agent.ReviewResult{
			Approved: false,
			Summary:  "No reviewable diff content was found.",
		}
		return &aggregate, nil
	}

	approved := true
	summaries := make([]string, 0, len(plan.Chunks))
	findings := make([]agent.ReviewFinding, 0)
	coverageSummary := reviewdiff.FormatChunkedCoverageSummary(plan, len(plan.Chunks), reviewdiff.DefaultMaxOmittedSummaryEntries)

	for _, chunk := range plan.Chunks {
		totalChunks := chunk.Total
		if plan.Coverage.RequiredChunks > totalChunks {
			totalChunks = plan.Coverage.RequiredChunks
		}
		opts := baseOpts
		opts.ChunkIndex = chunk.Index
		opts.TotalChunks = totalChunks
		opts.ChunkIncludedPathRange = chunkIncludedPathRange(chunk)
		opts.CoverageSummary = coverageSummary
		opts.ChunkedReview = totalChunks > 1
		opts.PartialReview = !plan.Coverage.Complete

		result, err := runReviewWithRetry(ctx, ag, p, chunk.Text, opts, prNumber)
		if errors.Is(err, errManualInterventionNeeded) {
			aggregate.ChunksReviewed = chunk.Index - 1
			aggregate.ManualIntervention = true
			return nil, errManualInterventionNeeded
		}
		if err != nil {
			aggregate.ChunksReviewed = chunk.Index - 1
			aggregate.AgentError = err
			return nil, aggregate.AgentError
		}
		if result == nil {
			aggregate.ChunksReviewed = chunk.Index
			aggregate.ManualIntervention = true
			return nil, nil
		}
		aggregate.ChunkStats = append(aggregate.ChunkStats, buildChunkReviewStats(chunk.Index, totalChunks, result.Findings))
		if !result.Approved {
			approved = false
		}
		if strings.TrimSpace(result.Summary) != "" {
			summaries = append(summaries, fmt.Sprintf("Chunk %d/%d: %s", chunk.Index, totalChunks, strings.TrimSpace(result.Summary)))
		}
		findings = append(findings, result.Findings...)
	}

	if len(materialNotReviewed(plan)) > 0 || plan.Coverage.ExceededMaxChunks {
		approved = false
	}

	summary := strings.Join(summaries, "\n\n")
	if len(plan.Chunks) > 1 {
		summary = fmt.Sprintf("Chunked review completed across %d chunk(s).", len(plan.Chunks)) + optionalSummarySuffix(summary)
	}
	dedupedFindings, dedupeStats := dedupeReviewFindings(findings)
	aggregate.Result = &agent.ReviewResult{
		Approved: approved,
		Findings: dedupedFindings,
		Comments: reviewCommentsFromFindings(dedupedFindings),
		Summary:  summary,
	}
	aggregate.ChunksReviewed = len(plan.Chunks)
	aggregate.DedupeStats = dedupeStats
	return &aggregate, nil
}

func buildChunkReviewStats(chunkIndex, totalChunks int, findings []agent.ReviewFinding) chunkReviewStats {
	stat := chunkReviewStats{
		ChunkIndex:        chunkIndex,
		TotalChunks:       totalChunks,
		TotalFindingCount: len(findings),
		Findings:          append([]agent.ReviewFinding(nil), findings...),
	}
	for _, finding := range findings {
		if strings.EqualFold(strings.TrimSpace(finding.Severity), "HIGH") {
			stat.HighFindingCount++
		}
	}
	return stat
}

func chunkIncludedPathRange(chunk reviewdiff.ReviewChunk) string {
	if len(chunk.IncludedFiles) == 0 {
		return ""
	}
	first := chunk.IncludedFiles[0].Path
	lastFile := chunk.IncludedFiles[len(chunk.IncludedFiles)-1]
	last := lastFile.Path
	if first == last {
		return first
	}
	return first + " through " + last
}

func reviewCommentsFromFindings(findings []agent.ReviewFinding) []string {
	comments := make([]string, 0, len(findings))
	for _, finding := range findings {
		comments = append(comments, finding.Description)
	}
	return comments
}

func offendingChunkSafetyValveStats(stats []chunkReviewStats, limit int) (chunkReviewStats, bool) {
	for _, stat := range stats {
		if stat.HighFindingCount > limit {
			return stat, true
		}
	}
	return chunkReviewStats{}, false
}

func buildReviewSafetyValveComment(highCount int, limit int) string {
	return fmt.Sprintf("⚠️ **HerdOS Integrator**\n\nAgent review found %d high-severity issues in a single review pass. This exceeds the safety limit (%d). Creating fix workers was skipped to prevent runaway agent invocations.", highCount, limit)
}

func buildChunkReviewSafetyValveComment(stat chunkReviewStats, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ **HerdOS Integrator**\n\nAgent review chunk %d/%d found %d high-severity issues, exceeding the safety limit (%d). Creating fix workers was skipped to prevent runaway agent invocations.", stat.ChunkIndex, stat.TotalChunks, stat.HighFindingCount, limit)
	highShown := 0
	for _, finding := range stat.Findings {
		if !strings.EqualFold(strings.TrimSpace(finding.Severity), "HIGH") {
			continue
		}
		if highShown == 0 {
			b.WriteString("\n\nFirst high-severity findings from the offending chunk:\n")
		}
		if highShown >= 3 {
			remaining := stat.HighFindingCount - highShown
			if remaining > 0 {
				fmt.Fprintf(&b, "- ...and %d more high-severity finding(s) in this chunk.\n", remaining)
			}
			break
		}
		description := strings.TrimSpace(finding.Description)
		if description == "" {
			description = "(no description)"
		}
		fmt.Fprintf(&b, "- %s\n", description)
		highShown++
	}
	return strings.TrimSpace(b.String())
}

func optionalSummarySuffix(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	return "\n\n" + summary
}

func materialNotReviewed(plan reviewdiff.ChunkPlan) []reviewdiff.FileCoverage {
	var material []reviewdiff.FileCoverage
	for _, file := range plan.Coverage.NotReviewedFiles {
		if reviewdiff.IsAllowableNotReviewedFile(file) {
			continue
		}
		material = append(material, file)
	}
	return material
}

func coverageBlocksApproval(plan reviewdiff.ChunkPlan) bool {
	return len(plan.Chunks) == 0 || plan.Coverage.ExceededMaxChunks || len(materialNotReviewed(plan)) > 0
}

func buildCoverageApprovalBlockedComment(prepared reviewdiff.PreparedDiff, plan reviewdiff.ChunkPlan) string {
	return appendDiffCoverageIfLimited(buildCoverageApprovalBlockedBody(plan), prepared)
}

func appendCoverageApprovalBlockedSection(comment string, plan reviewdiff.ChunkPlan) string {
	return strings.TrimRight(comment, "\n") + "\n\n" + buildCoverageApprovalBlockedBody(plan)
}

func buildCoverageApprovalBlockedBody(plan reviewdiff.ChunkPlan) string {
	material := materialNotReviewed(plan)
	var b strings.Builder
	b.WriteString("⚠️ **HerdOS Integrator**\n\n")
	b.WriteString("HerdOS could not approve this PR because the diff review was partial and not all material source files were reviewed.")
	if len(plan.Chunks) == 0 {
		b.WriteString("\n\nNo review chunks were sent to the review agent, so Herd cannot approve this PR without a reviewed chunk.")
	}
	if plan.Coverage.ExceededMaxChunks {
		fmt.Fprintf(&b, "\n\nThe diff required %d review chunk(s), which exceeds the configured maximum of %d. Herd reviewed only the allowed chunk(s).",
			plan.Coverage.RequiredChunks, plan.Coverage.MaxChunks)
	}
	if len(material) > 0 {
		b.WriteString("\n\nMaterial files not reviewed:\n")
		limit := min(len(material), reviewdiff.DefaultMaxOmittedSummaryEntries)
		for _, file := range material[:limit] {
			reason := file.Reason
			if strings.TrimSpace(reason) == "" {
				reason = "not reviewed"
			}
			fmt.Fprintf(&b, "- %s: %s\n", file.Path, reason)
		}
		if len(material) > limit {
			fmt.Fprintf(&b, "- ... %d additional material files not shown\n", len(material)-limit)
		}
	}
	if len(material) == 0 && len(plan.Coverage.NotReviewedFiles) > 0 {
		b.WriteString("\n\nFiles summarized but not reviewed:\n")
		limit := min(len(plan.Coverage.NotReviewedFiles), reviewdiff.DefaultMaxOmittedSummaryEntries)
		for _, file := range plan.Coverage.NotReviewedFiles[:limit] {
			reason := file.Reason
			if strings.TrimSpace(reason) == "" {
				reason = "not reviewed"
			}
			fmt.Fprintf(&b, "- %s: %s\n", file.Path, reason)
		}
		if len(plan.Coverage.NotReviewedFiles) > limit {
			fmt.Fprintf(&b, "- ... %d additional summarized files not shown\n", len(plan.Coverage.NotReviewedFiles)-limit)
		}
	}
	return b.String()
}

func trustedReviewResultMarkerHumanLogins(ctx context.Context, p platform.Platform) []string {
	provider, ok := p.(authenticatedLoginProvider)
	if !ok {
		return nil
	}
	login, err := provider.AuthenticatedLogin(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to get authenticated GitHub login for review-result marker trust: %s\n", err)
		return nil
	}
	if strings.TrimSpace(login) == "" {
		return nil
	}
	return []string{login}
}

// ReviewStandalone runs an agent review on a non-batch PR.
// It posts a findings comment but does NOT create fix issues, dispatch workers,
// look up milestones, or track fix cycles.
func ReviewStandalone(ctx context.Context, p platform.Platform, ag agent.Agent, cfg *config.Config, params ReviewStandaloneParams) (*ReviewStandaloneResult, error) {
	pr, err := p.PullRequests().Get(ctx, params.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("getting PR #%d: %w", params.PRNumber, err)
	}
	preparedDiff, err := reviewdiff.PrepareForReview(ctx, reviewdiff.PrepareRequest{
		PRNumber:     params.PRNumber,
		BaseRef:      pr.Base,
		HeadRef:      pr.Head,
		RepoRoot:     params.RepoRoot,
		Git:          git.New(params.RepoRoot),
		PullRequests: p.PullRequests(),
	})
	if err != nil {
		return nil, fmt.Errorf("preparing PR diff for review: %w", err)
	}
	plan := reviewdiff.ChunkForReview(preparedDiff.DiffSet, chunkOptionsFromConfig(cfg))
	preparedDiff.Chunks = plan.Chunks
	preparedDiff.Coverage = plan.Coverage
	logDiffCoverageIfLimited(preparedDiff)

	reviewOpts := agent.ReviewOptions{
		RepoRoot:          params.RepoRoot,
		Strictness:        cfg.Integrator.ReviewStrictness,
		MinFixSeverity:    cfg.Integrator.ReviewFixSeverity,
		CurrentPRMetadata: buildCurrentPRMetadata(pr, "", pr.Labels, currentPRCIStatus(ctx, p, "", pr.Head)),
	}

	ri, readErr := os.ReadFile(filepath.Join(params.RepoRoot, ".herd", "integrator.md"))
	if readErr == nil {
		reviewOpts.SystemPrompt = string(ri)
	}

	if params.ExtraInstructions != "" {
		if reviewOpts.SystemPrompt != "" {
			reviewOpts.SystemPrompt += "\n\n"
		}
		reviewOpts.SystemPrompt += params.ExtraInstructions
	}

	prComments, commentErr := p.Issues().ListComments(ctx, params.PRNumber)
	if commentErr == nil {
		reviewOpts.PriorReviewComments = collectPriorReviewComments(prComments)
		reviewOpts.UserFeedbackComments = collectUserFeedbackComments(prComments)
		reviewOpts.WorkerNoOpVerdicts = collectWorkerNoOpVerdicts(prComments)
	}

	aggregate, err := runChunkedReviewWithRetry(ctx, ag, p, plan, reviewOpts, params.PRNumber)
	if errors.Is(err, errManualInterventionNeeded) {
		_ = postManualInterventionReviewComment(ctx, p, params.PRNumber)
		return &ReviewStandaloneResult{ManualInterventionNeeded: true}, nil
	}
	if err != nil {
		fmt.Printf("Review agent failed: %s\n", err)
		return &ReviewStandaloneResult{}, nil
	}
	if aggregate == nil || aggregate.Result == nil {
		return &ReviewStandaloneResult{ManualInterventionNeeded: true}, nil
	}
	reviewResult := aggregate.Result
	livePR, err := p.PullRequests().Get(ctx, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("refreshing PR #%d after standalone review for current merge state: %w", pr.Number, err)
	}
	livePR, err = refreshedPRWithOriginalIdentity(pr, livePR)
	if err != nil {
		return nil, fmt.Errorf("refreshing PR #%d after standalone review for current merge state: %w", pr.Number, err)
	}
	pr = livePR
	originalFindingsCount := len(reviewResult.Findings)
	reconciledFindings, stateFilterStats := reconcileReviewFindingsWithLivePRState(ctx, nil, pr, reviewResult.Findings)
	reviewResult.Findings = reconciledFindings
	reviewResult.Comments = reviewCommentsFromFindings(reconciledFindings)
	staleStateOnlyFindings := originalFindingsCount > 0 &&
		stateFilterStats.StalePRStateFindingsIgnored == originalFindingsCount &&
		len(reviewResult.Findings) == 0
	appendReviewMetadataAndCoverage := func(comment string) string {
		return appendDiffCoverageIfLimited(appendReviewAggregationMetadata(comment, aggregate.DedupeStats, stateFilterStats), preparedDiff)
	}

	highFindings, mediumFindings, lowFindings, criteriaFindings := filterFindingsBySeverity(reviewResult.Findings)
	if coverageBlocksApproval(plan) {
		comment := buildCoverageApprovalBlockedBody(plan)
		if len(reviewResult.Findings) > 0 {
			comment += "\n\n" + buildReviewCycleComment(0, 0, nil, highFindings, mediumFindings, lowFindings, criteriaFindings)
		}
		comment = appendReviewMetadataAndCoverage(comment)
		_ = p.PullRequests().AddComment(ctx, params.PRNumber, comment)
		_ = p.PullRequests().CreateReview(ctx, params.PRNumber, "Review coverage is partial; not all material files were reviewed.", platform.ReviewRequestChanges)
		return &ReviewStandaloneResult{FindingsCount: len(reviewResult.Findings)}, nil
	}

	if reviewResult.Approved {
		comment := fmt.Sprintf("✅ **HerdOS Agent Review**\n\n%s\n", reviewResult.Summary)
		comment = appendDiffCoverageIfLimited(comment, preparedDiff)
		_ = p.PullRequests().AddComment(ctx, params.PRNumber, comment)
		_ = p.PullRequests().CreateReview(ctx, params.PRNumber, "", platform.ReviewApprove)
		return &ReviewStandaloneResult{}, nil
	}

	if staleStateOnlyFindings {
		comment := buildStalePRStateFindingsIgnoredComment()
		comment = appendReviewMetadataAndCoverage(comment)
		_ = p.PullRequests().AddComment(ctx, params.PRNumber, comment)
		_ = p.PullRequests().CreateReview(ctx, params.PRNumber, "", platform.ReviewApprove)
		return &ReviewStandaloneResult{}, nil
	}

	findingsComment := buildReviewCycleComment(0, 0, nil, highFindings, mediumFindings, lowFindings, criteriaFindings)
	findingsComment = appendReviewMetadataAndCoverage(findingsComment)
	_ = p.PullRequests().AddComment(ctx, params.PRNumber, findingsComment)
	_ = p.PullRequests().CreateReview(ctx, params.PRNumber, "Agent review found issues.", platform.ReviewRequestChanges)

	return &ReviewStandaloneResult{FindingsCount: len(reviewResult.Findings)}, nil
}

// postCloseCleanup closes all issues in the milestone (labeling non-done ones
// as cancelled), closes the milestone, and deletes the batch branch.
// Used when a batch PR is closed without merging.
func postCloseCleanup(ctx context.Context, p platform.Platform, msNumber int, batchBranch string) error {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &msNumber,
	})
	if err != nil {
		return fmt.Errorf("listing milestone issues: %w", err)
	}

	closed := "closed"
	for _, issue := range allIssues {
		if !issues.HasLabel(issue.Labels, issues.StatusDone) {
			currentStatus := issues.StatusLabel(issue.Labels)
			if currentStatus != "" {
				_ = p.Issues().RemoveLabels(ctx, issue.Number, []string{currentStatus})
			}
			_ = p.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusCancelled})
		}
		_, _ = p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{State: &closed})
	}

	_, _ = p.Milestones().Update(ctx, msNumber, platform.MilestoneUpdate{State: &closed})

	_ = p.Repository().DeleteBranch(ctx, batchBranch)

	return nil
}

// postMergeCleanup closes all issues in the milestone, closes the milestone,
// and deletes the batch branch.
func postMergeCleanup(ctx context.Context, p platform.Platform, msNumber int, batchBranch string) error {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &msNumber,
	})
	if err != nil {
		return fmt.Errorf("listing milestone issues: %w", err)
	}

	closed := "closed"
	for _, issue := range allIssues {
		_, _ = p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{State: &closed})
	}

	_, _ = p.Milestones().Update(ctx, msNumber, platform.MilestoneUpdate{State: &closed})

	_ = p.Repository().DeleteBranch(ctx, batchBranch)

	return nil
}

// collectFixRequests fetches comments on the given issue/PR number and
// extracts the prompt/description from any /herd fix commands.
func collectFixRequests(ctx context.Context, p platform.Platform, prNumber int) []string {
	comments, err := p.Issues().ListComments(ctx, prNumber)
	if err != nil {
		fmt.Printf("Warning: failed to list PR comments for fix request collection: %s\n", err)
		return nil
	}
	return collectFixRequestsFromComments(comments)
}

// collectFixRequestsFromComments extracts the prompt/description from any
// /herd fix commands in the given comments.
func collectFixRequestsFromComments(comments []*platform.Comment) []string {
	var fixes []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(body, "/herd fix") {
			continue
		}
		// Ensure exact command match (not e.g. "/herd fixci")
		rest := strings.TrimPrefix(body, "/herd fix")
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' {
			continue
		}
		// Extract the description after "/herd fix"
		desc := strings.TrimSpace(rest)
		// Strip surrounding quotes if present
		if len(desc) >= 2 && desc[0] == '"' && desc[len(desc)-1] == '"' {
			desc = desc[1 : len(desc)-1]
		}
		if desc != "" {
			fixes = append(fixes, desc)
		}
	}
	return fixes
}

// collectPriorReviewComments returns the full body of any previous HerdOS
// agent review comments. These are identified by the emoji markers used in
// buildReviewCycleComment and buildBatchSummaryComment.
func collectPriorReviewComments(comments []*platform.Comment) []string {
	var prior []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if strings.HasPrefix(body, "🔍 **HerdOS Agent Review**") ||
			strings.HasPrefix(body, "✅ **HerdOS Agent Review**") {
			prior = append(prior, body)
		}
	}
	return prior
}

// collectUserFeedbackComments returns the full body of non-HerdOS user comments.
// These are regular user comments that may contain feedback on review findings
// (e.g., marking false positives). HerdOS bot comments are excluded by checking
// for known marker prefixes.
func collectUserFeedbackComments(comments []*platform.Comment) []string {
	herdPrefixes := []string{
		"🔍 **HerdOS",
		"✅ **HerdOS",
		"⚠️ **HerdOS",
		"🔧 ",
		"🔄 **Integrator",
		"📋 **Worker Progress",
		"**Worker #",
		"/herd ",
	}
	var feedback []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		isHerd := false
		for _, prefix := range herdPrefixes {
			if strings.HasPrefix(body, prefix) {
				isHerd = true
				break
			}
		}
		if !isHerd {
			feedback = append(feedback, body)
		}
	}
	return feedback
}

// workerNoOpVerdictPrefix matches the first line of a structured worker
// no-op verdict posted on a batch PR by the worker package. Keep this in
// sync with worker.noOpVerdictHeader: "**Worker #N — no-op verdict**".
const workerNoOpVerdictPrefix = "**Worker #"
const workerNoOpVerdictSuffix = "— no-op verdict**"

// collectWorkerNoOpVerdicts returns the bodies of comments that are
// structured worker no-op verdicts. A verdict is identified by a first
// line of the form "**Worker #<digits> — no-op verdict**". Comments
// that don't match are ignored. Order is preserved.
func collectWorkerNoOpVerdicts(comments []*platform.Comment) []string {
	var out []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		firstLine := body
		if idx := strings.Index(body, "\n"); idx >= 0 {
			firstLine = body[:idx]
		}
		firstLine = strings.TrimSpace(firstLine)
		if !strings.HasPrefix(firstLine, workerNoOpVerdictPrefix) {
			continue
		}
		if !strings.HasSuffix(firstLine, workerNoOpVerdictSuffix) {
			continue
		}
		out = append(out, body)
	}
	return out
}

func buildCurrentPRMetadata(pr *platform.PullRequest, headSHA string, labels []string, ciStatus string) string {
	var lines []string
	if pr != nil {
		lines = append(lines,
			fmt.Sprintf("PR number: #%d", pr.Number),
			fmt.Sprintf("Head branch: %s", unavailableIfEmpty(pr.Head)),
			fmt.Sprintf("Base branch: %s", unavailableIfEmpty(pr.Base)),
		)
	}
	lines = append(lines, fmt.Sprintf("Head SHA: %s", unavailableIfEmpty(headSHA)))
	if pr != nil {
		lines = append(lines,
			fmt.Sprintf("Mergeable known: %t", pr.MergeableKnown),
			fmt.Sprintf("Mergeable: %t", pr.Mergeable),
			fmt.Sprintf("Merge state status: %s", unavailableIfEmpty(pr.MergeStateStatus)),
		)
	}
	lines = append(lines,
		fmt.Sprintf("Labels: %s", formatMetadataLabels(labels)),
		fmt.Sprintf("CI status: %s", unavailableIfEmpty(ciStatus)),
	)
	return strings.Join(lines, "\n")
}

func currentPRCIStatus(ctx context.Context, p platform.Platform, headSHA, headRef string) string {
	checks := p.Checks()
	if checks == nil {
		return "unavailable"
	}
	ref := headSHA
	if ref == "" {
		ref = headRef
	}
	if ref == "" {
		return "unavailable"
	}
	status, err := checks.GetCombinedStatus(ctx, ref)
	if err != nil {
		fmt.Printf("Warning: failed to get current PR CI status for %s: %s\n", ref, err)
		return "unavailable"
	}
	if strings.TrimSpace(status) == "" {
		return "unavailable"
	}
	return status
}

func formatMetadataLabels(labels []string) string {
	if len(labels) == 0 {
		return "(none)"
	}
	return strings.Join(labels, ", ")
}

func unavailableIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return value
}

func findMaxFixCycle(allIssues []*platform.Issue) int {
	max := 0
	for _, issue := range allIssues {
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.FixCycle > max {
			max = parsed.FrontMatter.FixCycle
		}
	}
	return max
}

// unparseableRetryDelay is the wait between unparseable-output review
// attempts. Exposed as a package-level var so tests can shorten it.
var unparseableRetryDelay = 5 * time.Second

// runReviewWithRetry runs ag.Review and retries once on unparseable
// output. If both attempts fail, it returns errManualInterventionNeeded.
// On agent-side error it returns (nil, err). On success it returns the
// parsed result.
func runReviewWithRetry(ctx context.Context, ag agent.Agent, _ platform.Platform, diff string, opts agent.ReviewOptions, _ int) (*agent.ReviewResult, error) {
	res, err := ag.Review(ctx, diff, opts)
	if err != nil {
		return nil, err
	}
	if !isUnparseable(res) {
		return res, nil
	}
	fmt.Printf("Review agent returned unparseable output. Retrying in %s...\n", unparseableRetryDelay)
	select {
	case <-time.After(unparseableRetryDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	res2, err := ag.Review(ctx, diff, opts)
	if err != nil {
		return nil, err
	}
	if !isUnparseable(res2) {
		return res2, nil
	}
	return nil, errManualInterventionNeeded
}

func postManualInterventionReviewComment(ctx context.Context, p platform.Platform, prNumber int) error {
	return p.PullRequests().AddComment(ctx, prNumber, manualInterventionReviewComment)
}

// isUnparseable returns true when the agent layer signaled an
// unparseable review, either via the explicit flag or (for backward
// compatibility) via the legacy "Failed to parse" Summary prefix.
func isUnparseable(res *agent.ReviewResult) bool {
	if res == nil {
		return false
	}
	if res.IsUnparseable {
		return true
	}
	return strings.HasPrefix(res.Summary, "Failed to parse")
}

// ParseBatchBranchMilestone extracts the milestone number from a batch branch name.
// Expected format: "herd/batch/{number}-{slug}"
func ParseBatchBranchMilestone(branch string) (int, error) {
	parts := strings.TrimPrefix(branch, "herd/batch/")
	if parts == branch {
		return 0, fmt.Errorf("not a batch branch: %s", branch)
	}
	idx := strings.Index(parts, "-")
	if idx < 0 {
		return 0, fmt.Errorf("invalid batch branch format: %s", branch)
	}
	return strconv.Atoi(parts[:idx])
}

func truncate(s string, max int) string {
	// Truncate to first line, then to max chars
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// filterFindingsBySeverity partitions findings into high, medium, low, and criteria.
func filterFindingsBySeverity(findings []agent.ReviewFinding) (high, medium, low, criteria []agent.ReviewFinding) {
	for _, f := range findings {
		switch strings.ToUpper(f.Severity) {
		case "HIGH":
			high = append(high, f)
		case "MEDIUM":
			medium = append(medium, f)
		case "CRITERIA":
			criteria = append(criteria, f)
		default:
			low = append(low, f)
		}
	}
	return
}

// buildBatchSummaryComment creates the approval comment with batch statistics.
func buildBatchSummaryComment(allIssues []*platform.Issue, reviewSummary string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("✅ **HerdOS Agent Review**\n\n%s\n", reviewSummary))

	// Count statistics from issues
	originalTasks := 0
	reviewFixIssues := 0
	ciFixIssues := 0
	maxFixCycle := 0
	maxCIFixCycle := 0
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		switch {
		case parsed.FrontMatter.CIFixCycle > 0:
			ciFixIssues++
		case parsed.FrontMatter.Type == "fix":
			reviewFixIssues++
		default:
			originalTasks++
		}
		if parsed.FrontMatter.FixCycle > maxFixCycle {
			maxFixCycle = parsed.FrontMatter.FixCycle
		}
		if parsed.FrontMatter.CIFixCycle > maxCIFixCycle {
			maxCIFixCycle = parsed.FrontMatter.CIFixCycle
		}
	}

	b.WriteString("\n📊 **Batch Summary**\n\n")
	b.WriteString(fmt.Sprintf("- Original tasks: %d\n", originalTasks))
	b.WriteString(fmt.Sprintf("- Review fix issues: %d\n", reviewFixIssues))
	b.WriteString(fmt.Sprintf("- CI fix issues: %d\n", ciFixIssues))
	b.WriteString(fmt.Sprintf("- Review cycles: %d\n", maxFixCycle))
	b.WriteString(fmt.Sprintf("- CI fix cycles: %d\n", maxCIFixCycle))
	b.WriteString(fmt.Sprintf("- Total issues: %d\n", len(allIssues)))

	return b.String()
}

func buildStaleReviewResultComment(reviewedHeadSHA, currentHeadSHA string) string {
	return fmt.Sprintf("⚠️ **HerdOS Agent Review**\n\nHerdOS discarded this review result because the PR head changed while the review was running.\n\nReviewed head SHA: `%s`\nCurrent head SHA: `%s`\n\nThe updated diff will be reviewed on a later trigger, or you can run `/herd review` manually.",
		reviewedHeadSHA, currentHeadSHA)
}

func buildActiveReviewLockSkipComment(state reviewLockState, currentHeadSHA string) string {
	var b strings.Builder
	b.WriteString("⚠️ **HerdOS Agent Review**\n\n")
	b.WriteString("`/herd review` was skipped because another review lock is active.\n\n")
	if state.Owner != "" {
		b.WriteString(fmt.Sprintf("- Owner: `%s`\n", state.Owner))
	}
	if state.AcquiredAt != nil {
		b.WriteString(fmt.Sprintf("- Acquired at: `%s`\n", formatReviewLockTime(state.AcquiredAt)))
	}
	if state.ExpiresAt != nil {
		b.WriteString(fmt.Sprintf("- Expires at: `%s`\n", formatReviewLockTime(state.ExpiresAt)))
	}
	if state.BatchBranchSHA != "" {
		b.WriteString(fmt.Sprintf("- Recorded head SHA: `%s`\n", state.BatchBranchSHA))
	}
	if currentHeadSHA != "" {
		b.WriteString(fmt.Sprintf("- Current head SHA: `%s`\n", currentHeadSHA))
	}
	b.WriteString("\nWait for the active review to finish or for the lock to expire before retrying.")
	return b.String()
}

func buildReclaimedStaleReviewLockComment(state reviewLockState, currentHeadSHA string) string {
	var b strings.Builder
	b.WriteString("ℹ️ Herd reclaimed a stale review lock from an older PR head and is continuing review for the current head.")
	if state.BatchBranchSHA != "" || currentHeadSHA != "" {
		b.WriteString("\n\n")
	}
	if state.BatchBranchSHA != "" {
		b.WriteString(fmt.Sprintf("- Locked head: `%s`\n", state.BatchBranchSHA))
	}
	if currentHeadSHA != "" {
		b.WriteString(fmt.Sprintf("- Current head: `%s`", currentHeadSHA))
	}
	return b.String()
}

func formatReviewLockTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// buildReviewCycleComment creates a structured PR comment for a review cycle.
func buildReviewCycleComment(cycle, maxCycles int, fixIssueNums []int, high, medium, low, criteria []agent.ReviewFinding) string {
	var b strings.Builder

	totalFindings := len(high) + len(medium) + len(low) + len(criteria)

	if cycle > 0 {
		b.WriteString(fmt.Sprintf("🔍 **HerdOS Agent Review** (cycle %d of %d)\n\n", cycle, maxCycles))
	} else {
		b.WriteString("🔍 **HerdOS Agent Review**\n\n")
	}
	if totalFindings == 0 {
		b.WriteString("No issues found.\n")
		return b.String()
	} else if totalFindings == 1 {
		b.WriteString("Found 1 issue:\n\n")
	} else {
		b.WriteString(fmt.Sprintf("Found %d issues:\n\n", totalFindings))
	}

	if len(high) > 0 {
		if len(fixIssueNums) > 0 {
			nums := make([]string, len(fixIssueNums))
			for i, n := range fixIssueNums {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			b.WriteString(fmt.Sprintf("**HIGH** (fix worker dispatched → %s):\n", strings.Join(nums, ", ")))
		} else {
			b.WriteString("**HIGH**:\n")
		}
		for _, f := range high {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	if len(medium) > 0 {
		if len(fixIssueNums) > 0 {
			nums := make([]string, len(fixIssueNums))
			for i, n := range fixIssueNums {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			b.WriteString(fmt.Sprintf("**MEDIUM** (fix worker dispatched → %s):\n", strings.Join(nums, ", ")))
		} else {
			b.WriteString("**MEDIUM**:\n")
		}
		for _, f := range medium {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	if len(low) > 0 {
		b.WriteString("**LOW** (informational):\n")
		for _, f := range low {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	if len(criteria) > 0 {
		b.WriteString("**CRITERIA** (requires human review):\n")
		for _, f := range criteria {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// extractFindingLines extracts individual finding descriptions from a fix
// issue body. Findings are stored as numbered list items ("1. description").
// Matching against individual lines avoids false-positive substring matches
// when multiple findings are concatenated in a single batched issue body.
func extractFindingLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		// Strip leading number+dot prefix (e.g. "1. ", "12. ")
		if len(line) > 2 {
			dotIdx := strings.Index(line, ". ")
			if dotIdx > 0 && dotIdx <= 4 {
				prefix := line[:dotIdx]
				if _, err := strconv.Atoi(prefix); err == nil {
					text := strings.TrimSpace(line[dotIdx+2:])
					if text != "" {
						lines = append(lines, text)
					}
					continue
				}
			}
		}
		// Skip bare numbered-list markers (e.g. "1." with no content after
		// trimming). These arise when a list item like "1. " is trimmed to
		// "1." and falls through the dot-space detection above.
		if len(line) >= 2 && len(line) <= 5 && line[len(line)-1] == '.' {
			if _, err := strconv.Atoi(line[:len(line)-1]); err == nil {
				continue
			}
		}
		// Also include raw non-empty lines for simple (non-batched) bodies
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// minSubstringLen is the minimum description prefix length for substring
// matching in dedupFindings. Descriptions shorter than this use equality
// to avoid false positives (e.g. "bug" matching any line containing "bug").
const minSubstringLen = 20

// descriptionMatch reports whether text contains a match for descPrefix.
// For short prefixes (< minSubstringLen) it requires equality; for longer
// ones it uses substring containment.
func descriptionMatch(text, descPrefix string) bool {
	if len(descPrefix) < minSubstringLen {
		return text == descPrefix
	}
	return strings.Contains(text, descPrefix)
}

// activeFixIssues returns fix-typed issues whose herd status is in-progress
// or ready. Issues with status done/failed are past attempts and must not
// suppress recurring findings — a reviewer flagging the same problem again
// is evidence the previous fix attempt did not take.
func activeFixIssues(allIssues []*platform.Issue) []*platform.Issue {
	var out []*platform.Issue
	for _, iss := range allIssues {
		if iss.State == "closed" {
			continue
		}
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.Type != "fix" {
			continue
		}
		status := issues.StatusLabel(iss.Labels)
		if status == issues.StatusInProgress || status == issues.StatusReady {
			out = append(out, iss)
		}
	}
	return out
}

// dedupFindings removes findings that are similar to existing open fix issues.
func dedupFindings(findings []agent.ReviewFinding, openFixIssues []*platform.Issue) []agent.ReviewFinding {
	var deduped []agent.ReviewFinding
	for _, f := range findings {
		descPrefix := f.Description
		if len(descPrefix) > 100 {
			descPrefix = descPrefix[:100]
		}
		duplicate := false
		for _, iss := range openFixIssues {
			if descriptionMatch(iss.Title, descPrefix) {
				fmt.Printf("Skipping duplicate finding: similar to #%d\n", iss.Number)
				duplicate = true
				break
			}
			// Match against individual finding lines rather than the
			// raw body to avoid false positives in batched issues.
			for _, line := range extractFindingLines(iss.Body) {
				if descriptionMatch(line, descPrefix) {
					fmt.Printf("Skipping duplicate finding: similar to #%d\n", iss.Number)
					duplicate = true
					break
				}
			}
			if duplicate {
				break
			}
		}
		if !duplicate {
			deduped = append(deduped, f)
		}
	}
	return deduped
}

// findingMatchesAnyVerdict returns the index of the first verdict in
// verdicts that contains a substring of the finding's description (using
// the same first-100-chars heuristic as dedupFindings). Returns -1 when
// no verdict matches.
func findingMatchesAnyVerdict(finding agent.ReviewFinding, verdicts []string) int {
	descPrefix := finding.Description
	if len(descPrefix) > 100 {
		descPrefix = descPrefix[:100]
	}
	for i, v := range verdicts {
		if descriptionMatch(v, descPrefix) {
			return i
		}
	}
	return -1
}

// stableDisagreementBlocked partitions findings into those that match a
// previous worker no-op verdict (blocked) and those that are genuinely
// new (kept). When blocked is non-empty, the integrator labels the PR
// herd/stable-disagreement, posts a help-needed comment, and does NOT
// dispatch a fix worker for the kept findings either — the entire cycle
// halts so the user can decide.
func stableDisagreementBlocked(findings []agent.ReviewFinding, verdicts []string) (blocked, kept []agent.ReviewFinding, verdictIdxByBlocked []int) {
	for _, f := range findings {
		if idx := findingMatchesAnyVerdict(f, verdicts); idx >= 0 {
			blocked = append(blocked, f)
			verdictIdxByBlocked = append(verdictIdxByBlocked, idx)
		} else {
			kept = append(kept, f)
		}
	}
	return
}

// buildStableDisagreementComment renders the help-needed comment posted on
// the batch PR when a reviewer↔worker stalemate is detected. It includes
// each blocked finding alongside a short snippet of the worker verdict
// that previously cleared it, plus three numbered resolution options that
// guide the user out of the deadlock.
func buildStableDisagreementComment(blocked []agent.ReviewFinding, verdictIdx []int, verdicts []string) string {
	var b strings.Builder
	b.WriteString("⚠️ **Stable disagreement detected**\n\n")
	b.WriteString("The reviewer has flagged findings that a previous fix worker already determined to be no-ops. Continuing would loop indefinitely.\n\n")
	b.WriteString("Findings flagged again this cycle:\n")
	for i, f := range blocked {
		var summary string
		if i < len(verdictIdx) && verdictIdx[i] < len(verdicts) {
			summary = summarizeVerdict(verdicts[verdictIdx[i]])
		}
		if summary != "" {
			b.WriteString(fmt.Sprintf("- %s (worker no-op: %s)\n", f.Description, summary))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
	}
	b.WriteString("\n")
	b.WriteString("Resolution options:\n")
	b.WriteString("1. If the workers were right and the code is correct as-is, post a `/herd fix` with explicit acceptance criteria that close out these findings, or close the PR review with `/herd integrate` to merge if you're satisfied.\n")
	b.WriteString("2. If the reviewer is right and the workers missed something, post a `/herd fix` with concrete file:line evidence that contradicts the worker verdicts.\n")
	b.WriteString("3. Remove the `herd/stable-disagreement` label and post `/herd integrate` to resume automatic reviews.\n")
	return b.String()
}

// summarizeVerdict extracts the first non-empty bullet line from a
// worker no-op verdict body, returning a short snippet (max 200 chars)
// suitable for inline use in the stable-disagreement comment.
func summarizeVerdict(verdict string) string {
	for _, line := range strings.Split(verdict, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		return text
	}
	// Fallback: take the line after "Conclusion:" if present.
	for _, line := range strings.Split(verdict, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Conclusion:") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "Conclusion:"))
			if len(text) > 200 {
				text = text[:200] + "…"
			}
			return text
		}
	}
	return ""
}
