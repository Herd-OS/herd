package worker

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/images"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// ExecParams holds the parameters for executing a worker task.
type ExecParams struct {
	IssueNumber int
	RepoRoot    string
	HTTPClient  *http.Client // nil means skip image downloading
	// Mode selects the execution flow: "batch" (default) or "standalone".
	Mode string
}

// ExecResult holds the result of a worker execution.
type ExecResult struct {
	WorkerBranch string
	NoOp         bool // true if no changes were needed
}

const workerPromptTemplate = `You are a HerdOS worker executing a task.

## Branch & PR Discipline (do not violate)

- You are already on the worker branch ` + "`{{.WorkerBranch}}`" + `. STAY on it.
- Do NOT create new branches, Do NOT open new pull requests.
- Do NOT push to any branch other than ` + "`{{.WorkerBranch}}`" + `.
- The herd Integrator will consolidate your worker branch into the batch branch automatically.
- If the task description says "create a new branch", "open a PR against ...", "submit a separate PR", or anything similar, IGNORE those instructions — they were written for a different workflow. Do the actual code change directly on this worker branch.

## Task

{{.Title}}

{{.Body}}

## Instructions

- The issue body is your primary source of context. Start there.
- If the issue includes Implementation Details, Conventions, or Context
  from Dependencies sections, follow them closely — the Planner wrote
  them specifically for you.
- If the issue lacks information you need, explore the codebase to fill
  the gaps. But prefer what the issue says over what you infer.
- Check if the acceptance criteria are already satisfied by existing
  code. If so, exit successfully without making any commits, BUT
  before exiting you MUST emit a verdict report in your final output
  in EXACTLY this format (the orchestrator parses it and forwards it
  to the next review cycle):

      Findings reviewed against the current code:

      - **<finding 1 summary>**: <verdict — why no change is needed, with specific file:line references>
      - **<finding 2 summary>**: <verdict>

      Conclusion: <one-line summary, e.g., "All 3 findings describe behavior that already exists.">

  This is non-negotiable. If you determine no code change is needed
  you MUST include this block in your final output. Do not exit the
  no-op path without it. Reference specific file:line locations in
  the verdicts so the next reviewer can verify your reasoning.
{{if .IsFixIssue}}- **This is a fix issue created by the reviewer.** Focus on the specific
  problem described in the Task section. The reviewer found an issue that
  may not be covered by the original acceptance criteria. Do not dismiss
  the concern just because existing code passes the original criteria.
  If after careful analysis you genuinely believe the reviewer is wrong,
  explain your reasoning in detail in your output rather than silently
  doing nothing.
{{end}}- If the task describes a merge or rebase conflict, follow the explicit
  git commands in the task. Do not skip the git merge or rebase step and
  try to manually rewrite files. You must resolve the actual conflict
  markers produced by git.
- Focus on files listed in the Scope or Files to Modify sections. You
  may modify other files if necessary to satisfy acceptance criteria.
- Commit your changes with clear messages referencing issue #{{.IssueNumber}}.
- Do not add features, refactor code, or make improvements beyond
  what is specified in the issue.
- You are running in a fully automated CI environment with no human
  present. Do not pause, ask questions, or wait for confirmation.
  Figure it out. If something is broken, try to fix it. If a tool
  fails, try a different approach. Only exit with a non-zero status
  if the task is genuinely impossible (e.g., the issue references
  code that doesn't exist and can't be inferred).
- If you cannot complete the task after exhausting alternatives,
  exit with a non-zero status and include the reason in your output.
{{if .Retry}}## Validation Failed — Stale Progress

- Pre-push validation FAILED on the code currently in the worktree. The validation errors are in the Task body above and are the ONLY thing you must fix.
- A ` + "`.herd/progress/{{.IssueNumber}}.md`" + ` file may exist from the previous attempt. It is STALE — do NOT honor it, do NOT treat its checked items as proof the work is correct, and do NOT skip work because of it.
- Make the minimal changes needed to make validation pass. Commit and push to ` + "`{{.WorkerBranch}}`" + `.
{{else}}## Incremental Progress

- Your branch is ` + "`{{.WorkerBranch}}`" + `. After completing work on each file or
  logical unit, run ` + "`git push origin {{.WorkerBranch}}`" + ` to save your progress
  remotely. Do not wait until all work is done to push.
- Before your first push, ensure the directory exists (` + "`mkdir -p .herd/progress`" + `) then create a file called .herd/progress/{{.IssueNumber}}.md.
  Update it before each push with a checklist of what you have completed
  and what remains. Format: completed items checked (` + "`- [x]`" + `), remaining items
  unchecked (` + "`- [ ]`" + `).
- The progress file alone no longer causes the worker to skip re-running the
  agent: the worker now also requires that pre-push validation passed last time.
  Keep writing it as before — it is still used to resume a timed-out attempt.
- If .herd/progress/{{.IssueNumber}}.md already exists when you start (from a previous
  timed-out attempt), read it to understand what was already done and continue
  from where it left off. Do not redo completed work.
{{end}}{{if .CoAuthorTrailer}}- Add the following trailer to every commit message (on its own line after a blank line):
  {{.CoAuthorTrailer}}
{{end}}{{if .RoleInstructions}}
## Project-Specific Instructions
{{.RoleInstructions}}
{{end}}`

type promptData struct {
	Title            string
	Body             string
	IssueNumber      int
	WorkerBranch     string
	RoleInstructions string
	CoAuthorTrailer  string
	IsFixIssue       bool
	Retry            bool
}

