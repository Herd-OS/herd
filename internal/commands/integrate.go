package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

func handleIntegrate(hctx *HandlerContext, cmd Command) Result {
	ctx := hctx.Ctx

	// Extract batch number based on context (PR vs issue).
	var batchNum int
	if hctx.IsPR {
		pr, err := hctx.Platform.PullRequests().Get(ctx, hctx.IssueNumber)
		if err != nil {
			return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
		}
		if !strings.HasPrefix(pr.Head, "herd/batch/") {
			return Result{Message: "⚠️ `/herd integrate` can only be used on batch PRs or issues with a batch frontmatter."}
		}
		num, err := integrator.ParseBatchBranchMilestone(pr.Head)
		if err != nil {
			return Result{Error: fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)}
		}
		batchNum = num
	} else {
		parsed, err := issues.ParseBody(hctx.IssueBody)
		if err != nil {
			return Result{Error: fmt.Errorf("parsing issue body: %w", err)}
		}
		if parsed.FrontMatter.Batch == 0 {
			return Result{Message: "⚠️ This issue has no batch number in its frontmatter."}
		}
		batchNum = parsed.FrontMatter.Batch
	}

	// Get milestone.
	ms, err := hctx.Platform.Milestones().Get(ctx, batchNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting milestone #%d: %w", batchNum, err)}
	}
	if ms.State == "closed" {
		return Result{Message: fmt.Sprintf("⏭️ Batch #%d is already closed — skipping.", batchNum)}
	}

	// List all issues in milestone.
	allIssues, err := hctx.Platform.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return Result{Error: fmt.Errorf("listing milestone issues: %w", err)}
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	// Consolidate unconsolidated worker branches.
	consolidated := 0
	var consolidateErrors []string
	needsGit := false

	// First pass: check if any branches need consolidation.
	for _, iss := range allIssues {
		if issues.StatusLabel(iss.Labels) != issues.StatusDone {
			continue
		}
		workerBranch := fmt.Sprintf("herd/worker/%d-%s", iss.Number, planner.Slugify(iss.Title))
		if _, err := hctx.Platform.Repository().GetBranchSHA(ctx, workerBranch); err != nil {
			continue // branch already consolidated/deleted
		}
		needsGit = true
		break
	}

	if needsGit && hctx.Git != nil {
		// Configure git identity and fetch once.
		_ = hctx.Git.ConfigureIdentity("HerdOS Integrator", "herd@herd-os.com")
		_ = hctx.Git.Fetch("origin")

		for _, iss := range allIssues {
			if issues.StatusLabel(iss.Labels) != issues.StatusDone {
				continue
			}
			workerBranch := fmt.Sprintf("herd/worker/%d-%s", iss.Number, planner.Slugify(iss.Title))
			if _, err := hctx.Platform.Repository().GetBranchSHA(ctx, workerBranch); err != nil {
				continue // branch already consolidated/deleted
			}

			if err := hctx.Git.Checkout(batchBranch); err != nil {
				consolidateErrors = append(consolidateErrors, fmt.Sprintf("%s: checkout failed: %v", workerBranch, err))
				continue
			}

			if err := hctx.Git.Merge("origin/" + workerBranch); err != nil {
				_ = hctx.Git.AbortMerge()
				consolidateErrors = append(consolidateErrors, fmt.Sprintf("%s: merge conflict", workerBranch))
				continue
			}

			// Remove WORKER_PROGRESS.md if present.
			progressFile := filepath.Join(hctx.RepoRoot, "WORKER_PROGRESS.md")
			if _, statErr := os.Stat(progressFile); statErr == nil {
				if rmErr := hctx.Git.Rm("WORKER_PROGRESS.md"); rmErr == nil {
					if amendErr := hctx.Git.AmendNoEdit(); amendErr != nil {
						_ = hctx.Git.ResetHead()
					}
				}
			}

			if err := hctx.Git.Push("origin", batchBranch); err != nil {
				consolidateErrors = append(consolidateErrors, fmt.Sprintf("%s: push failed: %v", workerBranch, err))
				continue
			}

			// Delete worker branch (non-fatal).
			_ = hctx.Platform.Repository().DeleteBranch(ctx, workerBranch)
			consolidated++
		}
	}

	// Run integrator steps.
	var lines []string
	lines = append(lines, fmt.Sprintf("🔄 **Integrator cycle for batch #%d**\n", batchNum))

	if consolidated > 0 || len(consolidateErrors) > 0 {
		lines = append(lines, fmt.Sprintf("- Consolidated %d worker branch(es)", consolidated))
		for _, e := range consolidateErrors {
			lines = append(lines, fmt.Sprintf("- ⚠️ Consolidation skipped: %s", e))
		}
	} else {
		lines = append(lines, "- No unconsolidated worker branches")
	}

	// Check CI.
	ciResult, ciErr := integrator.CheckCI(ctx, hctx.Platform, hctx.Config, integrator.CheckCIParams{
		BatchNumber: batchNum,
		RepoRoot:    hctx.RepoRoot,
	})
	if ciErr != nil {
		lines = append(lines, fmt.Sprintf("- CI check: error (%v)", ciErr))
	} else if ciResult.Skipped {
		lines = append(lines, "- CI check: skipped (disabled)")
	} else {
		lines = append(lines, fmt.Sprintf("- CI status: %s", ciResult.Status))
	}

	// Advance.
	advResult, advErr := integrator.AdvanceByBatch(ctx, hctx.Platform, hctx.Git, hctx.Config, batchNum)
	if advErr != nil {
		lines = append(lines, fmt.Sprintf("- Advance: error (%v)", advErr))
	} else if advResult.AllComplete {
		lines = append(lines, "- All tiers complete")
		if advResult.DispatchedCount > 0 {
			lines = append(lines, fmt.Sprintf("- Dispatched %d worker(s)", advResult.DispatchedCount))
		}
	} else if advResult.TierComplete && advResult.DispatchedCount > 0 {
		lines = append(lines, fmt.Sprintf("- Tier complete, dispatched %d worker(s) for next tier", advResult.DispatchedCount))
	} else if advResult.DispatchedCount > 0 {
		lines = append(lines, fmt.Sprintf("- Dispatched %d worker(s)", advResult.DispatchedCount))
	} else {
		lines = append(lines, "- Advance: no action needed")
	}

	// Review: run if advance opened a PR or a batch PR already exists.
	ranReview := false
	if advResult != nil && advResult.AllComplete {
		_, reviewErr := integrator.Review(ctx, hctx.Platform, hctx.Agent, hctx.Git, hctx.Config, integrator.ReviewParams{
			BatchNumber: batchNum,
			RepoRoot:    hctx.RepoRoot,
		})
		if reviewErr != nil {
			lines = append(lines, fmt.Sprintf("- Review: error (%v)", reviewErr))
		} else {
			lines = append(lines, "- Review: completed")
		}
		ranReview = true
	}

	if !ranReview {
		// Check if a batch PR already exists.
		existingPRs, err := hctx.Platform.PullRequests().List(ctx, platform.PRFilters{
			State: "open",
			Head:  batchBranch,
		})
		if err == nil && len(existingPRs) > 0 {
			_, reviewErr := integrator.Review(ctx, hctx.Platform, hctx.Agent, hctx.Git, hctx.Config, integrator.ReviewParams{
				BatchNumber: batchNum,
				RepoRoot:    hctx.RepoRoot,
			})
			if reviewErr != nil {
				lines = append(lines, fmt.Sprintf("- Review: error (%v)", reviewErr))
			} else {
				lines = append(lines, "- Review: completed")
			}
		} else {
			lines = append(lines, "- Review: skipped (no batch PR)")
		}
	}

	return Result{Message: strings.Join(lines, "\n")}
}
