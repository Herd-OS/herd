package integrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

type ConflictResolutionKind string

const (
	ConflictResolutionKindWorkerMerge ConflictResolutionKind = "worker-merge"
	ConflictResolutionKindBatchRebase ConflictResolutionKind = "batch-rebase"
	ConflictResolutionKindPRBase      ConflictResolutionKind = "pr-base"
)

type ConflictResolutionIssueParams struct {
	Kind              ConflictResolutionKind
	Milestone         *platform.Milestone
	Title             string
	BatchPR           int
	SourceIssueNumber int
	SourceIssueTitle  string
	WorkerBranch      string
	BatchBranch       string
	PRHeadBranch      string
	PRHeadSHA         string
	BaseBranch        string
	BaseSHA           string
	TriggerAuthor     string
	TriggerComment    string
	UserContext       string
}

type ConflictResolutionDispatchResult struct {
	IssueNumber int
	Body        string
	Duplicated  bool
}

type conflictResolutionDispatchError struct {
	issueNumber int
	message     string
	err         error
}

func (e *conflictResolutionDispatchError) Error() string {
	return fmt.Sprintf("%s: %v", e.message, e.err)
}

func (e *conflictResolutionDispatchError) Unwrap() error {
	return e.err
}

func BuildConflictResolutionIssueBody(params ConflictResolutionIssueParams) string {
	fm := issues.FrontMatter{
		Version:             1,
		Type:                "fix",
		ConflictResolution:  true,
		ConflictingBranches: conflictResolutionBranches(params),
	}
	if params.Milestone != nil {
		fm.Batch = params.Milestone.Number
	}
	if params.Kind == ConflictResolutionKindPRBase {
		fm.BatchPR = params.BatchPR
		fm.PRHeadSHA = params.PRHeadSHA
		fm.PRBaseSHA = params.BaseSHA
	}

	body := issues.IssueBody{
		FrontMatter: fm,
		Task:        conflictResolutionTask(params),
		Context:     conflictResolutionContext(params),
	}

	return issues.RenderBody(body)
}

func DispatchConflictResolutionIssue(ctx context.Context, p platform.Platform, cfg *config.Config, params ConflictResolutionIssueParams, labels []string, dispatchBatchBranch string) (*ConflictResolutionDispatchResult, error) {
	if params.Kind == ConflictResolutionKindPRBase && params.Milestone != nil {
		existing, err := FindActivePRConflictResolutionIssue(ctx, p, params.Milestone.Number, params.BatchPR, params.PRHeadSHA, params.BaseSHA)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return &ConflictResolutionDispatchResult{
				IssueNumber: existing.Number,
				Body:        existing.Body,
				Duplicated:  true,
			}, nil
		}
	}

	body := BuildConflictResolutionIssueBody(params)
	truncatedBody, overflow := issues.TruncateIssueBody(body)
	milestoneNumber := conflictResolutionMilestoneNumber(params.Milestone)
	fixIssue, err := p.Issues().Create(ctx, conflictResolutionTitle(params), truncatedBody, labels, milestoneNumber)
	if err != nil {
		return nil, fmt.Errorf("creating conflict-resolution issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on conflict-resolution issue #%d: %v\n", fixIssue.Number, cerr)
		}
	}

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		markConflictResolutionDispatchFailed(ctx, p, fixIssue.Number, err)
		return nil, &conflictResolutionDispatchError{
			issueNumber: fixIssue.Number,
			message:     "getting default branch for conflict-resolution dispatch",
			err:         err,
		}
	}
	if _, err := p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    dispatchBatchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	}); err != nil {
		markConflictResolutionDispatchFailed(ctx, p, fixIssue.Number, err)
		return nil, &conflictResolutionDispatchError{
			issueNumber: fixIssue.Number,
			message:     fmt.Sprintf("dispatching conflict-resolution worker for issue #%d", fixIssue.Number),
			err:         err,
		}
	}

	return &ConflictResolutionDispatchResult{
		IssueNumber: fixIssue.Number,
		Body:        truncatedBody,
	}, nil
}