// Exec runs the full worker lifecycle: reads the issue, creates a worker branch,
// invokes the agent, pushes the branch, and updates labels.
func Exec(ctx context.Context, p platform.Platform, ag agent.Agent, cfg *config.Config, params ExecParams) (result *ExecResult, err error) {
	// Deferred failure handling — must be registered before any error returns
	defer func() {
		if err != nil {
			_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusFailed})
			// Trigger monitor for immediate escalation
			defaultBranch, dbErr := p.Repository().GetDefaultBranch(ctx)
			if dbErr == nil {
				_, _ = p.Workflows().Dispatch(ctx, "herd-monitor.yml", defaultBranch, nil)
			}
		}
	}()

	mode := params.Mode
	if mode == "" {
		mode = "batch"
	}
	if mode == "standalone" {
		return execStandalone(ctx, p, ag, cfg, params)
	}

	// Get issue
	issue, err := p.Issues().Get(ctx, params.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", params.IssueNumber, err)
	}

	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d has no milestone (not part of a batch)", params.IssueNumber)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))
	workerBranch := fmt.Sprintf("herd/worker/%d-%s", params.IssueNumber, planner.Slugify(issue.Title))

	// Git setup: fetch, checkout batch branch, create or resume worker branch
	g := git.New(params.RepoRoot)
	if err = g.Fetch("origin"); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}

	// Check if worker branch already exists remotely (previous timed-out attempt)
	_, remoteBranchErr := p.Repository().GetBranchSHA(ctx, workerBranch)
	if remoteBranchErr == nil {
		// Resume: checkout existing worker branch
		if err = g.Checkout(workerBranch); err != nil {
			return nil, fmt.Errorf("checking out existing worker branch: %w", err)
		}
		// Merge latest batch branch to avoid operating on a stale base.
		// If the batch branch has advanced (e.g., other workers consolidated),
		// this brings in those changes and prevents avoidable merge conflicts.
		if err = g.ConfigureIdentity("HerdOS Worker", "herd@herd-os.com"); err != nil {
			return nil, fmt.Errorf("configuring git identity for merge: %w", err)
		}
		if mergeErr := g.Merge("origin/" + batchBranch); mergeErr != nil {
			// Merge conflict — abort and fall back to a fresh branch
			fmt.Fprintf(os.Stderr, "warning: Merge conflict when updating resumed worker branch, starting fresh from batch branch.\n")
			_ = g.AbortMerge()

			// Checkout batch branch so we can delete the worker branch
			if err = g.Checkout(batchBranch); err != nil {
				return nil, fmt.Errorf("checking out batch branch after merge conflict: %w", err)
			}

			// Delete old worker branch locally and remotely
			if err = g.DeleteLocalBranch(workerBranch); err != nil {
				return nil, fmt.Errorf("deleting local worker branch after merge conflict: %w", err)
			}
			_ = p.Repository().DeleteBranch(ctx, workerBranch) // best-effort remote delete

			// Remove stale progress file from previous attempt
			progressFile := filepath.Join(params.RepoRoot, ".herd", "progress", fmt.Sprintf("%d.md", params.IssueNumber))
			_ = os.Remove(progressFile)

			// Create fresh worker branch from batch branch
			if err = g.CreateBranch(workerBranch, batchBranch); err != nil {
				return nil, fmt.Errorf("creating fresh worker branch after merge conflict: %w", err)
			}
		}
	} else {
		// Fresh start: checkout batch branch, create worker branch
		if err = g.Checkout(batchBranch); err != nil {
			return nil, fmt.Errorf("checking out batch branch: %w", err)
		}
		if err = g.CreateBranch(workerBranch, batchBranch); err != nil {
			return nil, fmt.Errorf("creating worker branch: %w", err)
		}
	}

	// Check if previous attempt already completed all work AND validation passed.
	// Both the progress file and the worker-written validation marker are required:
	// a completed progress file alone is no longer trusted as proof the work is
	// correct, otherwise a broken-but-"complete" attempt would short-circuit every
	// retry with the same failing code.
	skipAgent := false
	if remoteBranchErr == nil && checkProgressComplete(params.RepoRoot, params.IssueNumber) && checkValidationPassed(params.RepoRoot, params.IssueNumber) {
		fmt.Printf("Progress file shows all work complete and validation passed for issue #%d — skipping agent execution.\n", params.IssueNumber)
		skipAgent = true
	}

	// Download GitHub attachment images so the agent can view them locally
	issueBody := issue.Body
	if !skipAgent && params.HTTPClient != nil {
		imgDir := filepath.Join(params.RepoRoot, ".herd", "tmp", "images")
		processedBody, imgErr := images.DownloadAndReplace(ctx, params.HTTPClient, issue.Body, imgDir)
		if imgErr != nil {
			fmt.Fprintf(os.Stderr, "warning: image download failed: %v\n", imgErr)
		} else {
			issueBody = processedBody
		}
	}

	rawSummary := ""
	if skipAgent {
		rawSummary = "Skipped — previous attempt completed all work and validation passed (detected via progress file and validation marker)."
	} else {
		// If we are resuming a previous attempt whose validation failed (progress
		// complete but no marker), inject the saved validation errors so the agent
		// knows what was broken instead of re-validating the same failing code.
		// In that case we also render the retry prompt variant so the agent is told
		// the progress file is stale and must not be honored — otherwise the all-`[x]`
		// checklist could lead it to conclude there is nothing left to do.
		resumeAfterValidationFailure := false
		if remoteBranchErr == nil {
			if prevErrs, ok := readValidationErrors(params.RepoRoot, params.IssueNumber); ok && checkProgressComplete(params.RepoRoot, params.IssueNumber) {
				issueBody += fmt.Sprintf("\n\n## Previous attempt's validation failed with:\n\n```\n%s\n```\n", prevErrs)
				resumeAfterValidationFailure = true
			}
		}

		// Build system prompt only when agent will actually run
		systemPrompt, err := renderWorkerPrompt(issue.Title, issueBody, params.IssueNumber, workerBranch, params.RepoRoot, cfg, resumeAfterValidationFailure)
		if err != nil {
			return nil, fmt.Errorf("rendering worker prompt: %w", err)
		}

		execOpts := agent.ExecOptions{
			RepoRoot:     params.RepoRoot,
			SystemPrompt: systemPrompt,
			MaxTurns:     cfg.Agent.MaxTurns,
		}

		// Execute task
		taskSpec := agent.TaskSpec{
			IssueNumber: params.IssueNumber,
			Title:       issue.Title,
			Body:        issueBody,
		}

		// Start progress poster — updates a comment on the issue periodically
		// with the contents of the progress file.
		progressDone := make(chan struct{})
		go postProgressUpdates(ctx, p, params.IssueNumber, params.RepoRoot, cfg.Workers.ProgressIntervalSeconds, progressDone)

		// Use a timeout for agent execution so the worker has time to push
		// whatever work was done if the agent hangs after completing its task.
		agentTimeout := time.Duration(cfg.Workers.TimeoutMinutes)*time.Minute - 5*time.Minute
		if agentTimeout < 5*time.Minute {
			agentTimeout = 5 * time.Minute
		}
		// Remove any stale validation marker so a previous pass cannot carry over;
		// the marker is re-created only after runValidation reports allPassed().
		_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
		agentCtx, agentCancel := context.WithTimeout(ctx, agentTimeout)
		agentResult, err := ag.Execute(agentCtx, taskSpec, execOpts)
		agentCancel()
		close(progressDone)

		if err != nil {
			// If the agent timed out, check if work was done — if so, continue to push
			if agentCtx.Err() == context.DeadlineExceeded {
				fmt.Printf("Agent execution timed out after %s, checking for completed work...\n", agentTimeout)
				diff, diffErr := g.Diff(batchBranch, "HEAD")
				if diffErr == nil && diff != "" {
					fmt.Println("Work detected despite timeout — proceeding to push.")
					rawSummary = "Agent timed out but work was completed."
					goto pushWork
				}
			}
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("**Worker failed:** agent returned an error.\n\n```\n%s\n```\n\nThis issue will be retried by the monitor.",
					truncateOutput(err.Error(), 2000)))
			return nil, fmt.Errorf("agent execution failed: %w", err)
		}

		if agentResult != nil && agentResult.Summary != "" {
			rawSummary = agentResult.Summary
		}
	}

