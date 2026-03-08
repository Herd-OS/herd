package display

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableRender(t *testing.T) {
	tbl := NewTable("NAME", "STATUS", "BUSY")
	tbl.AddRow("worker-1", "online", "idle")
	tbl.AddRow("worker-2", "offline", "—")

	out := tbl.Render()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "worker-1")
	assert.Contains(t, out, "worker-2")
	assert.Contains(t, out, "online")
	assert.Contains(t, out, "offline")
}

func TestTableAlignment(t *testing.T) {
	tbl := NewTable("A", "LONG HEADER")
	tbl.AddRow("short", "x")
	tbl.AddRow("a very long value", "y")

	out := tbl.Render()
	// Should not panic and should contain all values
	assert.Contains(t, out, "short")
	assert.Contains(t, out, "a very long value")
}

func TestTableEmpty(t *testing.T) {
	tbl := NewTable()
	assert.Equal(t, "", tbl.Render())
}

func TestProgress(t *testing.T) {
	assert.Equal(t, "3/5 (60%)", Progress(3, 5))
	assert.Equal(t, "0/0 (0%)", Progress(0, 0))
	assert.Equal(t, "5/5 (100%)", Progress(5, 5))
}

func TestTableFewerColumnsThanHeaders(t *testing.T) {
	tbl := NewTable("A", "B", "C")
	tbl.AddRow("only-one")

	out := tbl.Render()
	assert.Contains(t, out, "only-one")
	// Should not panic even with missing columns
}

func TestTableMoreColumnsThanHeaders(t *testing.T) {
	tbl := NewTable("A")
	tbl.AddRow("one", "two", "three")

	out := tbl.Render()
	assert.Contains(t, out, "one")
	// Extra columns beyond headers are ignored gracefully
}

func TestTableNoRows(t *testing.T) {
	tbl := NewTable("NAME", "STATUS")
	out := tbl.Render()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "STATUS")
}

func TestTableString(t *testing.T) {
	tbl := NewTable("X")
	tbl.AddRow("y")
	assert.Equal(t, tbl.Render(), tbl.String())
}

func TestProgressEdgeCases(t *testing.T) {
	assert.Equal(t, "1/1 (100%)", Progress(1, 1))
	assert.Equal(t, "0/10 (0%)", Progress(0, 10))
	assert.Equal(t, "1/3 (33%)", Progress(1, 3))
}

func TestStatusMessages(t *testing.T) {
	// Just verify they don't panic and contain the message
	assert.Contains(t, Success("done"), "done")
	assert.Contains(t, Error("failed"), "failed")
	assert.Contains(t, InProgress("running"), "running")
	assert.Contains(t, Blocked("waiting"), "waiting")
	assert.Contains(t, Warning("careful"), "careful")
}
