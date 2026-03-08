package display

import (
	"fmt"
	"strings"
)

// Table renders a simple aligned table.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable creates a table with the given headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// AddRow adds a row to the table.
func (t *Table) AddRow(cols ...string) {
	t.rows = append(t.rows, cols)
}

// Render returns the table as a formatted string.
func (t *Table) Render() string {
	if len(t.headers) == 0 {
		return ""
	}

	// Calculate column widths
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(h)
	}
	for _, row := range t.rows {
		for i, col := range row {
			if i < len(widths) && len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}

	var b strings.Builder

	// Header
	for i, h := range t.headers {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(Bold.Render(pad(h, widths[i])))
	}
	b.WriteString("\n")

	// Rows
	for _, row := range t.rows {
		for i := 0; i < len(t.headers); i++ {
			if i > 0 {
				b.WriteString("  ")
			}
			val := ""
			if i < len(row) {
				val = row[i]
			}
			if i < len(t.headers)-1 {
				b.WriteString(pad(val, widths[i]))
			} else {
				b.WriteString(val) // no trailing padding on last column
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// String implements fmt.Stringer.
func (t *Table) String() string {
	return t.Render()
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// Progress renders a progress string like "3/5 done".
func Progress(done, total int) string {
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	return fmt.Sprintf("%d/%d (%d%%)", done, total, pct)
}
