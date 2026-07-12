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
	UpdatedAt time.Time
}

type PullRequest struct {
	Number         int
	Title          string
	Body           string
	State          string // "open", "closed", "merged"
	Head           string // branch name
	HeadSHA        string
	Base           string // target branch
	Labels         []string
	Mergeable      bool
	MergeableKnown bool // false when GitHub is still computing mergeability
	URL            string
	CreatedAt      time.Time
}

type PullRequestFile struct {
	Path         string
	PreviousPath string
	Status       string
	Additions    int
	Deletions    int
	Changes      int
	Patch        string
	SHA          string
	BlobURL      string
	RawURL       string
	ContentsURL  string
}

type Run struct {
	ID           int64
	WorkflowID   int64
	WorkflowName string
	WorkflowPath string
	HeadBranch   string
	HeadSHA      string
	Status       string            // "queued", "in_progress", "completed"
	Conclusion   string            // "success", "failure", "cancelled"
	Inputs       map[string]string // workflow_dispatch inputs
	URL          string
	CreatedAt    time.Time
}

type WorkflowRunDiagnostics struct {
	RunID       int64
	Workflow    string
	URL         string
	Conclusion  string
	HeadBranch  string
	HeadSHA     string
	Jobs        []WorkflowJobDiagnostic
	Annotations []string
	LogExcerpt  string
	LogStatus   string
}

type WorkflowJobDiagnostic struct {
	ID         int64
	Name       string
	URL        string
	Conclusion string
	Status     string
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

type Comment struct {
	ID                int64
	Body              string
	AuthorLogin       string
	AuthorAssociation string // "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "NONE", etc.
}

// ReviewComment is an inline (line-level) comment on a pull request diff.
type ReviewComment struct {
	ID                int64
	Body              string
	AuthorLogin       string
	AuthorAssociation string // "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "NONE", etc.
	Path              string // file path the comment is on
	Line              int    // line number in the diff (0 if not anchored)
	DiffHunk          string // short diff hunk surrounding the comment
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
	ReviewCommentEvent   ReviewEvent = "COMMENT"
)

type MergeResult struct {
	SHA     string
	Merged  bool
	Message string
}

type CommitStatus struct {
	State       string
	Context     string
	Description string
	TargetURL   string
}

// Filter and update types

type IssueFilters struct {
	State     string // "open", "closed", "all"
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
	WorkflowID              int64
	WorkflowFileName        string // e.g. "herd-worker.yml" — used to filter by workflow file
	Status                  string // "queued", "in_progress", "completed"
	Branch                  string
	ResolveWorkflowIdentity bool // resolve workflow_id to the canonical workflow name/path
}

type MilestoneUpdate struct {
	Title       *string
	Description *string
	State       *string // "open", "closed"
	DueDate     *time.Time
}
