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
}

// ExecResult holds the result of a worker execution.
type ExecResult struct {
	WorkerBranch string
	NoOp         bool // true if no changes were needed
}

const workerPromptTemplate = `You are a HerdOS worker executing a task.

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
  code. If so, report that no changes are needed and exit successfully
  without making any commits.
{{if .IsFixIssue}}- **This is a fix issue created by the reviewer.** Focus on the specific
  problem described in the Task section. The reviewer found an issue that
  may not be covered by the original acceptance criteria. Do not dismiss
  the concern just because existing code passes the original criteria.
  If after careful analysis you genuinely believe the reviewer is wrong,
  explain your reasoning in detail in your output rather than silently
  doing nothing.
{{end}}- Focus on files listed in the Scope or Files to Modify sections. You
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
## Incremental Progress

- Your branch is ` + "`{{.WorkerBranch}}`" + `. After completing work on each file or
  logical unit, run ` + "`git push origin {{.WorkerBranch}}`" + ` to save your progress
  remotely. Do not wait until all work is done to push.
- Before your first push, ensure the directory exists (` + "`mkdir -p .herd/progress`" + `) then create a file called .herd/progress/{{.IssueNumber}}.md.
  Update it before each push with a checklist of what you have completed
  and what remains. Format: completed items checked (` + "`- [x]`" + `), remaining items
  unchecked (` + "`- [ ]`" + `).
- If .herd/progress/{{.IssueNumber}}.md already exists when you start (from a previous
  timed-out attempt), read it to understand what was already done and continue
  from where it left off. Do not redo completed work.
{{if .CoAuthorTrailer}}- Add the following trailer to every commit message (on its own line after a blank line):
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

	// Download GitHub attachment images so the agent can view them locally
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

	// Build system prompt
	systemPrompt, err := renderWorkerPrompt(issue.Title, issueBody, params.IssueNumber, workerBranch, params.RepoRoot, cfg)
	if err != nil {
		return nil, fmt.Errorf("rendering worker prompt: %w", err)
	}

	// Execute task
	taskSpec := agent.TaskSpec{
		IssueNumber: params.IssueNumber,
		Title:       issue.Title,
		Body:        issueBody,
	}
	execOpts := agent.ExecOptions{
		RepoRoot:     params.RepoRoot,
		SystemPrompt: systemPrompt,
		MaxTurns:     cfg.Agent.MaxTurns,
	}

	agentResult, err := ag.Execute(ctx, taskSpec, execOpts)
	if err != nil {
		_ = p.Issues().AddComment(ctx, params.IssueNumber,
			fmt.Sprintf("**Worker failed:** agent returned an error.\n\n```\n%s\n```\n\nThis issue will be retried by the monitor.",
				truncateOutput(err.Error(), 2000)))
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Capture raw agent summary for report
	rawSummary := ""
	if agentResult != nil && agentResult.Summary != "" {
		rawSummary = agentResult.Summary
	}

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

		// No changes — post report and label done without pushing
		noOpReport := "**Worker Report**\n\nNo changes were needed — acceptance criteria already satisfied.\n"
		if rawSummary != "" {
			noOpReport += fmt.Sprintf("\n<details>\n<summary>Agent output</summary>\n\n```\n%s\n```\n\n</details>", truncateOutput(rawSummary, 60000))
		}
		_ = p.Issues().AddComment(ctx, params.IssueNumber, noOpReport)

		// Best-effort: post a comment on the batch PR
		prs, _ := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if len(prs) > 0 {
			prComment := fmt.Sprintf("**Worker #%d (no-op):** No changes needed.", params.IssueNumber)
			if rawSummary != "" {
				prComment = fmt.Sprintf("**Worker #%d (no-op):** No changes needed. %s", params.IssueNumber, truncateOutput(rawSummary, 2000))
			}
			_ = p.PullRequests().AddComment(ctx, prs[0].Number, prComment)
		}

		_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress, issues.StatusFailed})
		_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
		return &ExecResult{NoOp: true}, nil
	}

	// Run pre-push validation
	validation := runValidation(ctx, params.RepoRoot)

	if !validation.allPassed() {
		fmt.Printf("Validation failed, re-invoking agent to fix:\n%s\n", validation.Errors)

		// Re-invoke agent with validation errors
		retrySpec := agent.TaskSpec{
			IssueNumber: params.IssueNumber,
			Title:       "Fix validation failures",
			Body:        fmt.Sprintf("The following validation commands failed after your changes. Please fix all errors:\n\n```\n%s\n```\n\nDo not add new features — only fix the validation errors.", validation.Errors),
		}
		retryResult, retryErr := ag.Execute(ctx, retrySpec, execOpts)
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

func renderWorkerPrompt(title, body string, issueNumber int, workerBranch string, repoRoot string, cfg *config.Config) (string, error) {
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
