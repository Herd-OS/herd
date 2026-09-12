package reconciler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportCountsByClassification(t *testing.T) {
	report := Report{Diagnostics: []Diagnostic{
		{Classification: ClassificationComplete},
		{Classification: ClassificationSafeToRetry},
		{Classification: ClassificationSafeToRetry},
	}}

	counts := report.CountsByClassification()

	assert.Equal(t, 1, counts[ClassificationComplete])
	assert.Equal(t, 2, counts[ClassificationSafeToRetry])
	assert.Zero(t, counts[ClassificationFailedSurfaced])
}

func TestDiagnosticMetadata(t *testing.T) {
	d := Diagnostic{
		Kind:           "job",
		ID:             "job-1",
		Classification: ClassificationFailedSurfaced,
		Action:         "mark_failed",
		Message:        "timed out",
		RecordedAt:     time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC),
	}

	raw := diagnosticMetadata(d)

	require.JSONEq(t, `{"reconciler":{"kind":"job","id":"job-1","classification":"failed_surfaced","action":"mark_failed","message":"timed out","error":"","recorded_at":"2026-07-11T01:02:03Z"}}`, string(raw))
}
