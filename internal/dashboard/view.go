package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	selStyle   = lipgloss.NewStyle().Reverse(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.header() + "\n")
	b.WriteString(panelStyle.Render(m.workersPanel()) + "\n")
	b.WriteString(panelStyle.Render(m.batchesPanel()) + "\n")
	b.WriteString(panelStyle.Render(m.failuresPanel()) + "\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header() string {
	last := "never"
	if !m.state.LastRefresh.IsZero() {
		last = m.state.LastRefresh.Format("15:04:05")
	}
	line := fmt.Sprintf("herd dashboard · %s/%s · refresh %ds · last %s",
		m.Owner, m.Repo, m.RefreshSec, last)
	if m.state.FetchError != "" {
		line += "  " + errStyle.Render("⚠ "+m.state.FetchError)
	}
	return titleStyle.Render(line)
}

func (m Model) workersPanel() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Active workers (%d)", len(m.state.Workers))) + "\n")
	if len(m.state.Workers) == 0 {
		b.WriteString(dimStyle.Render("no active workers"))
		return b.String()
	}
	now := time.Now()
	const maxRowWidth = 80
	for _, w := range m.state.Workers {
		elapsed := formatElapsed(now.Sub(w.StartedAt))
		elapsedSeg := "(" + elapsed + ")"

		var line string
		switch {
		case w.IssueNumber == 0 && w.IssueTitle == "":
			line = "  " + elapsedSeg
		case w.IssueTitle == "":
			line = fmt.Sprintf("  #%d %s", w.IssueNumber, elapsedSeg)
		default:
			prefix := fmt.Sprintf("  #%d ", w.IssueNumber)
			available := maxRowWidth - len(prefix) - len(elapsedSeg) - 1
			if available < 8 {
				available = 8
			}
			title := truncate(w.IssueTitle, available)
			line = fmt.Sprintf("%s%s %s", prefix, title, elapsedSeg)
		}
		b.WriteString(Hyperlink(w.URL, line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatElapsed(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %dm", h, m)
}

func (m Model) batchesPanel() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Open batches (%d)", len(m.state.Batches))) + "\n")
	if len(m.state.Batches) == 0 {
		b.WriteString(dimStyle.Render("no open batches"))
		return b.String()
	}
	for i, be := range m.state.Batches {
		prefix := "  "
		if i == m.selected {
			prefix = selStyle.Render("▶ ")
		}
		head := fmt.Sprintf("%s#%d %s", prefix, be.MilestoneNumber, be.MilestoneTitle)
		b.WriteString(head + "\n")
		if be.PRNumber > 0 {
			review := be.ReviewState
			if review == "" {
				review = "-"
			}
			b.WriteString(fmt.Sprintf("      PR #%d · CI %s · Review: %s\n", be.PRNumber, statusOr(be.CIStatus), review))
		} else {
			b.WriteString("      PR not yet opened\n")
		}
		glyphs := TierProgressGlyphs(be.Done, be.InProgress, be.Ready, be.Failed)
		total := be.Done + be.InProgress + be.Ready + be.Failed + be.Blocked
		b.WriteString(fmt.Sprintf("      Tier %d/%d · %d/%d done · %d in-progress · %d failed  %s\n",
			be.Tier, be.TotalTiers, be.Done, total, be.InProgress, be.Failed, glyphs))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) failuresPanel() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Recent failures (%d in last 24h)", len(m.state.Failures))) + "\n")
	shown := m.state.Failures
	extra := 0
	if len(shown) > 10 {
		extra = len(shown) - 10
		shown = shown[:10]
	}
	if len(shown) == 0 {
		b.WriteString(dimStyle.Render("none"))
		return b.String()
	}
	for _, f := range shown {
		b.WriteString(fmt.Sprintf("  #%d %s  (%s) %s\n",
			f.Number, truncate(f.Title, 50), f.UpdatedAt.Format("15:04"), f.Label))
	}
	if extra > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ...and %d more", extra)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) footer() string {
	return dimStyle.Render("[q] quit · [r] refresh · [↑↓] select · [enter] open in browser")
}

func statusOr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