pushWork:
	// Check for no-op (no commits made)
	diff, diffErr := g.Diff(batchBranch, "HEAD")
	if diffErr != nil {
		return nil, fmt.Errorf("checking for changes: %w", diffErr)
	}

	if diff == "" {
		// Check if this is a stale conflict resolution issue
		parsed, parseErr := issues.ParseBody(issue.Body)
		if parseErr == nil && parsed.FrontMatter.ConflictResolution {
			// This conflict resolution task produced no changes — the batch branch
			// is already up to date. Close the issue instead of marking done,
			// so the integrator doesn't try to consolidate a no-op worker branch.
			fmt.Printf("Conflict resolution issue #%d is no longer needed — closing.\n", params.IssueNumber)
			_ = p.Issues().AddComment(ctx, params.IssueNumber, "Automatically closed — conflict resolution is no longer needed. The batch branch is already up to date.")
			_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
			state := "closed"
			_, _ = p.Issues().Update(ctx, params.IssueNumber, platform.IssueUpdate{State: &state})
			return &ExecResult{NoOp: true}, nil
		}

		// Empty diff means the agent made no commits. There are two legitimate
		// reasons for this and one failure mode we MUST NOT silently mark
		// "done":
		//
		//   1. The agent inspected the codebase and determined the acceptance
		//      criteria are already satisfied. The prompt at the top of this
		//      file REQUIRES the agent to emit a structured verdict block
		//      ("Findings reviewed against the current code:" + "Conclusion:")
		//      in its final output when it takes this path. Presence of the
		//      block is our positive proof of intent.
		//   2. The agent crashed, was blocked by its sandbox, or otherwise
		//      failed before it could do any work. Empty diff, no verdict
		//      block. Historically the worker treated this identically to
		//      case 1 and labeled the issue `done`, silently succeeding the
		//      task without any code change — observed during the Codex
		//      sandbox bubblewrap incident on TrueNAS where every shell and
		//      apply_patch call exited with "No permissions to create a new
		//      namespace" before the agent could read a single file, yet the
		//      worker still flipped #729/#730/#731 to `done`.
		//
		// Gate the no-op success path on the verdict block being present.
		// Missing block returns an error so the deferred handler labels
		// the issue `failed` and the monitor can re-dispatch.
		if !hasNoOpVerdictBlock(rawSummary) {
			truncated := truncateOutput(rawSummary, 4000)
			if truncated == "" {
				truncated = "(no agent output captured)"
			}
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("**Worker failed:** the agent produced no commits AND did not emit the required no-op verdict block (`Findings reviewed against the current code:` + `Conclusion:`). This usually means the agent crashed or was blocked by its sandbox before doing any work — not that the task was already complete. This issue will be retried by the monitor.\n\n<details>\n<summary>Agent output (last 4000 chars)</summary>\n\n```\n%s\n```\n\n</details>",
					truncated))
			return nil, fmt.Errorf("worker produced empty diff without a no-op verdict block (agent likely crashed or was sandboxed off)")
		}

		// Verified intentional no-op — post report and label done without pushing.
		noOpReport := "**Worker Report**\n\nNo changes were needed — acceptance criteria already satisfied.\n"
		if rawSummary != "" {
			noOpReport += fmt.Sprintf("\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
		}
		_ = p.Issues().AddComment(ctx, params.IssueNumber, noOpReport)

		// Post a structured verdict comment on the batch PR so the next
		// review cycle can pick up the worker's reasoning.
		prs, _ := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if len(prs) > 0 {
			verdict := buildNoOpVerdictComment(params.IssueNumber, rawSummary)
			_ = p.PullRequests().AddComment(ctx, prs[0].Number, verdict)
		}

		_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress, issues.StatusFailed})
		_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
		return &ExecResult{NoOp: true}, nil
	}

	// Run pre-push validation
	validation := runValidation(ctx, params.RepoRoot)

	if !validation.allPassed() {
		fmt.Printf("Validation failed, re-invoking agent to fix:\n%s\n", validation.Errors)

		// Persist the failure so a subsequent outer retry has context, and clear
		// the marker so the failing code cannot be treated as validated.
		_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
		_ = writeValidationErrors(params.RepoRoot, params.IssueNumber, validation.Errors)
		commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

		// Build system prompt for the retry agent invocation. Use the retry
		// variant so the agent does NOT honor the stale progress file.
		retryPrompt, rpErr := renderWorkerPrompt(issue.Title, issueBody, params.IssueNumber, workerBranch, params.RepoRoot, cfg, true)
		if rpErr != nil {
			return nil, fmt.Errorf("rendering worker prompt for retry: %w", rpErr)
		}
		retryExecOpts := agent.ExecOptions{
			RepoRoot:     params.RepoRoot,
			SystemPrompt: retryPrompt,
			MaxTurns:     cfg.Agent.MaxTurns,
		}

		// Re-invoke agent with validation errors
		retrySpec := agent.TaskSpec{
			IssueNumber: params.IssueNumber,
			Title:       "Fix validation failures",
			Body:        fmt.Sprintf("The following validation commands failed after your changes. Please fix all errors:\n\n```\n%s\n```\n\nDo not add new features — only fix the validation errors.", validation.Errors),
		}
		// Remove any stale marker right before the retry invocation too.
		_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
		retryResult, retryErr := ag.Execute(ctx, retrySpec, retryExecOpts)
		if retryErr != nil {
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("**Worker failed:** agent returned an error during validation retry.\n\n```\n%s\n```\n\nThis issue will be retried by the monitor.",
					truncateOutput(retryErr.Error(), 2000)))
			return nil, fmt.Errorf("agent retry after validation failure: %w", retryErr)
		}
		if retryResult != nil && retryResult.Summary != "" {
			rawSummary += "\n\n--- Retry output ---\n" + retryResult.Summary
		}

		// Re-run validation
		validation = runValidation(ctx, params.RepoRoot)
		if !validation.allPassed() {
			// Persist the failure so the next outer attempt has context.
			_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
			_ = writeValidationErrors(params.RepoRoot, params.IssueNumber, validation.Errors)
			commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

			// Post report with failure info, then fail
			report := buildWorkerReport(g, batchBranch, rawSummary, validation)
			report += "\n\n⚠️ Validation failed after retry. Worker marked as failed."
			if rawSummary != "" {
				report += fmt.Sprintf("\n\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
			}
			_ = p.Issues().AddComment(ctx, params.IssueNumber, report)
			return nil, fmt.Errorf("validation failed after retry: %s", validation.Errors)
		}
	}

	// Validation passed — record the marker and clear any saved errors so the
	// next worker invocation can safely skip the agent.
	if err = writeValidationMarker(params.RepoRoot, params.IssueNumber); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing validation marker for #%d: %v\n", params.IssueNumber, err)
	}
	_ = removeValidationErrors(params.RepoRoot, params.IssueNumber)
	commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

	// Force push worker branch — previous failed attempts may have left
	// stale commits on the remote branch that would cause a non-fast-forward rejection.
	// Push BEFORE posting the report so we don't claim success if the push fails.
	if err = g.ForcePush("origin", workerBranch); err != nil {
		return nil, fmt.Errorf("pushing worker branch: %w", err)
	}

	// Build and post structured report (only after successful push)
	report := buildWorkerReport(g, batchBranch, rawSummary, validation)
	if rawSummary != "" {
		report += fmt.Sprintf("\n\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
	}
	_ = p.Issues().AddComment(ctx, params.IssueNumber, report)

	// Label issue as done
	_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress, issues.StatusFailed})
	_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})

	return &ExecResult{
		WorkerBranch: workerBranch,
	}, nil
}

