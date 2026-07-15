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

func mergeDiagnosticMetadata(existing json.RawMessage, d Diagnostic) json.RawMessage {
	var body map[string]any
	if len(existing) == 0 || json.Unmarshal(existing, &body) != nil {
		body = map[string]any{}
	}
	var diagnostic map[string]any
	if err := json.Unmarshal(diagnosticMetadata(d), &diagnostic); err != nil {
		return diagnosticMetadata(d)
	}
	for key, value := range diagnostic {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return diagnosticMetadata(d)
	}
	return raw
}
