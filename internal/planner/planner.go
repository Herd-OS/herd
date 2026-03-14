package planner

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/dag"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// Result contains the output of creating issues from a plan.
type Result struct {
	MilestoneNumber int
	IssueNumbers    []int   // Ordered same as plan tasks
	BatchBranch     string  // e.g., "herd/batch/5-add-jwt-auth"
	Tiers           [][]int // Issue numbers grouped by tier
}

// CreateFromPlan takes an agent plan and creates GitHub issues, a milestone,
// and a batch branch. Returns the created issue numbers and batch branch name.
func CreateFromPlan(ctx context.Context, p platform.Platform, plan *agent.Plan, cfg *config.Config) (*Result, error) {
	// 1. Validate the plan DAG
	d := dag.New()
	for i := range plan.Tasks {
		d.AddNode(i)
	}
	for i, task := range plan.Tasks {
		for _, dep := range task.DependsOn {
			d.AddEdge(i, dep)
		}
	}
	indexTiers, err := d.Tiers()
	if err != nil {
		return nil, fmt.Errorf("invalid plan DAG: %w", err)
	}

	// 2. Create milestone
	ms, err := p.Milestones().Create(ctx, plan.BatchName, "", nil)
	if err != nil {
		return nil, fmt.Errorf("creating milestone: %w", err)
	}

	// 3. Create issues — first pass with placeholder depends_on
	issueNumbers := make([]int, len(plan.Tasks))
	for i, task := range plan.Tasks {
		body := buildIssueBody(task, ms.Number, issueNumbers)
		labels := buildLabels(task, i, indexTiers)
		issue, err := p.Issues().Create(ctx, task.Title, body, labels, &ms.Number)
		if err != nil {
			return nil, fmt.Errorf("creating issue for task %d (%s): %w", i, task.Title, err)
		}
		issueNumbers[i] = issue.Number

		// For manual tasks, notify configured users
		if task.Manual && cfg != nil && len(cfg.Monitor.NotifyUsers) > 0 {
			mentions := buildMentions(cfg.Monitor.NotifyUsers)
			_ = p.Issues().AddComment(ctx, issue.Number, fmt.Sprintf(
				"👋 **Manual task** — this requires human action.\n\n%s", mentions))
		}
	}

	// Second pass — update bodies that have dependencies with real issue numbers
	for i, task := range plan.Tasks {
		if len(task.DependsOn) > 0 {
			body := buildIssueBody(task, ms.Number, issueNumbers)
			_, err := p.Issues().Update(ctx, issueNumbers[i], platform.IssueUpdate{Body: &body})
			if err != nil {
				return nil, fmt.Errorf("updating issue #%d depends_on: %w", issueNumbers[i], err)
			}
		}
	}

	// 4. Create batch branch from default branch
	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting default branch: %w", err)
	}
	sha, err := p.Repository().GetBranchSHA(ctx, defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("getting branch SHA for %s: %w", defaultBranch, err)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, Slugify(plan.BatchName))
	if err := p.Repository().CreateBranch(ctx, batchBranch, sha); err != nil {
		return nil, fmt.Errorf("creating batch branch: %w", err)
	}

	// 5. Convert index tiers to issue number tiers
	issueTiers := make([][]int, len(indexTiers))
	for t, tier := range indexTiers {
		issueTiers[t] = make([]int, len(tier))
		for j, idx := range tier {
			issueTiers[t][j] = issueNumbers[idx]
		}
	}

	return &Result{
		MilestoneNumber: ms.Number,
		IssueNumbers:    issueNumbers,
		BatchBranch:     batchBranch,
		Tiers:           issueTiers,
	}, nil
}

func buildIssueBody(task agent.PlannedTask, batch int, issueNumbers []int) string {
	// Convert depends_on indices to issue numbers
	dependsOn := make([]int, len(task.DependsOn))
	for j, dep := range task.DependsOn {
		dependsOn[j] = issueNumbers[dep]
	}

	body := issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:             1,
			Batch:               batch,
			DependsOn:           dependsOn,
			Scope:               task.Scope,
			EstimatedComplexity: task.Complexity,
			RunnerLabel:         task.RunnerLabel,
		},
		Task:                  task.Description,
		ImplementationDetails: task.ImplementationDetails,
		Conventions:           task.Conventions,
		ContextFromDeps:       task.ContextFromDependencies,
		Criteria:              task.AcceptanceCriteria,
		FilesToModify:         task.Scope,
	}

	return issues.RenderBody(body)
}

func buildLabels(task agent.PlannedTask, index int, tiers [][]int) []string {
	var labels []string

	// Type label
	if task.Manual {
		labels = append(labels, issues.TypeManual)
	} else {
		switch task.Type {
		case "bugfix":
			labels = append(labels, issues.TypeBugfix)
		default:
			labels = append(labels, issues.TypeFeature)
		}
	}

	// Status label based on tier
	tier := tierForIndex(index, tiers)
	if tier == 0 {
		labels = append(labels, issues.StatusReady)
	} else {
		labels = append(labels, issues.StatusBlocked)
	}

	return labels
}

func tierForIndex(index int, tiers [][]int) int {
	for t, tier := range tiers {
		for _, idx := range tier {
			if idx == index {
				return t
			}
		}
	}
	return 0
}

func buildMentions(users []string) string {
	var mentions []string
	for _, u := range users {
		mentions = append(mentions, "@"+u)
	}
	return strings.Join(mentions, " ")
}

// Slugify converts a string to a URL-friendly slug.
func Slugify(s string) string {
	s = strings.ToLower(s)
	var result []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result = append(result, r)
		} else if unicode.IsSpace(r) || r == '_' || r == '-' {
			result = append(result, '-')
		}
	}
	// Collapse multiple dashes
	slug := strings.Join(strings.FieldsFunc(string(result), func(r rune) bool { return r == '-' }), "-")
	// Truncate to reasonable length
	if len(slug) > 50 {
		slug = slug[:50]
		// Don't end on a dash
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}
