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
// The pinned npm version lives in images/base/Dockerfile.
const openCodeAuthPluginName = "opencode-openai-codex-auth"

// openCodeClaudeAuthPluginName is the npm package / plugin id registered in
// opencode.json for Anthropic (Claude) subscription auth via the community
// Claude OAuth bridge. It reuses CLAUDE_CODE_OAUTH_TOKEN (the same token the
// `claude` provider uses) instead of an Anthropic API key.
// TODO(verify): confirm the exact npm package name `opencode-claude-auth`
// (it may be scoped) against upstream griffinmartin/opencode-claude-auth.
// The pinned npm version lives in images/base/Dockerfile.
const openCodeClaudeAuthPluginName = "opencode-claude-auth"

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
	openAIRaw := strings.TrimSpace(os.Getenv("OPENCODE_AUTH_JSON"))
	claudeToken := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))

	var plugins []string

	// OpenAI (ChatGPT) subscription path: seed auth.json from OPENCODE_AUTH_JSON.
	if openAIRaw != "" {
		decoded, err := base64.StdEncoding.DecodeString(openAIRaw)
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
		plugins = append(plugins, openCodeAuthPluginName)
	}

	// Anthropic (Claude) subscription path: env-var-only. The bridge plugin is
	// assumed to read CLAUDE_CODE_OAUTH_TOKEN directly on each opencode run, so
	// no credential file is written here.
	// TODO(verify): confirm the plugin's credential intake. If it also/instead
	// requires a credential FILE at a known path (e.g.
	// ~/.config/claude/credentials.json), add a provisionClaudeOAuthFile step
	// that writes it from CLAUDE_CODE_OAUTH_TOKEN, mirroring the
	// OPENCODE_AUTH_JSON -> auth.json no-clobber/force-seed pattern.
	if claudeToken != "" {
		plugins = append(plugins, openCodeClaudeAuthPluginName)
	}

	if err := ensurePluginRegistered(plugins); err != nil {
		return fmt.Errorf("registering opencode auth plugins: %w", err)
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

// ensurePluginRegistered makes sure each name in plugins appears in the
// "plugin" array of ~/.config/opencode/opencode.json. It merges with any
// existing config without clobbering unrelated keys and is idempotent: if
// every requested plugin is already present the file is left byte-identical.
// An empty or nil plugins slice is a no-op (no file is created).
// TODO(verify): confirm OpenCode actually requires plugin registration in
// opencode.json for these plugins. If npm-global install alone is sufficient,
// this whole step can be removed.
func ensurePluginRegistered(plugins []string) error {
	if len(plugins) == 0 {
		return nil
	}
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

	// Build a set of already-present plugin names for idempotency.
	present := map[string]bool{}
	for _, entry := range existing {
		if s, ok := entry.(string); ok {
			present[s] = true
		}
	}

	changed := false
	for _, name := range plugins {
		if !present[name] {
			existing = append(existing, name)
			present[name] = true
			changed = true
		}
	}
	if !changed {
		// Every requested plugin already present — do not rewrite the file.
		return nil
	}
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
