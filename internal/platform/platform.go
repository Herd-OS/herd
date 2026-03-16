package platform

import (
	"context"
	"time"
)

// Platform abstracts all interactions with the hosting platform (GitHub, GitLab, etc.).
type Platform interface {
	Issues() IssueService
	PullRequests() PullRequestService
	Workflows() WorkflowService
	Labels() LabelService
	Milestones() MilestoneService
	Runners() RunnerService
	Repository() RepositoryService
	Checks() CheckService
}

type IssueService interface {
	Create(ctx context.Context, title, body string, labels []string, milestone *int) (*Issue, error)
	Get(ctx context.Context, number int) (*Issue, error)
	List(ctx context.Context, filters IssueFilters) ([]*Issue, error)
	Update(ctx context.Context, number int, changes IssueUpdate) (*Issue, error)
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabels(ctx context.Context, number int, labels []string) error
	AddComment(ctx context.Context, number int, body string) error
	ListComments(ctx context.Context, number int) ([]*Comment, error)
	CreateReaction(ctx context.Context, commentID int64, reaction string) error
}

type PullRequestService interface {
	Create(ctx context.Context, title, body, head, base string) (*PullRequest, error)
	Get(ctx context.Context, number int) (*PullRequest, error)
	List(ctx context.Context, filters PRFilters) ([]*PullRequest, error)
	Update(ctx context.Context, number int, title, body *string) (*PullRequest, error)
	Merge(ctx context.Context, number int, method MergeMethod) (*MergeResult, error)
	UpdateBranch(ctx context.Context, number int) error
	CreateReview(ctx context.Context, number int, body string, event ReviewEvent) error
	AddComment(ctx context.Context, number int, body string) error
}

type WorkflowService interface {
	GetWorkflow(ctx context.Context, filename string) (workflowID int64, err error)
	Dispatch(ctx context.Context, workflowFile, ref string, inputs map[string]string) (*Run, error)
	GetRun(ctx context.Context, runID int64) (*Run, error)
	ListRuns(ctx context.Context, filters RunFilters) ([]*Run, error)
	CancelRun(ctx context.Context, runID int64) error
}

type LabelService interface {
	Create(ctx context.Context, name, color, description string) error
	List(ctx context.Context) ([]*Label, error)
	Delete(ctx context.Context, name string) error
}

type MilestoneService interface {
	Create(ctx context.Context, title, description string, dueDate *time.Time) (*Milestone, error)
	Get(ctx context.Context, number int) (*Milestone, error)
	List(ctx context.Context) ([]*Milestone, error)
	Update(ctx context.Context, number int, changes MilestoneUpdate) (*Milestone, error)
}

type RunnerService interface {
	List(ctx context.Context) ([]*Runner, error)
	Get(ctx context.Context, id int64) (*Runner, error)
}

type CheckService interface {
	// GetCombinedStatus returns the combined CI status for a ref (branch or SHA).
	// Returns "success", "failure", "pending", or "error".
	GetCombinedStatus(ctx context.Context, ref string) (string, error)

	// RerunFailedChecks re-runs failed check suites for a ref.
	RerunFailedChecks(ctx context.Context, ref string) error
}

type RepositoryService interface {
	GetInfo(ctx context.Context) (*RepoInfo, error)
	GetDefaultBranch(ctx context.Context) (string, error)
	CreateBranch(ctx context.Context, name, fromSHA string) error
	DeleteBranch(ctx context.Context, name string) error
	GetBranchSHA(ctx context.Context, name string) (string, error)
}
