package dashboard

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModel_QuitOnQ(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "q", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}},
		{name: "ctrl+c", key: tea.KeyMsg{Type: tea.KeyCtrlC}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(nil, "owner", "repo", 15)
			updated, cmd := m.Update(tt.key)
			require.NotNil(t, cmd, "expected a command to be returned")
			mu, ok := updated.(Model)
			require.True(t, ok)
			assert.True(t, mu.quitting, "model should be marked quitting")
			// Compare commands by invoking; tea.Quit returns a tea.QuitMsg.
			msg := cmd()
			_, isQuit := msg.(tea.QuitMsg)
			assert.True(t, isQuit, "expected tea.Quit message, got %T", msg)
		})
	}
}

func TestModel_SelectionBounds(t *testing.T) {
	tests := []struct {
		name        string
		batchesLen  int
		startSel    int
		key         string
		wantSel     int
	}{
		{name: "down at last stays", batchesLen: 3, startSel: 2, key: "down", wantSel: 2},
		{name: "down advances", batchesLen: 3, startSel: 1, key: "down", wantSel: 2},
		{name: "up at zero stays", batchesLen: 3, startSel: 0, key: "up", wantSel: 0},
		{name: "up decrements", batchesLen: 3, startSel: 2, key: "up", wantSel: 1},
		{name: "j alias for down", batchesLen: 3, startSel: 0, key: "j", wantSel: 1},
		{name: "k alias for up", batchesLen: 3, startSel: 1, key: "k", wantSel: 0},
		{name: "down on empty stays at 0", batchesLen: 0, startSel: 0, key: "down", wantSel: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(nil, "o", "r", 15)
			batches := make([]BatchEntry, tt.batchesLen)
			for i := range batches {
				batches[i] = BatchEntry{MilestoneNumber: i + 1}
			}
			m.state.Batches = batches
			m.selected = tt.startSel

			var key tea.KeyMsg
			switch tt.key {
			case "up":
				key = tea.KeyMsg{Type: tea.KeyUp}
			case "down":
				key = tea.KeyMsg{Type: tea.KeyDown}
			case "j":
				key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
			case "k":
				key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
			}

			updated, _ := m.Update(key)
			mu, ok := updated.(Model)
			require.True(t, ok)
			assert.Equal(t, tt.wantSel, mu.selected)
		})
	}
}

func TestModel_StateMsgPreservesOnEmptyError(t *testing.T) {
	prior := []BatchEntry{
		{MilestoneNumber: 1, MilestoneTitle: "alpha"},
		{MilestoneNumber: 2, MilestoneTitle: "beta"},
	}
	priorWorkers := []WorkerEntry{{RunID: 100, IssueNumber: 7}}
	priorFailures := []FailureEntry{{Number: 5, Title: "broken"}}

	m := NewModel(nil, "o", "r", 15)
	m.state.Batches = prior
	m.state.Workers = priorWorkers
	m.state.Failures = priorFailures
	m.state.LastRefresh = time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	now := time.Date(2026, 5, 6, 12, 5, 0, 0, time.UTC)
	updated, _ := m.Update(stateMsg{
		s:      State{Owner: "o", Repo: "r", LastRefresh: now},
		errStr: "list milestones: boom",
	})
	mu, ok := updated.(Model)
	require.True(t, ok)
	assert.Equal(t, prior, mu.state.Batches, "batches preserved")
	assert.Equal(t, priorWorkers, mu.state.Workers, "workers preserved")
	assert.Equal(t, priorFailures, mu.state.Failures, "failures preserved")
	assert.Equal(t, now, mu.state.LastRefresh, "LastRefresh updated")
	assert.Equal(t, "list milestones: boom", mu.state.FetchError)
}

func TestModel_StateMsgReplacesOnSuccess(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	m.state.Batches = []BatchEntry{{MilestoneNumber: 1, MilestoneTitle: "old"}}

	fresh := State{
		Owner:   "o",
		Repo:    "r",
		Batches: []BatchEntry{{MilestoneNumber: 2, MilestoneTitle: "new"}},
	}
	updated, _ := m.Update(stateMsg{s: fresh, errStr: ""})
	mu, ok := updated.(Model)
	require.True(t, ok)
	require.Len(t, mu.state.Batches, 1)
	assert.Equal(t, "new", mu.state.Batches[0].MilestoneTitle)
	assert.Empty(t, mu.state.FetchError)
}

func TestModel_SelectionClampedAfterStateMsg(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	m.state.Batches = []BatchEntry{
		{MilestoneNumber: 1}, {MilestoneNumber: 2}, {MilestoneNumber: 3},
	}
	m.selected = 2

	// New state has fewer batches; selection should clamp.
	updated, _ := m.Update(stateMsg{
		s: State{Batches: []BatchEntry{{MilestoneNumber: 9}}},
	})
	mu, ok := updated.(Model)
	require.True(t, ok)
	assert.Equal(t, 0, mu.selected)
}

func TestModel_RefreshKeyTriggersFetch(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	require.NotNil(t, cmd, "pressing r should return a fetch command")
}

func TestModel_TickReturnsFetchAndTick(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	_, cmd := m.Update(tickMsg(time.Now()))
	require.NotNil(t, cmd, "tick should return a batch command")
}

func TestModel_WindowSizeMsg(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mu, ok := updated.(Model)
	require.True(t, ok)
	assert.Equal(t, 80, mu.width)
	assert.Equal(t, 24, mu.height)
}

func TestMilestoneURL(t *testing.T) {
	tests := []struct {
		owner, repo string
		n           int
		want        string
	}{
		{"acme", "widgets", 12, "https://github.com/acme/widgets/milestone/12"},
		{"o", "r", 1, "https://github.com/o/r/milestone/1"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, milestoneURL(tt.owner, tt.repo, tt.n))
		})
	}
}

func TestModel_Init(t *testing.T) {
	m := NewModel(nil, "o", "r", 15)
	cmd := m.Init()
	assert.NotNil(t, cmd)
}
