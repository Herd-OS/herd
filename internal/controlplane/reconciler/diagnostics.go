package reconciler

import (
	"encoding/json"
	"time"
)

type Classification string

const (
	ClassificationComplete       Classification = "already_complete"
	ClassificationStillNeeded    Classification = "still_needed"
	ClassificationStaleAbandoned Classification = "stale_abandoned"
	ClassificationFailedSurfaced Classification = "failed_surfaced"
	ClassificationSafeToRetry    Classification = "safe_to_retry"
)

type Diagnostic struct {
	Kind           string         `json:"kind"`
	ID             string         `json:"id"`
	Classification Classification `json:"classification"`
	Action         string         `json:"action,omitempty"`
	Message        string         `json:"message,omitempty"`
	Error          string         `json:"error,omitempty"`
	RecordedAt     time.Time      `json:"recorded_at"`
}

type Report struct {
	StartedAt   time.Time    `json:"started_at"`
	CompletedAt time.Time    `json:"completed_at"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func (r Report) CountsByClassification() map[Classification]int {
	counts := map[Classification]int{}
	for _, diagnostic := range r.Diagnostics {
		counts[diagnostic.Classification]++
	}
	return counts
}

func diagnosticMetadata(d Diagnostic) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"reconciler": map[string]any{
			"kind":           d.Kind,
			"id":             d.ID,
			"classification": d.Classification,
			"action":         d.Action,
			"message":        d.Message,
			"error":          d.Error,
			"recorded_at":    d.RecordedAt,
		},
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
