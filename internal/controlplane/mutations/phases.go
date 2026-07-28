package mutations

import "strings"

const (
	PhaseIntentRecorded = "intent_recorded"
	PhaseCallStarted    = "call_started"
	PhaseCompleted      = "completed"
	PhaseFailedPreCall  = "failed_pre_call"
	PhaseRepairRequired = "repair_required"

	LegacyStarted = "started"
	LegacyFailed  = "failed"
)

// IsPreCallRetryable reports whether a GitHub-visible mutation can be retried
// without repeating an observable side effect. Mutations must record durable
// intent before the outbound API call; only intent_recorded and failed_pre_call
// may be retried automatically. call_started and repair_required mean the call
// may already be visible on GitHub and must converge through repair or a
// completed mutation result instead of issuing the same side effect again.
func IsPreCallRetryable(status string) bool {
	switch Normalize(status) {
	case PhaseIntentRecorded, PhaseFailedPreCall:
		return true
	default:
		return false
	}
}

func IsPostCallUnknown(status string) bool {
	switch Normalize(status) {
	case PhaseCallStarted, PhaseRepairRequired:
		return true
	default:
		return false
	}
}

func IsCompleted(status string) bool {
	return Normalize(status) == PhaseCompleted
}

func Normalize(status string) string {
	switch strings.TrimSpace(status) {
	case LegacyStarted:
		return PhaseCallStarted
	case LegacyFailed:
		return PhaseRepairRequired
	default:
		return strings.TrimSpace(status)
	}
}
