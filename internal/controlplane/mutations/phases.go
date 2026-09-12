package mutations

import "strings"

const (
	PhaseIntentRecorded  = "intent_recorded"
	PhaseCallStarted     = "call_started"
	PhasePostCallUnknown = "post_call_unknown"
	PhaseCompleted       = "completed"
	PhaseFailedPreCall   = "failed_pre_call"
	PhaseRepairRequired  = "repair_required"

	LegacyStarted              = "started"
	LegacyFailed               = "failed"
	LegacyWebhookStarted       = "processor_started"
	LegacyWebhookCompleted     = "processed"
	LegacyWebhookFailedPreCall = "failed_pre_processor"
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

// IsPreCallRetryableRecord reports whether an idempotency record can safely
// rerun a GitHub-visible mutation. Some stores persist failed_pre_call as the
// durable status, while legacy stores keep a generic failed status and store
// the phase marker in the result/error ref. Generic failed records are treated
// as post-call-unknown and must not be retried automatically.
func IsPreCallRetryableRecord(status, resultRef string) bool {
	if IsPreCallRetryable(status) {
		return true
	}
	return Normalize(status) == PhaseRepairRequired && strings.HasPrefix(strings.TrimSpace(resultRef), PhaseFailedPreCall+":")
}

func IsPostCallUnknown(status string) bool {
	switch Normalize(status) {
	case PhaseCallStarted, PhasePostCallUnknown, PhaseRepairRequired:
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
	case LegacyStarted, LegacyWebhookStarted:
		return PhaseCallStarted
	case LegacyWebhookCompleted:
		return PhaseCompleted
	case LegacyWebhookFailedPreCall:
		return PhaseFailedPreCall
	case LegacyFailed:
		return PhaseRepairRequired
	default:
		return strings.TrimSpace(status)
	}
}
