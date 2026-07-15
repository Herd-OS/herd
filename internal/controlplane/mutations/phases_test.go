package mutations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPhaseClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		preRetry    bool
		postUnknown bool
		completed   bool
	}{
		{name: "intent recorded", status: PhaseIntentRecorded, preRetry: true},
		{name: "failed before call", status: PhaseFailedPreCall, preRetry: true},
		{name: "call started", status: PhaseCallStarted, postUnknown: true},
		{name: "repair required", status: PhaseRepairRequired, postUnknown: true},
		{name: "completed", status: PhaseCompleted, completed: true},
		{name: "legacy started is post call unknown", status: LegacyStarted, postUnknown: true},
		{name: "legacy failed is repair required", status: LegacyFailed, postUnknown: true},
		{name: "empty", status: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.preRetry, IsPreCallRetryable(tt.status))
			assert.Equal(t, tt.postUnknown, IsPostCallUnknown(tt.status))
			assert.Equal(t, tt.completed, IsCompleted(tt.status))
		})
	}
}