func markConflictResolutionDispatchFailed(ctx context.Context, p platform.Platform, issueNumber int, dispatchErr error) {
	_ = p.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusInProgress, issues.StatusReady})
	_ = p.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
	_ = p.Issues().AddComment(ctx, issueNumber, fmt.Sprintf("Failed to dispatch conflict-resolution worker: %v", dispatchErr))
}

func FindActivePRConflictResolutionIssue(ctx context.Context, p platform.Platform, milestoneNumber int, prNumber int, headSHA string, baseSHA string) (*platform.Issue, error) {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &milestoneNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("listing PR conflict-resolution issues: %w", err)
	}

	for _, iss := range allIssues {
		if !isActiveConflictResolutionIssue(iss) {
			continue
		}
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		fm := parsed.FrontMatter
		if !fm.ConflictResolution || fm.BatchPR != prNumber {
			continue
		}
		if !prConflictResolutionSHAMatches(fm.PRHeadSHA, headSHA, iss.Body) {
			continue
		}
		if !prConflictResolutionSHAMatches(fm.PRBaseSHA, baseSHA, iss.Body) {
			continue
		}
		return iss, nil
	}

	return nil, nil
}

func prConflictResolutionSHAMatches(frontMatterSHA string, requestedSHA string, body string) bool {
	if requestedSHA == "" {
		return true
	}
	if frontMatterSHA != "" {
		return frontMatterSHA == requestedSHA
	}
	return strings.Contains(body, requestedSHA)
}

func isActiveConflictResolutionIssue(iss *platform.Issue) bool {
	if iss == nil {
		return false
	}
	return issues.HasLabel(iss.Labels, issues.StatusInProgress) || issues.HasLabel(iss.Labels, issues.StatusReady)
}

func conflictResolutionBranches(params ConflictResolutionIssueParams) []string {
	switch params.Kind {
	case ConflictResolutionKindWorkerMerge:
		return []string{params.WorkerBranch, params.BatchBranch}
	case ConflictResolutionKindBatchRebase:
		return []string{params.BatchBranch, params.BaseBranch}
	case ConflictResolutionKindPRBase:
		return []string{params.PRHeadBranch, params.BaseBranch}
	default:
		return nil
	}
}

func conflictResolutionTask(params ConflictResolutionIssueParams) string {
	switch params.Kind {
	case ConflictResolutionKindWorkerMerge:
		return fmt.Sprintf("Resolve merge conflict between `%s` and `%s`.\n\n"+
			"**IMPORTANT:** You are already on your own worker branch (`herd/worker/<this-issue>-<slug>`). Do NOT checkout `%s` or any other branch — your commits must land on your worker branch so the worker framework can push them. The integrator will then merge your worker branch into `%s`.\n\n"+
			"Follow these steps exactly:\n"+
			"1. `git fetch origin`\n"+
			"2. Stay on your current worker branch — do NOT run `git checkout %s`.\n"+
			"3. `git merge origin/%s`\n"+
			"4. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch — only fix the conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) produced by git.\n"+
			"5. `git add <resolved files>`\n"+
			"6. `git commit` (accept the default merge commit message).\n"+
			"7. Do NOT push — the worker framework handles pushing your worker branch.",
			params.WorkerBranch, params.BatchBranch, params.BatchBranch, params.BatchBranch, params.BatchBranch, params.WorkerBranch)
	case ConflictResolutionKindBatchRebase:
		return fmt.Sprintf("Resolve the conflict between batch branch `%s` and the latest `%s`.\n\n"+
			"**IMPORTANT:** You are already on your own worker branch (`herd/worker/<this-issue>-<slug>`). Do NOT checkout `%s` or `%s` — your commits must land on your worker branch so the worker framework can push them. The integrator will then merge your worker branch into `%s`.\n\n"+
			"Follow these steps exactly:\n"+
			"1. `git fetch origin`\n"+
			"2. Stay on your current worker branch — do NOT run `git checkout %s` or `git checkout %s`.\n"+
			"3. `git merge origin/%s` (this brings the latest default-branch commits into your worker branch).\n"+
			"4. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch — only fix the conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) produced by git.\n"+
			"5. `git add <resolved files>`\n"+
			"6. `git commit` (accept the default merge commit message).\n"+
			"7. Do NOT push — the worker framework handles pushing your worker branch.",
			params.BatchBranch, params.BaseBranch, params.BatchBranch, params.BaseBranch, params.BatchBranch, params.BatchBranch, params.BaseBranch, params.BaseBranch)
	case ConflictResolutionKindPRBase:
		return fmt.Sprintf("Follow these steps exactly:\n"+
			"1. `git fetch origin`\n"+
			"2. Stay on your current worker branch. Do not checkout `%s` or `%s`.\n"+
			"3. Run either `git merge origin/%s` or `git rebase origin/%s` on your current worker branch.\n"+
			"4. Resolve conflict markers in the affected files. Do not rewrite files from scratch; only fix the conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) produced by git.\n"+
			"5. `git add <resolved files>`\n"+
			"6. `git commit` (or continue the rebase, if you chose rebase).\n"+
			"7. Do not push. The worker framework will push your worker branch.\n\n"+
			"Do not search for conflict markers before attempting the merge or rebase; first produce the conflict state with git. Do not review stale historical findings unless the user explicitly requested an additional code fix.",
			params.PRHeadBranch, params.BaseBranch, params.BaseBranch, params.BaseBranch)
	default:
		return ""
	}
}

