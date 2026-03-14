package display

// Status symbols for terminal output.
const (
	SymbolSuccess    = "✓"
	SymbolError      = "✗"
	SymbolInProgress = "⟳"
	SymbolBlocked    = "◌"
	SymbolWarning    = "⚠"
	SymbolManual     = "👤"
)

// Success renders a green success message.
func Success(msg string) string {
	return Green.Render(SymbolSuccess) + " " + msg
}

// Error renders a red error message.
func Error(msg string) string {
	return Red.Render(SymbolError) + " " + msg
}

// InProgress renders a yellow in-progress message.
func InProgress(msg string) string {
	return Yellow.Render(SymbolInProgress) + " " + msg
}

// Blocked renders a muted blocked message.
func Blocked(msg string) string {
	return Muted.Render(SymbolBlocked) + " " + msg
}

// Warning renders a yellow warning message.
func Warning(msg string) string {
	return Yellow.Render(SymbolWarning) + " " + msg
}

// Manual renders a manual task message.
func Manual(msg string) string {
	return SymbolManual + " " + msg
}
