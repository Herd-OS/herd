package dashboard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDashboardSort_AttentionFirst(t *testing.T) {
	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	batches := []BatchEntry{
		{MilestoneNumber: 1, HasAttention: false, LatestActivity: newer},
		{MilestoneNumber: 2, HasAttention: true, LatestActivity: old},
	}
	SortBatches(batches)
	assert.Equal(t, 2, batches[0].MilestoneNumber, "attention batch should sort first")
	assert.Equal(t, 1, batches[1].MilestoneNumber)
}

func TestDashboardSort_ActivityTiebreak(t *testing.T) {
	older := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	batches := []BatchEntry{
		{MilestoneNumber: 1, HasAttention: false, LatestActivity: older},
		{MilestoneNumber: 2, HasAttention: false, LatestActivity: newer},
	}
	SortBatches(batches)
	assert.Equal(t, 2, batches[0].MilestoneNumber, "newer activity should sort first")
	assert.Equal(t, 1, batches[1].MilestoneNumber)
}

func TestDashboardSort_MilestoneTiebreak(t *testing.T) {
	same := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	batches := []BatchEntry{
		{MilestoneNumber: 5, HasAttention: false, LatestActivity: same},
		{MilestoneNumber: 2, HasAttention: false, LatestActivity: same},
		{MilestoneNumber: 9, HasAttention: false, LatestActivity: same},
	}
	SortBatches(batches)
	assert.Equal(t, 2, batches[0].MilestoneNumber)
	assert.Equal(t, 5, batches[1].MilestoneNumber)
	assert.Equal(t, 9, batches[2].MilestoneNumber)
}

func TestDashboardSort_AttentionInternalOrdering(t *testing.T) {
	older := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	batches := []BatchEntry{
		{MilestoneNumber: 1, HasAttention: true, LatestActivity: older},
		{MilestoneNumber: 2, HasAttention: false, LatestActivity: newer},
		{MilestoneNumber: 3, HasAttention: true, LatestActivity: newer},
	}
	SortBatches(batches)
	// Both attention batches first; newer-activity attention batch wins.
	assert.Equal(t, 3, batches[0].MilestoneNumber)
	assert.Equal(t, 1, batches[1].MilestoneNumber)
	assert.Equal(t, 2, batches[2].MilestoneNumber)
}
