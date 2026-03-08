package platform

import "time"

type Issue struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed"
	Labels    []string
	Milestone *Milestone
	Assignees []string
	URL       string
}

type PullRequest struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed", "merged"
	Head      string // branch name
	Base      string // target branch
	Mergeable bool
	URL       string
}

type Run struct {
	ID         int64
	Status     string            // "queued", "in_progress", "completed"
	Conclusion string            // "success", "failure", "cancelled"
	Inputs     map[string]string // workflow_dispatch inputs
	URL        string
}

type Runner struct {
	ID     int64
	Name   string
	Status string // "online", "offline"
	Labels []string
	Busy   bool
}

type Label struct {
	Name        string
	Color       string
	Description string
}

type Milestone struct {
	Number       int
	Title        string
	Description  string
	State        string // "open", "closed"
	DueDate      *time.Time
	OpenIssues   int
	ClosedIssues int
}

type RepoInfo struct {
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	URL           string
}

type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"
	MergeMethodSquash MergeMethod = "squash"
	MergeMethodRebase MergeMethod = "rebase"
)

type ReviewEvent string

const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
	ReviewComment        ReviewEvent = "COMMENT"
)

type MergeResult struct {
	SHA     string
	Merged  bool
	Message string
}

// Filter and update types

type IssueFilters struct {
	State     string   // "open", "closed", "all"
	Labels    []string
	Milestone *int
}

type IssueUpdate struct {
	Title     *string
	Body      *string
	State     *string // "open", "closed"
	Milestone *int    // 0 to clear
}

type PRFilters struct {
	State string // "open", "closed", "all"
	Head  string
	Base  string
}

type RunFilters struct {
	WorkflowID int64
	Status     string // "queued", "in_progress", "completed"
	Branch     string
}

type MilestoneUpdate struct {
	Title       *string
	Description *string
	State       *string // "open", "closed"
	DueDate     *time.Time
}