func conflictResolutionContext(params ConflictResolutionIssueParams) string {
	switch params.Kind {
	case ConflictResolutionKindWorkerMerge:
		return fmt.Sprintf("Worker branch `%s` (from issue #%d) conflicts with the batch branch `%s`.", params.WorkerBranch, params.SourceIssueNumber, params.BatchBranch)
	case ConflictResolutionKindBatchRebase:
		return fmt.Sprintf("Automatic rebase of batch branch `%s` onto `%s` failed due to conflicts.", params.BatchBranch, params.BaseBranch)
	case ConflictResolutionKindPRBase:
		parts := []string{
			fmt.Sprintf("PR #%d cannot be merged cleanly into `%s`.", params.BatchPR, params.BaseBranch),
			fmt.Sprintf("Head branch: `%s`", params.PRHeadBranch),
			fmt.Sprintf("Head SHA: `%s`", params.PRHeadSHA),
			fmt.Sprintf("Base branch: `%s`", params.BaseBranch),
			fmt.Sprintf("Base SHA: `%s`", params.BaseSHA),
		}
		if params.TriggerAuthor != "" {
			parts = append(parts, fmt.Sprintf("Triggering author: `%s`", params.TriggerAuthor))
		}
		if params.TriggerComment != "" {
			parts = append(parts, "Triggering comment:\n\n"+params.TriggerComment)
		}
		if params.UserContext != "" {
			parts = append(parts, "User context:\n\n"+params.UserContext)
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func conflictResolutionTitle(params ConflictResolutionIssueParams) string {
	if params.Title != "" {
		return params.Title
	}
	switch params.Kind {
	case ConflictResolutionKindWorkerMerge:
		return fmt.Sprintf("Resolve conflict: #%d (%s)", params.SourceIssueNumber, truncate(params.SourceIssueTitle, 40))
	case ConflictResolutionKindBatchRebase:
		return fmt.Sprintf("Resolve rebase conflict: %s onto %s", params.BatchBranch, params.BaseBranch)
	case ConflictResolutionKindPRBase:
		return fmt.Sprintf("Resolve PR conflict: #%d (%s onto %s)", params.BatchPR, params.PRHeadBranch, params.BaseBranch)
	default:
		return "Resolve conflict"
	}
}

func conflictResolutionMilestoneNumber(ms *platform.Milestone) *int {
	if ms == nil {
		return nil
	}
	return &ms.Number
}
