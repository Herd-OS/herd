package issues

// Status labels (mutually exclusive)
const (
	StatusReady      = "herd/status:ready"
	StatusInProgress = "herd/status:in-progress"
	StatusDone       = "herd/status:done"
	StatusFailed     = "herd/status:failed"
	StatusBlocked    = "herd/status:blocked"
	StatusCancelled  = "herd/status:cancelled"
)

// PR state labels
const (
	// CIFixPending is added to a batch PR when the monitor posts /herd fix-ci.
	// It is removed once CI passes, allowing future failures to re-trigger the command.
	CIFixPending = "herd/ci-fix-pending"

	// RetryPending is added to a failed issue when the monitor posts /herd retry.
	// It is removed by the retry handler, preventing duplicate retry comments
	// when two patrol runs execute before the first retry is processed.
	RetryPending = "herd/retry-pending"

	// RebasePending is added to a batch PR when the monitor detects a merge
	// conflict with the base branch and dispatches a rebase conflict resolution
	// worker. It is removed once the conflict is resolved (PR becomes mergeable
	// again), preventing duplicate dispatches from concurrent patrol runs.
	RebasePending = "herd/rebase-pending"
)

// Type labels
const (
	TypeFeature = "herd/type:feature"
	TypeBugfix  = "herd/type:bugfix"
	TypeFix     = "herd/type:fix"
	TypeManual  = "herd/type:manual"
)

// AllStatusLabels returns all status labels.
func AllStatusLabels() []string {
	return []string{StatusReady, StatusInProgress, StatusDone, StatusFailed, StatusBlocked, StatusCancelled}
}

// AllTypeLabels returns all type labels.
func AllTypeLabels() []string {
	return []string{TypeFeature, TypeBugfix, TypeFix, TypeManual}
}

// AllLabels returns all herd labels.
func AllLabels() []LabelDef {
	return []LabelDef{
		{StatusReady, "0E8A16", "Ready for a worker to pick up"},
		{StatusInProgress, "FBCA04", "A worker is actively executing this task"},
		{StatusDone, "6F42C1", "Worker completed, branch ready for consolidation"},
		{StatusFailed, "D93F0B", "Worker failed — needs re-dispatch or manual fix"},
		{StatusBlocked, "C5DEF5", "Waiting for a dependency to complete"},
		{StatusCancelled, "B60205", "Batch PR closed without merging — task was not completed"},
		{TypeFeature, "1D76DB", "New functionality"},
		{TypeBugfix, "D93F0B", "Bug fix"},
		{TypeFix, "E99695", "Auto-generated fix from agent review or conflict resolution"},
		{TypeManual, "BFD4F2", "Requires human action — not dispatched to workers"},
		{CIFixPending, "E4E669", "CI fix cycle in progress for this batch PR"},
		{RetryPending, "D4C5F9", "Monitor-posted retry pending processing"},
		{RebasePending, "FBCA04", "Rebase conflict resolution in progress for this batch PR"},
	}
}

// LabelDef defines a label with its name, color, and description.
type LabelDef struct {
	Name        string
	Color       string
	Description string
}

// IsStatusLabel returns true if the label is a herd status label.
func IsStatusLabel(label string) bool {
	for _, s := range AllStatusLabels() {
		if s == label {
			return true
		}
	}
	return false
}

// HasLabel returns true if the label list contains the given label.
func HasLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}
	return false
}

// StatusLabel returns the status label from a list of labels, or empty string.
func StatusLabel(labels []string) string {
	for _, l := range labels {
		if IsStatusLabel(l) {
			return l
		}
	}
	return ""
}
