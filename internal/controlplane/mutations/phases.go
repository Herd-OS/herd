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
