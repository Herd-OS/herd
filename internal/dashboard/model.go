package dashboard

import (
	"context"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/herd-os/herd/internal/platform"
)

type tickMsg time.Time

type stateMsg struct {
	s      State
	errStr string
}

// Model is the bubbletea model. Exported so cli/dashboard.go can construct it.
type Model struct {
	Platform   platform.Platform
	Owner      string
	Repo       string
	RefreshSec int

	state    State
	selected int
	width    int
	height   int
	quitting bool
}

// NewModel constructs a Model with the supplied platform client and config.
func NewModel(p platform.Platform, owner, repo string, refreshSec int) Model {
	return Model{Platform: p, Owner: owner, Repo: repo, RefreshSec: refreshSec}
}

// Init starts the initial fetch and tick loop.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(time.Duration(m.RefreshSec)*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) fetchCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, errStr := Fetch(ctx, m.Platform, m.Owner, m.Repo)
		return stateMsg{s: s, errStr: errStr}
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, m.fetchCmd()
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.state.Batches)-1 {
				m.selected++
			}
		case "enter":
			if m.selected >= 0 && m.selected < len(m.state.Batches) {
				b := m.state.Batches[m.selected]
				target := b.PRURL
				if target == "" {
					target = milestoneURL(m.Owner, m.Repo, b.MilestoneNumber)
				}
				_ = OpenURL(target)
			}
		}
	case tickMsg:
		return m, tea.Batch(m.fetchCmd(), m.tickCmd())
	case stateMsg:
		// Preserve previous data on partial failure: only replace populated slices.
		if msg.errStr != "" && len(msg.s.Batches) == 0 && len(m.state.Batches) > 0 {
			prev := m.state
			prev.LastRefresh = msg.s.LastRefresh
			prev.FetchError = msg.errStr
			m.state = prev
		} else {
			m.state = msg.s
			m.state.FetchError = msg.errStr
		}
		if m.selected >= len(m.state.Batches) {
			m.selected = len(m.state.Batches) - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
	}
	return m, nil
}

func milestoneURL(owner, repo string, n int) string {
	return "https://github.com/" + owner + "/" + repo + "/milestone/" + strconv.Itoa(n)
}
