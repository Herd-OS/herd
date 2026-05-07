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