type validationResult struct {
	BuildOK     bool
	TestOK      bool
	VetOK       bool
	LintOK      bool
	LintSkipped bool   // true if golangci-lint not in PATH
	Errors      string // combined error output from failed commands
}

// runValidation runs Go validation commands in the repo root.
// Skips all validation if no go.mod exists in repoRoot.
func runValidation(ctx context.Context, repoRoot string) *validationResult {
	result := &validationResult{}

	// Skip validation for non-Go repos
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); os.IsNotExist(err) {
		result.BuildOK = true
		result.TestOK = true
		result.VetOK = true
		result.LintOK = true
		return result
	}

	var errors strings.Builder

	// go build
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		result.BuildOK = false
		fmt.Fprintf(&errors, "go build failed:\n%s\n", string(out))
	} else {
		result.BuildOK = true
	}

	// go test
	cmd = exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		result.TestOK = false
		fmt.Fprintf(&errors, "go test failed:\n%s\n", string(out))
	} else {
		result.TestOK = true
	}

	// go vet
	cmd = exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		result.VetOK = false
		fmt.Fprintf(&errors, "go vet failed:\n%s\n", string(out))
	} else {
		result.VetOK = true
	}

	// golangci-lint (optional — skip if not installed)
	if _, lookErr := exec.LookPath("golangci-lint"); lookErr != nil {
		result.LintOK = true
		result.LintSkipped = true
	} else {
		cmd = exec.CommandContext(ctx, "golangci-lint", "run", "./...")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			result.LintOK = false
			fmt.Fprintf(&errors, "golangci-lint failed:\n%s\n", string(out))
		} else {
			result.LintOK = true
		}
	}

	result.Errors = errors.String()
	return result
}

func (v *validationResult) allPassed() bool {
	return v.BuildOK && v.TestOK && v.VetOK && v.LintOK
}

func (v *validationResult) statusString() string {
	icon := func(ok bool) string {
		if ok {
			return "✅"
		}
		return "❌"
	}
	s := fmt.Sprintf("%s build | %s test | %s vet", icon(v.BuildOK), icon(v.TestOK), icon(v.VetOK))
	if !v.LintSkipped {
		s += fmt.Sprintf(" | %s lint", icon(v.LintOK))
	}
	return s
}

func buildWorkerReport(g *git.Git, batchBranch string, agentSummary string, validation *validationResult) string {
	var b strings.Builder
	b.WriteString("**Worker Report**\n\n")

	// Files changed
	diffStat, err := g.DiffStat(batchBranch, "HEAD")
	if err == nil && diffStat != "" {
		b.WriteString("**Files changed:**\n```\n")
		b.WriteString(diffStat)
		b.WriteString("\n```\n\n")
	}

	// Agent summary (first paragraph, truncated to 500 chars)
	if agentSummary != "" {
		short := agentSummary
		if idx := strings.Index(short, "\n\n"); idx >= 0 {
			short = short[:idx]
		}
		if len(short) > 500 {
			short = short[:500] + "..."
		}
		b.WriteString("**Summary:** ")
		b.WriteString(short)
		b.WriteString("\n\n")
	}

	// Validation
	if validation != nil {
		b.WriteString("**Validation:** ")
		b.WriteString(validation.statusString())
		b.WriteString("\n")
	}

	return b.String()
}

func truncateOutput(s string, max int) string {
	if len(s) > max {
		return s[:max] + "\n\n... (truncated)"
	}
	return s
}

