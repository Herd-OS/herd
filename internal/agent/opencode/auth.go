package opencode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// openCodeAuthPluginName is the npm package / plugin id registered in
// opencode.json for ChatGPT subscription auth.
// TODO(verify): confirm the correct plugin package name. Best-effort
// default is the most-maintained community plugin per upstream READMEs.
// The pinned npm version lives in images/base/entrypoint.herd.sh.
const openCodeAuthPluginName = "opencode-openai-codex-auth"

var (
	ensureAuthOnce sync.Once
	ensureAuthErr  error
)

// ensureOpenCodeAuth provisions OpenCode subscription credentials from the
// OPENCODE_AUTH_JSON env var when set. It is a no-op (returns nil) when the
// env var is unset/empty, preserving the API-key auth path unchanged.
// It is safe to call from every agent method; provisioning runs at most once
// per process via sync.Once.
func ensureOpenCodeAuth() error {
	ensureAuthOnce.Do(func() { ensureAuthErr = provisionOpenCodeAuth() })
	return ensureAuthErr
}

// provisionOpenCodeAuth is the underlying provisioning routine, intentionally
// not wrapped in sync.Once so that unit tests can exercise it in isolation.
func provisionOpenCodeAuth() error {
	raw := os.Getenv("OPENCODE_AUTH_JSON")
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("OPENCODE_AUTH_JSON is not valid base64: %w", err)
	}

	authPath, err := openCodeAuthPath()
	if err != nil {
		return fmt.Errorf("resolving opencode auth path: %w", err)
	}

	forceSeed := os.Getenv("OPENCODE_AUTH_FORCE_SEED") == "1"

	_, statErr := os.Stat(authPath)
	switch {
	case statErr != nil && os.IsNotExist(statErr):
		if err := writeAuthFile(authPath, decoded); err != nil {
			return err
		}
	case statErr != nil:
		return fmt.Errorf("stat %s: %w", authPath, statErr)
	case forceSeed:
		if err := writeAuthFile(authPath, decoded); err != nil {
			return err
		}
	default:
		// File exists and !forceSeed: leave it untouched (a persisted
		// volume keeps refreshed/rotated tokens).
		// TODO(verify): refresh token MAY rotate; no-clobber preserves
		// the volume-persisted copy.
	}

	if err := ensurePluginRegistered(); err != nil {
		return fmt.Errorf("registering opencode auth plugin: %w", err)
	}
	return nil
}

func writeAuthFile(authPath string, decoded []byte) error {
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		return fmt.Errorf("creating opencode data dir: %w", err)
	}
	if err := os.WriteFile(authPath, decoded, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", authPath, err)
	}
	return nil
}

// openCodeAuthPath returns the path to OpenCode's auth.json file, honoring
// XDG_DATA_HOME with a fallback to ~/.local/share.
// TODO(verify): confirm auth.json lives at
// ${XDG_DATA_HOME:-~/.local/share}/opencode/auth.json.
func openCodeAuthPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "opencode", "auth.json"), nil
}

// ensurePluginRegistered makes sure openCodeAuthPluginName appears in the
// "plugin" array of ~/.config/opencode/opencode.json. It merges with any
// existing config without clobbering unrelated keys and is idempotent.
// TODO(verify): confirm OpenCode actually requires plugin registration in
// opencode.json. If npm-global install alone is sufficient, this whole step
// can be removed.
func ensurePluginRegistered() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")

	var cfg map[string]any
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parsing existing %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", cfgPath, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	var existing []any
	if raw, ok := cfg["plugin"]; ok {
		if arr, ok := raw.([]any); ok {
			existing = arr
		}
	}

	for _, entry := range existing {
		if s, ok := entry.(string); ok && s == openCodeAuthPluginName {
			// Already present — idempotent no-op. Do not rewrite the file.
			return nil
		}
	}

	existing = append(existing, openCodeAuthPluginName)
	cfg["plugin"] = existing

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("creating opencode config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling opencode config: %w", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	return nil
}
