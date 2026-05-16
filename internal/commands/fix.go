package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

func handleFix(hctx *HandlerContext, cmd Command) Result {
	if cmd.Prompt == "" {
		return Result{Message: "⚠️ Usage: `/herd fix \"description of what to fix\"`"}
	}
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd fix` can only be used on pull requests."}
	}

	pr, err := hctx.Platform.PullRequests().Get(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return handleStandaloneFix(hctx, cmd, pr)
	}

	batchNum, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return Result{Error: fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)}
	}

	ms, err := hctx.Platform.Milestones().Get(hctx.Ctx, batchNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting milestone #%d: %w", batchNum, err)}
	}

	allIssues, err := hctx.Platform.Issues().List(hctx.Ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return Result{Error: fmt.Errorf("listing milestone issues: %w", err)}
	}
	currentCycle := 0
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.FixCycle > currentCycle {
			currentCycle = parsed.FrontMatter.FixCycle
		}
	}
	nextCycle := currentCycle + 1

	// Fetch PR comment history for context
	comments, err := hctx.Platform.Issues().ListComments(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("listing PR #%d comments: %w", hctx.IssueNumber, err)}
	}

	var history string
	if len(comments) > 0 {
		history = formatCommentHistory(comments)
	}

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    ms.Number,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  pr.Number,
		},
		Task:                cmd.Prompt,
		Context:             fmt.Sprintf("Requested by @%s via `/herd fix` on batch PR #%d.", hctx.AuthorLogin, pr.Number),
		ConversationHistory: history,
	})

	// Detect conflict-related keywords and append explicit git instructions
	if looksLikeConflict(cmd.Prompt) {
		body = appendConflictInstructions(body, pr.Base)
	}

	truncated := truncateRunes(cmd.Prompt, 60)
	truncatedBody, overflow := issues.TruncateIssueBody(body)
	fixIssue, err := hctx.Platform.Issues().Create(hctx.Ctx,
		"Fix: "+truncated,
		truncatedBody,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return Result{Error: fmt.Errorf("creating fix issue: %w", err)}
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := hctx.Platform.Issues().AddComment(hctx.Ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on fix issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(hctx.Ctx)
	if err != nil {
		return Result{Error: fmt.Errorf("getting default branch: %w", err)}
	}
	if _, err := hctx.Platform.Workflows().Dispatch(hctx.Ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	}); err != nil {
		return Result{Error: fmt.Errorf("dispatching worker for fix issue #%d: %w", fixIssue.Number, err)}
	}

	return Result{Message: fmt.Sprintf("🔧 Created fix issue #%d and dispatched worker.", fixIssue.Number)}
}

// formatCommentHistory formats PR comments into a markdown conversation log.
func formatCommentHistory(comments []*platform.Comment) string {
	var b strings.Builder
	for i, c := range comments {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(fmt.Sprintf("**@%s:**\n\n%s\n", c.AuthorLogin, c.Body))
	}
	return b.String()
}

// looksLikeConflict returns true if the description contains conflict-related keywords.
func looksLikeConflict(description string) bool {
	lower := strings.ToLower(description)
	keywords := []string{"merge conflict", "rebase conflict", "conflict with main", "conflict with master", "conflicts with main", "conflicts with master"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// appendConflictInstructions appends explicit git instructions to a fix issue body.
func appendConflictInstructions(body, baseBranch string) string {
	instructions := fmt.Sprintf("\n\n## Git Instructions\n\n"+
		"This task involves a merge or rebase conflict. Follow these steps:\n\n"+
		"**For merge conflicts:**\n"+
		"1. `git fetch origin`\n"+
		"2. `git merge origin/%s`\n"+
		"3. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch.\n"+
		"4. `git add <resolved files>`\n"+
		"5. `git commit`\n\n"+
		"**For rebase conflicts:**\n"+
		"1. `git fetch origin`\n"+
		"2. `git rebase origin/%s`\n"+
		"3. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch.\n"+
		"4. `git add <resolved files>`\n"+
		"5. `git rebase --continue`\n"+
		"6. Repeat steps 3-5 for each conflicting commit.\n",
		baseBranch, baseBranch)
	return body + instructions
}

// truncateRunes truncates s to at most n runes, appending "..." if truncated.
// This is safe for multi-byte UTF-8 strings.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// firstLine returns the substring of s up to the first newline, or all of s if none.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// handleStandaloneFix handles /herd fix on a non-batch PR by creating a
// tracking issue (no milestone) with target_pr / target_branch frontmatter
// and dispatching a worker with mode=standalone.
func handleStandaloneFix(hctx *HandlerContext, cmd Command, pr *platform.PullRequest) Result {
	existing, err := hctx.Platform.Issues().List(hctx.Ctx, platform.IssueFilters{
		State:  "open",
		Labels: []string{issues.TypeStandaloneFix},
	})
	if err != nil {
		return Result{Error: fmt.Errorf("listing standalone fix issues: %w", err)}
	}
	for _, iss := range existing {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.TargetPR != pr.Number {
			continue
		}
		if issues.HasLabel(iss.Labels, issues.StatusInProgress) || issues.HasLabel(iss.Labels, issues.StatusReady) {
			return Result{Message: fmt.Sprintf("⚠️ A standalone fix is already in progress for this PR (#%d). Wait for it to complete before posting another `/herd fix`.", iss.Number)}
		}
	}

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:      1,
			Type:         "standalone-fix",
			TargetPR:     pr.Number,
			TargetBranch: pr.Head,
		},
		Task:    cmd.Prompt,
		Context: fmt.Sprintf("Requested by @%s via `/herd fix` on PR #%d.", hctx.AuthorLogin, pr.Number),
	})

	truncatedBody, overflow := issues.TruncateIssueBody(body)

	title := "Standalone fix: " + truncateRunes(firstLine(cmd.Prompt), 70)
	fixIssue, err := hctx.Platform.Issues().Create(hctx.Ctx,
		title,
		truncatedBody,
		[]string{issues.TypeStandaloneFix, issues.StatusInProgress},
		nil,
	)
	if err != nil {
		return Result{Error: fmt.Errorf("creating standalone fix issue: %w", err)}
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := hctx.Platform.Issues().AddComment(hctx.Ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on fix issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(hctx.Ctx)
	if err != nil {
		return Result{Error: fmt.Errorf("getting default branch: %w", err)}
	}
	if _, err := hctx.Platform.Workflows().Dispatch(hctx.Ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"mode":            "standalone",
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	}); err != nil {
		return Result{Error: fmt.Errorf("dispatching worker for standalone fix issue #%d: %w", fixIssue.Number, err)}
	}

	return Result{Message: fmt.Sprintf("🔧 Created standalone fix issue #%d and dispatched worker. The worker will push directly to this PR's branch when done.", fixIssue.Number)}
}