// checkProgressComplete checks if the worker's progress file indicates all work
// is done. It looks for .herd/progress/<issueNumber>.md first, then falls back
// to WORKER_PROGRESS.md. Returns true if a progress file exists, has at least
// one checkbox, and all checkboxes are checked.
// postProgressUpdates polls the progress file and posts/updates a comment on
// the issue with the current contents. Stops when done is closed.
func postProgressUpdates(ctx context.Context, p platform.Platform, issueNumber int, repoRoot string, intervalSeconds int, done <-chan struct{}) {
	if intervalSeconds <= 0 {
		<-done
		return
	}

	progressFile := filepath.Join(repoRoot, ".herd", "progress", fmt.Sprintf("%d.md", issueNumber))
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	var commentID int64
	var lastContent string

	for {
		select {
		case <-done:
			// Post final update with completed state
			if content, err := os.ReadFile(progressFile); err == nil && string(content) != lastContent {
				body := "📋 **Worker Progress** _(final)_\n\n" + string(content)
				if commentID != 0 {
					_ = p.Issues().UpdateComment(ctx, commentID, body)
				}
			} else if commentID != 0 && lastContent != "" {
				body := "📋 **Worker Progress** _(final)_\n\n" + lastContent
				_ = p.Issues().UpdateComment(ctx, commentID, body)
			}
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			content, err := os.ReadFile(progressFile)
			if err != nil || string(content) == lastContent {
				continue
			}
			lastContent = string(content)
			body := "📋 **Worker Progress** _(live)_\n\n" + lastContent
			if commentID == 0 {
				id, createErr := p.Issues().AddCommentReturningID(ctx, issueNumber, body)
				if createErr == nil {
					commentID = id
				}
			} else {
				_ = p.Issues().UpdateComment(ctx, commentID, body)
			}
		}
	}
}

func checkProgressComplete(repoRoot string, issueNumber int) bool {
	paths := []string{
		filepath.Join(repoRoot, ".herd", "progress", fmt.Sprintf("%d.md", issueNumber)),
		filepath.Join(repoRoot, "WORKER_PROGRESS.md"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return isAllChecked(string(data))
	}
	return false
}

// isAllChecked returns true if the content has at least one checkbox and all
// checkboxes are checked (no unchecked "- [ ]" lines).
func isAllChecked(content string) bool {
	checked := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]") {
			checked++
		} else if strings.HasPrefix(trimmed, "- [ ]") {
			return false
		}
	}
	return checked > 0
}

// validationMarkerPath returns the path to the worker-written marker that
// records that pre-push validation passed for this issue.
func validationMarkerPath(repoRoot string, issueNumber int) string {
	return filepath.Join(repoRoot, ".herd", "progress", fmt.Sprintf("%d.validation", issueNumber))
}

// validationErrorsPath returns the path to the saved validation error output
// from the previous failed attempt.
func validationErrorsPath(repoRoot string, issueNumber int) string {
	return filepath.Join(repoRoot, ".herd", "progress", fmt.Sprintf("%d.validation.errors", issueNumber))
}

// writeValidationMarker writes an empty marker file recording that validation
// passed. Creates the .herd/progress directory if missing.
func writeValidationMarker(repoRoot string, issueNumber int) error {
	path := validationMarkerPath(repoRoot, issueNumber)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte{}, 0o644)
}

