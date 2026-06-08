package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuthJSONPresent reports whether a Codex subscription credential file
// (auth.json) exists under the resolved Codex home ($CODEX_HOME, else
// $HOME/.codex). It returns false on any stat error (missing file, unreadable
// home dir, etc.) so a missing auth.json is treated as "no subscription".
// This is a deliberate policy choice: an "exists but unreadable" auth.json is
// treated as "absent", so a broken credential file falls through to the
// OPENAI_API_KEY->CODEX_API_KEY mapping rather than blocking it.
func AuthJSONPresent() bool {
	codexHome, err := ResolveCodexHome()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		return false
	}
	return true
}

// ResolveCodexHome returns $CODEX_HOME if set, else $HOME/.codex.
func ResolveCodexHome() (string, error) {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}
