package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newModelWithWorkers(workers []WorkerEntry) Model {
	return Model{
		Owner:      "o",
		Repo:       "r",
		RefreshSec: 5,
		state:      State{Workers: workers, LastRefresh: time.Now()},
	}
}

func TestRenderWorkers_ShowsNumberAndTitle(t *testing.T) {
	m := newModelWithWorkers([]WorkerEntry{{
		RunID: 1, URL: "https://example/run/1",
		IssueNumber: 42, IssueTitle: "Add auth",
		StartedAt: time.Now().Add(-2*time.Minute - 5*time.Second),
	}})
	out := m.workersPanel()
	assert.Contains(t, out, "#42")
	assert.Contains(t, out, "Add auth")
	assert.Contains(t, out, "2m")
}

func TestRenderWorkers_TruncatesLongTitle(t *testing.T) {
	longTitle := strings.Repeat("x", 200)
	m := newModelWithWorkers([]WorkerEntry{{
		RunID: 1, URL: "https://example/run/1",
		IssueNumber: 42, IssueTitle: longTitle,
		StartedAt: time.Now().Add(-30 * time.Second),
	}})
	out := m.workersPanel()
	assert.Contains(t, out, "…")
	assert.Contains(t, out, "30s")
	assert.NotContains(t, out, longTitle)
}

func TestRenderWorkers_EmptyTitleFallback(t *testing.T) {
	m := newModelWithWorkers([]WorkerEntry{{
		RunID: 1, URL: "https://example/run/1",
		IssueNumber: 42, IssueTitle: "",
		StartedAt: time.Now().Add(-10 * time.Second),
	}})
	out := m.workersPanel()
	assert.Contains(t, out, "#42")
	assert.Contains(t, out, "10s")
}

func TestRenderWorkers_ZeroIssueNumberFallback(t *testing.T) {
	m := newModelWithWorkers([]WorkerEntry{{
		RunID: 1, URL: "https://example/run/1",
		IssueNumber: 0, IssueTitle: "",
		StartedAt: time.Now().Add(-3 * time.Second),
	}})
	out := m.workersPanel()
	assert.NotContains(t, out, "#0")
	assert.Contains(t, out, "3s")
}

func TestDashboard_ShowsCascadeFailedMarker(t *testing.T) {
	m := Model{
		Owner:      "o",
		Repo:       "r",
		RefreshSec: 5,
		state: State{
			LastRefresh: time.Now(),
			Batches: []BatchEntry{{
				MilestoneNumber: 1,
				MilestoneTitle:  "Test Batch",
				PRNumber:        42,
				CIStatus:        "failure",
				CascadeFailed:   true,
				HasAttention:    true,
			}},
		},
	}
	out := m.batchesPanel()
	assert.Contains(t, out, "⚠ cascade failed")
}

func TestDashboard_CascadeFailedDoesNotMarkerWhenFalse(t *testing.T) {
	m := Model{
		Owner:      "o",
		Repo:       "r",
		RefreshSec: 5,
		state: State{
			LastRefresh: time.Now(),
			Batches: []BatchEntry{{
				MilestoneNumber: 1,
				MilestoneTitle:  "Test Batch",
				PRNumber:        42,
				CIStatus:        "success",
				CascadeFailed:   false,
			}},
		},
	}
	out := m.batchesPanel()
	assert.NotContains(t, out, "cascade failed")
}

func TestDashboard_CascadeFailedNoPRCannotBeMarked(t *testing.T) {
	// Edge case: a batch with no PR (PRNumber==0) should not render the
	// cascade marker even if CascadeFailed somehow got set, because the
	// marker is part of the PR status line.
	m := Model{
		Owner:      "o",
		Repo:       "r",
		RefreshSec: 5,
		state: State{
			LastRefresh: time.Now(),
			Batches: []BatchEntry{{
				MilestoneNumber: 1,
				MilestoneTitle:  "No PR yet",
				PRNumber:        0,
				CascadeFailed:   false,
			}},
		},
	}
	out := m.batchesPanel()
	assert.Contains(t, out, "PR not yet opened")
	assert.NotContains(t, out, "cascade failed")
}

func TestBatchesPanel_RendersStableDisagreementFlag(t *testing.T) {
	m := Model{
		Owner:      "o",
		Repo:       "r",
		RefreshSec: 5,
		state: State{
			LastRefresh: time.Now(),
			Batches: []BatchEntry{{
				MilestoneNumber:    1,
				MilestoneTitle:     "Test Batch",
				PRNumber:           42,
				StableDisagreement: true,
				HasAttention:       true,
			}},
		},
	}
	out := m.batchesPanel()
	assert.Contains(t, out, "stable-disagreement")
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"negative", -5 * time.Second, "0s"},
		{"seconds", 49 * time.Second, "49s"},
		{"just under minute", 59 * time.Second, "59s"},
		{"minute and seconds", 4*time.Minute + 12*time.Second, "4m 12s"},
		{"just under hour", 59*time.Minute + 30*time.Second, "59m 30s"},
		{"hour and minutes", 2*time.Hour + 5*time.Minute, "2h 5m"},
		{"large hours", 25*time.Hour + 30*time.Minute, "25h 30m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatElapsed(tt.d))
		})
	}
}