// removeValidationMarker removes the validation marker. Missing file is not an error.
func removeValidationMarker(repoRoot string, issueNumber int) error {
	err := os.Remove(validationMarkerPath(repoRoot, issueNumber))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// checkValidationPassed reports whether the validation marker exists.
func checkValidationPassed(repoRoot string, issueNumber int) bool {
	_, err := os.Stat(validationMarkerPath(repoRoot, issueNumber))
	return err == nil
}

const validationErrorsMaxBytes = 16 * 1024

// writeValidationErrors saves the failing validation output (truncated to
// validationErrorsMaxBytes, keeping the TAIL since the most relevant errors are
// usually last) so the next attempt can pass it into the agent prompt.
func writeValidationErrors(repoRoot string, issueNumber int, errs string) error {
	path := validationErrorsPath(repoRoot, issueNumber)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(errs) > validationErrorsMaxBytes {
		errs = "...(truncated)...\n" + errs[len(errs)-validationErrorsMaxBytes:]
	}
	return os.WriteFile(path, []byte(errs), 0o644)
}

// readValidationErrors returns the saved validation errors and true if present.
func readValidationErrors(repoRoot string, issueNumber int) (string, bool) {
	data, err := os.ReadFile(validationErrorsPath(repoRoot, issueNumber))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// removeValidationErrors removes the saved validation errors file. Missing file is not an error.
func removeValidationErrors(repoRoot string, issueNumber int) error {
	err := os.Remove(validationErrorsPath(repoRoot, issueNumber))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// commitValidationStatus stages and commits the validation marker / errors
// files so they survive across worker invocations. Best-effort: logs but does
// not fail the worker if git operations error (e.g. nothing to commit).
func commitValidationStatus(g *git.Git, repoRoot string, issueNumber int, message string) {
	for _, p := range []string{
		validationMarkerPath(repoRoot, issueNumber),
		validationErrorsPath(repoRoot, issueNumber),
	} {
		if _, err := os.Stat(p); err == nil {
			_ = g.Add(p)
		} else if os.IsNotExist(err) {
			// Stage the deletion if the file used to be tracked; ignore "did not match" for never-tracked paths.
			_ = g.Rm(p)
		}
	}
	if err := g.Commit(message); err != nil {
		// Empty commit (nothing to stage) is the common case after the very first marker write; do not log on that.
		if !strings.Contains(err.Error(), "nothing to commit") &&
			!strings.Contains(err.Error(), "no changes added") {
			fmt.Fprintf(os.Stderr, "warning: committing validation status for #%d: %v\n", issueNumber, err)
		}
	}
}

func renderWorkerPrompt(title, body string, issueNumber int, workerBranch string, repoRoot string, cfg *config.Config, retry bool) (string, error) {
	tmpl, err := template.New("worker").Parse(workerPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing worker prompt template: %w", err)
	}

	isFixIssue := false
	if parsed, parseErr := issues.ParseBody(body); parseErr == nil {
		isFixIssue = parsed.FrontMatter.Type == "fix"
	}

	data := promptData{
		Title:        title,
		Body:         body,
		IssueNumber:  issueNumber,
		WorkerBranch: workerBranch,
		IsFixIssue:   isFixIssue,
		Retry:        retry,
	}

	if cfg.PullRequests.CoAuthorEmail != "" {
		data.CoAuthorTrailer = fmt.Sprintf("Co-authored-by: herd-os[bot] <%s>", cfg.PullRequests.CoAuthorEmail)
	}

	// Load role instructions from .herd/worker.md if it exists
	ri, readErr := os.ReadFile(filepath.Join(repoRoot, ".herd", "worker.md"))
	if readErr == nil {
		data.RoleInstructions = string(ri)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing worker prompt template: %w", err)
	}
	return buf.String(), nil
}

const workerStandalonePromptTemplate = `You are a HerdOS worker executing a task in **standalone mode**.

In standalone mode you operate directly on the PR's head branch (` + "`{{.TargetBranch}}`" + `).
Your commits are pushed directly to the target branch — there is no consolidation step.

## Branch & PR Discipline (do not violate)

- You are already on the PR head branch ` + "`{{.TargetBranch}}`" + `. STAY on it.
- Do NOT create new branches, Do NOT open new pull requests.
- Do NOT push to any branch other than ` + "`{{.TargetBranch}}`" + `.
- If the task description says "create a new branch", "open a PR against ...", "submit a separate PR", or anything similar, IGNORE those instructions — they were written for a different workflow. Do the actual code change directly on this branch.

## Task

{{.Title}}

{{.Body}}

## Instructions

- The issue body is your primary source of context. Start there.
- If the issue includes Implementation Details, Conventions, or Context
  from Dependencies sections, follow them closely — the Planner wrote
  them specifically for you.
- If the issue lacks information you need, explore the codebase to fill
  the gaps. But prefer what the issue says over what you infer.
- Check if the acceptance criteria are already satisfied by existing
  code. If so, exit successfully without making any commits, BUT
  before exiting you MUST emit a verdict report in your final output
  in EXACTLY this format (the orchestrator parses it and forwards it
  to the next review cycle):

      Findings reviewed against the current code:

      - **<finding 1 summary>**: <verdict — why no change is needed, with specific file:line references>
      - **<finding 2 summary>**: <verdict>

      Conclusion: <one-line summary, e.g., "All 3 findings describe behavior that already exists.">

  This is non-negotiable. If you determine no code change is needed
  you MUST include this block in your final output. Do not exit the
  no-op path without it. Reference specific file:line locations in
  the verdicts so the next reviewer can verify your reasoning.
- If the task describes a merge or rebase conflict, follow the explicit
  git commands in the task. Do not skip the git merge or rebase step and
  try to manually rewrite files. You must resolve the actual conflict
  markers produced by git.
- Focus on files listed in the Scope or Files to Modify sections. You
  may modify other files if necessary to satisfy acceptance criteria.
- Commit your changes with clear messages referencing issue #{{.IssueNumber}}.
- Do not add features, refactor code, or make improvements beyond
  what is specified in the issue.
- You are running in a fully automated CI environment with no human
  present. Do not pause, ask questions, or wait for confirmation.
  Figure it out. If something is broken, try to fix it. If a tool
  fails, try a different approach. Only exit with a non-zero status
  if the task is genuinely impossible (e.g., the issue references
  code that doesn't exist and can't be inferred).
- If you cannot complete the task after exhausting alternatives,
  exit with a non-zero status and include the reason in your output.
{{if .Retry}}## Validation Failed — Stale Progress

- Pre-push validation FAILED on the code currently in the worktree. The validation errors are in the Task body above and are the ONLY thing you must fix.
- A ` + "`.herd/progress/{{.IssueNumber}}.md`" + ` file may exist from the previous attempt. It is STALE — do NOT honor it, do NOT treat its checked items as proof the work is correct, and do NOT skip work because of it.
- Make the minimal changes needed to make validation pass. Commit and push to ` + "`{{.TargetBranch}}`" + `.
{{else}}## Incremental Progress

- Your branch is ` + "`{{.TargetBranch}}`" + `. After completing work on each file or
  logical unit, run ` + "`git push origin {{.TargetBranch}}`" + ` to save your progress
  remotely. Do not wait until all work is done to push.
- Before your first push, ensure the directory exists (` + "`mkdir -p .herd/progress`" + `) then create a file called .herd/progress/{{.IssueNumber}}.md.
  Update it before each push with a checklist of what you have completed
  and what remains. Format: completed items checked (` + "`- [x]`" + `), remaining items
  unchecked (` + "`- [ ]`" + `).
- The progress file alone no longer causes the worker to skip re-running the
  agent: the worker now also requires that pre-push validation passed last time.
  Keep writing it as before — it is still used to resume a timed-out attempt.
- If .herd/progress/{{.IssueNumber}}.md already exists when you start (from a previous
  timed-out attempt), read it to understand what was already done and continue
  from where it left off. Do not redo completed work.
{{end}}{{if .CoAuthorTrailer}}- Add the following trailer to every commit message (on its own line after a blank line):
  {{.CoAuthorTrailer}}
{{end}}{{if .RoleInstructions}}
## Project-Specific Instructions
{{.RoleInstructions}}
{{end}}`

type standalonePromptData struct {
	Title            string
	Body             string
	IssueNumber      int
	TargetBranch     string
	RoleInstructions string
	CoAuthorTrailer  string
	Retry            bool
}

func renderStandalonePrompt(title, body string, issueNumber int, targetBranch, repoRoot string, cfg *config.Config, retry bool) (string, error) {
	tmpl, err := template.New("worker-standalone").Parse(workerStandalonePromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing standalone prompt template: %w", err)
	}

	data := standalonePromptData{
		Title:        title,
		Body:         body,
		IssueNumber:  issueNumber,
		TargetBranch: targetBranch,
		Retry:        retry,
	}

	if cfg.PullRequests.CoAuthorEmail != "" {
		data.CoAuthorTrailer = fmt.Sprintf("Co-authored-by: herd-os[bot] <%s>", cfg.PullRequests.CoAuthorEmail)
	}

	if ri, readErr := os.ReadFile(filepath.Join(repoRoot, ".herd", "worker.md")); readErr == nil {
		data.RoleInstructions = string(ri)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing standalone prompt template: %w", err)
	}
	return buf.String(), nil
}

// execStandalone runs the standalone worker flow: checks out the PR's head
// branch directly, runs the agent with a standalone prompt, validates the
// result, pushes back to the head branch with a non-force push, and posts
// completion comments on the PR.
func execStandalone(ctx context.Context, p platform.Platform, ag agent.Agent, cfg *config.Config, params ExecParams) (*ExecResult, error) {
	issue, err := p.Issues().Get(ctx, params.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", params.IssueNumber, err)
	}

	parsed, perr := issues.ParseBody(issue.Body)
	if perr != nil || parsed == nil || parsed.FrontMatter.TargetBranch == "" || parsed.FrontMatter.TargetPR == 0 {
		return nil, fmt.Errorf("standalone issue #%d missing target_branch/target_pr in frontmatter", params.IssueNumber)
	}
	targetBranch := parsed.FrontMatter.TargetBranch
	targetPR := parsed.FrontMatter.TargetPR

	g := git.New(params.RepoRoot)
	if err = g.Fetch("origin"); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	if err = g.ConfigureIdentity("HerdOS Worker", "herd@herd-os.com"); err != nil {
		return nil, fmt.Errorf("configuring git identity: %w", err)
	}
	if err = g.Checkout(targetBranch); err != nil {
		return nil, fmt.Errorf("checking out target branch %q: %w", targetBranch, err)
	}

	startSHA, err := g.HeadSHA()
	if err != nil {
		return nil, fmt.Errorf("getting head SHA: %w", err)
	}

	issueBody := issue.Body
	if params.HTTPClient != nil {
		imgDir := filepath.Join(params.RepoRoot, ".herd", "tmp", "images")
		processedBody, imgErr := images.DownloadAndReplace(ctx, params.HTTPClient, issue.Body, imgDir)
		if imgErr != nil {
			fmt.Fprintf(os.Stderr, "warning: image download failed: %v\n", imgErr)
		} else {
			issueBody = processedBody
		}
	}

	systemPrompt, err := renderStandalonePrompt(issue.Title, issueBody, params.IssueNumber, targetBranch, params.RepoRoot, cfg, false)
	if err != nil {
		return nil, fmt.Errorf("rendering standalone prompt: %w", err)
	}

	progressDone := make(chan struct{})
	go postProgressUpdates(ctx, p, params.IssueNumber, params.RepoRoot, cfg.Workers.ProgressIntervalSeconds, progressDone)

	agentTimeout := time.Duration(cfg.Workers.TimeoutMinutes)*time.Minute - 5*time.Minute
	if agentTimeout < 5*time.Minute {
		agentTimeout = 5 * time.Minute
	}
	// Remove any stale validation marker so a previous pass cannot carry over;
	// the marker is re-created only after runValidation reports allPassed().
	_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
	agentCtx, agentCancel := context.WithTimeout(ctx, agentTimeout)
	taskSpec := agent.TaskSpec{IssueNumber: params.IssueNumber, Title: issue.Title, Body: issueBody}
	execOpts := agent.ExecOptions{RepoRoot: params.RepoRoot, SystemPrompt: systemPrompt, MaxTurns: cfg.Agent.MaxTurns}
	agentResult, agentErr := ag.Execute(agentCtx, taskSpec, execOpts)
	agentCancel()
	close(progressDone)

	rawSummary := ""
	if agentErr != nil {
		timedOutWithWork := false
		if agentCtx.Err() == context.DeadlineExceeded {
			fmt.Printf("Agent execution timed out after %s, checking for completed work...\n", agentTimeout)
			diff, diffErr := g.Diff("origin/"+targetBranch, "HEAD")
			if diffErr == nil && diff != "" {
				fmt.Println("Work detected despite timeout — proceeding to push.")
				rawSummary = "Agent timed out but work was completed."
				timedOutWithWork = true
			}
		}
		if !timedOutWithWork {
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("**Worker failed:** agent returned an error.\n\n```\n%s\n```\n\nThis issue will be retried by the monitor.",
					truncateOutput(agentErr.Error(), 2000)))
			return nil, fmt.Errorf("agent execution failed: %w", agentErr)
		}
	}

	if agentResult != nil && agentResult.Summary != "" {
		rawSummary = agentResult.Summary
	}

	diff, diffErr := g.Diff("origin/"+targetBranch, "HEAD")
	if diffErr != nil {
		return nil, fmt.Errorf("checking for changes: %w", diffErr)
	}

	if diff == "" {
		noOpComment := fmt.Sprintf("Worker #%d — no-op: no changes were needed. (Standalone fix completed without modifying any files.)", params.IssueNumber)
		if rawSummary != "" {
			noOpComment += fmt.Sprintf("\n\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
		}
		_ = p.PullRequests().AddComment(ctx, targetPR, noOpComment)

		_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress, issues.StatusFailed})
		_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
		state := "closed"
		_, _ = p.Issues().Update(ctx, params.IssueNumber, platform.IssueUpdate{State: &state})
		return &ExecResult{NoOp: true}, nil
	}

	validation := runValidation(ctx, params.RepoRoot)
	if !validation.allPassed() {
		fmt.Printf("Validation failed, re-invoking agent to fix:\n%s\n", validation.Errors)

		// Persist the failure and clear the marker so the failing code cannot be
		// treated as validated by a subsequent invocation.
		_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
		_ = writeValidationErrors(params.RepoRoot, params.IssueNumber, validation.Errors)
		commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

		// Use the retry variant so the agent does NOT honor the stale progress file.
		retryPrompt, rpErr := renderStandalonePrompt(issue.Title, issueBody, params.IssueNumber, targetBranch, params.RepoRoot, cfg, true)
		if rpErr != nil {
			return nil, fmt.Errorf("rendering standalone prompt for retry: %w", rpErr)
		}
		retryExecOpts := agent.ExecOptions{
			RepoRoot:     params.RepoRoot,
			SystemPrompt: retryPrompt,
			MaxTurns:     cfg.Agent.MaxTurns,
		}
		retrySpec := agent.TaskSpec{
			IssueNumber: params.IssueNumber,
			Title:       "Fix validation failures",
			Body:        fmt.Sprintf("The following validation commands failed after your changes. Please fix all errors:\n\n```\n%s\n```\n\nDo not add new features — only fix the validation errors.", validation.Errors),
		}
		// Remove any stale marker right before the retry invocation too.
		_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
		retryResult, retryErr := ag.Execute(ctx, retrySpec, retryExecOpts)
		if retryErr != nil {
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("**Worker failed:** agent returned an error during validation retry.\n\n```\n%s\n```\n\nThis issue will be retried by the monitor.",
					truncateOutput(retryErr.Error(), 2000)))
			return nil, fmt.Errorf("agent retry after validation failure: %w", retryErr)
		}
		if retryResult != nil && retryResult.Summary != "" {
			rawSummary += "\n\n--- Retry output ---\n" + retryResult.Summary
		}

		validation = runValidation(ctx, params.RepoRoot)
		if !validation.allPassed() {
			_ = removeValidationMarker(params.RepoRoot, params.IssueNumber)
			_ = writeValidationErrors(params.RepoRoot, params.IssueNumber, validation.Errors)
			commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

			report := buildWorkerReport(g, "origin/"+targetBranch, rawSummary, validation)
			report += "\n\n⚠️ Validation failed after retry. Worker marked as failed."
			if rawSummary != "" {
				report += fmt.Sprintf("\n\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
			}
			_ = p.Issues().AddComment(ctx, params.IssueNumber, report)
			return nil, fmt.Errorf("validation failed after retry: %s", validation.Errors)
		}
	}

	// Validation passed — record the marker and clear any saved errors.
	if err = writeValidationMarker(params.RepoRoot, params.IssueNumber); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing validation marker for #%d: %v\n", params.IssueNumber, err)
	}
	_ = removeValidationErrors(params.RepoRoot, params.IssueNumber)
	commitValidationStatus(g, params.RepoRoot, params.IssueNumber, fmt.Sprintf("Update validation status for #%d", params.IssueNumber))

	if err = g.Push("origin", targetBranch); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "non-fast-forward") ||
			strings.Contains(errStr, "rejected") ||
			strings.Contains(errStr, "updates were rejected") {
			_ = p.Issues().AddComment(ctx, params.IssueNumber,
				fmt.Sprintf("⚠️ Could not push to `%s` — the branch has new commits on the remote. Rebase your PR and re-run `/herd fix` to retry.", targetBranch))
		}
		return nil, fmt.Errorf("pushing to target branch: %w", err)
	}

	report := buildWorkerReport(g, "origin/"+targetBranch, rawSummary, validation)
	if rawSummary != "" {
		report += fmt.Sprintf("\n\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
	}
	_ = p.Issues().AddComment(ctx, params.IssueNumber, report)

	var confirmComment string
	if count, cerr := g.RevListCount(startSHA + "..HEAD"); cerr == nil && count > 0 {
		confirmComment = fmt.Sprintf("✓ Standalone fix complete — pushed %d commit(s) to `%s`.", count, targetBranch)
	} else {
		confirmComment = fmt.Sprintf("✓ Standalone fix complete — pushed to `%s`.", targetBranch)
	}
	_ = p.PullRequests().AddComment(ctx, targetPR, confirmComment)

	_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress, issues.StatusFailed})
	_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
	state := "closed"
	_, _ = p.Issues().Update(ctx, params.IssueNumber, platform.IssueUpdate{State: &state})

	return &ExecResult{}, nil
}

