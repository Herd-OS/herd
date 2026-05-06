package dashboard

import "time"

// State is the snapshot the TUI renders on each tick. All fields are populated
// by Fetch; on partial failure, the previous State is reused and FetchError is
// set on a copy by the caller.
type State struct {
	Owner       string
	Repo        string
	Workers     []WorkerEntry
	Batches     []BatchEntry
	Failures    []FailureEntry
	LastRefresh time.Time
	FetchError  string // last non-fatal error, displayed in status area
}

// WorkerEntry is one in-progress run of herd-worker.yml.
type WorkerEntry struct {
	RunID       int64
	IssueNumber int       // parsed from run inputs ("issue") if present, else 0
	IssueTitle  string    // populated by Fetch via Issues().Get when IssueNumber>0; truncated by view
	URL         string    // workflow run URL
	StartedAt   time.Time
}

// BatchEntry is one open milestone with at least one herd/* labelled issue.
//
// ReviewState is reserved for a future task: the platform package does not
// currently expose a list-reviews API, so Fetch leaves it empty in v1.
type BatchEntry struct {
	MilestoneNumber int
	MilestoneTitle  string
	MilestoneURL    string
	PRNumber        int    // 0 if no PR yet
	PRURL           string
	CIStatus        string // "", "success", "failure", "pending", "error"
	ReviewState     string // "", "approved", "changes_requested", "commented"
	Tier            int    // current tier (1-indexed for display)
	TotalTiers      int
	Done            int
	InProgress      int
	Ready           int
	Failed          int
	Blocked         int
	LatestActivity  time.Time // max issue.UpdatedAt across milestone
	HasAttention    bool      // any failed issue || CIStatus==failure || ReviewState==changes_requested
}

// FailureEntry is one issue with herd/status:failed updated in the last 24h.
type FailureEntry struct {
	Number    int
	Title     string
	Label     string // primary type label, e.g. "herd/type:feature"
	UpdatedAt time.Time
}
