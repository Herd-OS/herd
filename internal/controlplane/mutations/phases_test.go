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

func TestIsPreCallRetryableRecord(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		resultRef string
		want      bool
	}{
		{name: "explicit failed pre call status", status: PhaseFailedPreCall, want: true},
		{name: "intent recorded status", status: PhaseIntentRecorded, want: true},
		{name: "legacy failed with failed pre call result ref", status: LegacyFailed, resultRef: PhaseFailedPreCall + ":temporary setup failure", want: true},
		{name: "legacy failed without marker", status: LegacyFailed, resultRef: "github call outcome unknown", want: false},
		{name: "call started with marker is not retryable", status: PhaseCallStarted, resultRef: PhaseFailedPreCall + ":late marker", want: false},
		{name: "completed is not retryable", status: PhaseCompleted, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPreCallRetryableRecord(tt.status, tt.resultRef))
		})
	}
}