// NoOpVerdictMarker is the prefix every worker no-op verdict comment
// posted to the batch PR begins with. The integrator uses this prefix
// to identify worker verdicts when collecting prior-cycle context.
const NoOpVerdictMarker = "**Worker #"

// noOpVerdictHeader returns the exact first line for a worker N verdict.
// Used by both the worker (to format the comment) and the integrator's
// collector (to recognize and parse it).
func noOpVerdictHeader(issueNumber int) string {
	return fmt.Sprintf("**Worker #%d — no-op verdict**", issueNumber)
}

// hasNoOpVerdictBlock reports whether the agent emitted the structured
// verdict block required by the worker prompt when no code change is being
// made. The prompt mandates this exact pair of markers (case-sensitive):
//
//	Findings reviewed against the current code:
//	...
//	Conclusion:
//
// Presence of both markers is treated as positive proof that the agent
// intentionally chose the no-op path after reviewing the codebase, rather
// than crashing/being sandboxed off mid-task. The block must contain the
// "Conclusion:" marker AFTER the "Findings reviewed..." header to count —
// a "Conclusion:" before the findings header is not a valid no-op verdict.
//
// Returns false on empty input (typical for a crashed agent with no
// stdout captured). The check tolerates leading/trailing whitespace but
// the marker text itself is matched literally to keep the prompt contract
// auditable.
func hasNoOpVerdictBlock(summary string) bool {
	const findingsMarker = "Findings reviewed against the current code:"
	const conclusionMarker = "Conclusion:"
	findingsIdx := strings.Index(summary, findingsMarker)
	if findingsIdx < 0 {
		return false
	}
	conclusionIdx := strings.Index(summary[findingsIdx+len(findingsMarker):], conclusionMarker)
	return conclusionIdx >= 0
}

// buildNoOpVerdictComment renders the structured comment posted on the
// batch PR when a worker concludes no code change is needed. The agent
// is instructed (via the worker prompt) to write its findings-by-finding
// reasoning into rawSummary in the exact bullet format described in the
// prompt; this helper wraps that reasoning with the standard header.
//
// If rawSummary is empty (the agent produced no reasoning, e.g. the
// progress file was empty in a true no-op), a degraded fallback body is
// emitted so the comment is still posted and detectable.
func buildNoOpVerdictComment(issueNumber int, rawSummary string) string {
	var b strings.Builder
	b.WriteString(noOpVerdictHeader(issueNumber))
	b.WriteString("\n\n")
	if strings.TrimSpace(rawSummary) == "" {
		b.WriteString("Findings reviewed against the current code:\n\n")
		b.WriteString("- No reasoning was captured by the agent for this no-op run.\n\n")
		b.WriteString("Conclusion: Worker exited without making changes; see issue #")
		b.WriteString(fmt.Sprintf("%d", issueNumber))
		b.WriteString(" for details.\n")
		return b.String()
	}
	b.WriteString(truncateOutput(strings.TrimSpace(rawSummary), 8000))
	b.WriteString("\n")
	return b.String()
}
