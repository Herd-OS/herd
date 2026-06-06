package codex

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	provisionOnce sync.Once
	provisionErr  error
)

// ensureProvisioned runs provisionCodexAuth exactly once per process.
func ensureProvisioned() error {
	provisionOnce.Do(func() { provisionErr = provisionCodexAuth() })
	return provisionErr
}

// provisionCodexAuth seeds ~/.codex/auth.json from the base64-encoded
// CODEX_AUTH_JSON env var so ChatGPT-subscription OAuth credentials survive
// across worker invocations in a long-lived runner container. It is idempotent:
// a .herd-seed marker records the last-applied env value, so the auth.json is
// only rewritten when the env var changes.
func provisionCodexAuth() error {
	envSeed := strings.TrimSpace(os.Getenv("CODEX_AUTH_JSON"))
	if envSeed == "" {
		return nil // no subscription auth requested
	}
	codexHome, err := resolveCodexHome()
	if err != nil {
		return err
	}
	seedFile := filepath.Join(codexHome, ".herd-seed")
	authFile := filepath.Join(codexHome, "auth.json")
	cfgFile := filepath.Join(codexHome, "config.toml")
	existingSeed, err := os.ReadFile(seedFile)
	if err == nil && bytes.Equal(existingSeed, []byte(envSeed)) {
		return ensureConfigToml(cfgFile)
	}
	decoded, err := base64.StdEncoding.DecodeString(envSeed)
	if err != nil {
		return fmt.Errorf("CODEX_AUTH_JSON is not valid base64: %w", err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return err
	}
	// MkdirAll honors the process umask, so the dir may end up more restrictive
	// than 0o700 but never broader. Chmod explicitly to make the perms
	// deterministic regardless of the environment's umask.
	if err := os.Chmod(codexHome, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(authFile, decoded, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(seedFile, []byte(envSeed), 0o600); err != nil {
		return err
	}
	return ensureConfigToml(cfgFile)
}

// ensureConfigToml writes a minimal config.toml that pins Codex to the
// file-based credentials store, but leaves any existing config untouched.
func ensureConfigToml(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // exists, leave alone
	}
	return os.WriteFile(path, []byte(`cli_auth_credentials_store = "file"`+"\n"), 0o600)
}

// authJSONPresent reports whether a Codex subscription credential file
// (auth.json) exists under the resolved Codex home ($CODEX_HOME, else
// $HOME/.codex). It returns false on any stat error (missing file, unreadable
// home dir, etc.) so a missing auth.json is treated as "no subscription".
func authJSONPresent() bool {
	codexHome, err := resolveCodexHome()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		return false
	}
	return true
}

// resolveCodexHome returns $CODEX_HOME if set, else $HOME/.codex.
func resolveCodexHome() (string, error) {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}
