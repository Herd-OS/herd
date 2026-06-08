package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent/codex"
	"github.com/herd-os/herd/internal/config"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "OK"
	doctorWarn doctorStatus = "WARN"
	doctorErr  doctorStatus = "ERR"
	doctorInfo doctorStatus = "INFO"
)

type doctorRow struct {
	Status doctorStatus
	Check  string
	Detail string
}

type doctorContext struct {
	CodexHome     string
	Auth          doctorAuthState
	Env           doctorEnvState
	EffectiveAuth string
}

type doctorAuthKind string

const (
	doctorAuthAbsent      doctorAuthKind = "absent"
	doctorAuthChatGPT     doctorAuthKind = "chatgpt"
	doctorAuthAPIKey      doctorAuthKind = "apikey"
	doctorAuthUnparseable doctorAuthKind = "unparseable"
)

type doctorAuthState struct {
	Kind     doctorAuthKind
	Path     string
	AuthMode string
	Err      error
}

type doctorEnvState struct {
	CodexAPIKey      bool
	OpenAIAPIKey     bool
	CodexAccessToken bool
	CodexAuthJSON    bool
}

type doctorCheck func(context.Context, *doctorContext) doctorRow

var errCodexDoctorFailed = errors.New("codex doctor found errors")

var codexDoctorChecks = []doctorCheck{
	checkCodexBinary,
	checkCodexVersion,
	checkCodexAuthJSON,
	checkCodexAuthEnv,
	checkCodexEffectiveAuth,
	checkCodexConfig,
	checkStaleCodexAuthJSONEnv,
}

func runCodexDoctor(ctx context.Context, out io.Writer) error {
	dctx := &doctorContext{}
	rows := make([]doctorRow, 0, len(codexDoctorChecks))
	hasErr := false
	for _, check := range codexDoctorChecks {
		row := check(ctx, dctx)
		if row.Status == doctorErr {
			hasErr = true
		}
		rows = append(rows, row)
	}

	if _, err := fmt.Fprintf(out, "%-6s  %-24s  %s\n", "STATUS", "CHECK", "DETAIL"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(out, "%-6s  %-24s  %s\n", row.Status, row.Check, row.Detail); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "Active path: %s\n", dctx.EffectiveAuth); err != nil {
		return err
	}

	if hasErr {
		return errCodexDoctorFailed
	}
	return nil
}

func checkCodexBinary(context.Context, *doctorContext) doctorRow {
	// Always looks up the literal "codex" rather than cfg.Agent.Binary — the
	// doctor's job is to diagnose whether the codex CLI is reachable at all,
	// not to validate a user's explicit binary-path override.
	path, err := exec.LookPath("codex")
	if err != nil {
		return doctorRow{Status: doctorErr, Check: "Codex binary", Detail: "not found in PATH=" + os.Getenv("PATH")}
	}
	return doctorRow{Status: doctorOK, Check: "Codex binary", Detail: "found at " + path}
}

func checkCodexVersion(ctx context.Context, _ *doctorContext) doctorRow {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "codex", "--version")
	output, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(output))
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return doctorRow{Status: doctorErr, Check: "Codex version", Detail: "codex --version timed out after 10s"}
	}
	if err != nil {
		if detail != "" {
			return doctorRow{Status: doctorErr, Check: "Codex version", Detail: fmt.Sprintf("codex --version failed: %v: %s", err, detail)}
		}
		return doctorRow{Status: doctorErr, Check: "Codex version", Detail: fmt.Sprintf("codex --version failed: %v", err)}
	}
	if detail == "" {
		detail = "codex --version completed with no output"
	}
	return doctorRow{Status: doctorOK, Check: "Codex version", Detail: detail}
}

func checkCodexAuthJSON(_ context.Context, dctx *doctorContext) doctorRow {
	home, err := codex.ResolveCodexHome()
	if err != nil {
		dctx.Auth = doctorAuthState{Kind: doctorAuthAbsent, Err: err}
		return doctorRow{Status: doctorWarn, Check: "auth.json", Detail: fmt.Sprintf("could not resolve Codex home: %v", err)}
	}
	dctx.CodexHome = home
	path := filepath.Join(home, "auth.json")
	dctx.Auth.Path = path

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			dctx.Auth.Kind = doctorAuthAbsent
			return doctorRow{Status: doctorInfo, Check: "auth.json", Detail: "absent at " + path}
		}
		dctx.Auth.Kind = doctorAuthUnparseable
		dctx.Auth.Err = err
		return doctorRow{Status: doctorWarn, Check: "auth.json", Detail: fmt.Sprintf("cannot stat %s: %v", path, err)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		dctx.Auth.Kind = doctorAuthUnparseable
		dctx.Auth.Err = err
		return doctorRow{Status: doctorWarn, Check: "auth.json", Detail: fmt.Sprintf("present at %s but unparseable: %v", path, err)}
	}
	var doc struct {
		AuthMode string `json:"auth_mode"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		dctx.Auth.Kind = doctorAuthUnparseable
		dctx.Auth.Err = err
		return doctorRow{Status: doctorWarn, Check: "auth.json", Detail: fmt.Sprintf("present at %s but unparseable: %v", path, err)}
	}

	dctx.Auth.AuthMode = doc.AuthMode
	switch doc.AuthMode {
	case "chatgpt":
		dctx.Auth.Kind = doctorAuthChatGPT
		return doctorRow{Status: doctorOK, Check: "auth.json", Detail: "chatgpt subscription auth at " + path}
	case "apikey":
		dctx.Auth.Kind = doctorAuthAPIKey
		return doctorRow{Status: doctorInfo, Check: "auth.json", Detail: "auth_mode=apikey at " + path}
	default:
		dctx.Auth.Kind = doctorAuthUnparseable
		authMode := doc.AuthMode
		if authMode == "" {
			authMode = "(empty)"
		}
		return doctorRow{Status: doctorWarn, Check: "auth.json", Detail: fmt.Sprintf("auth_mode=%s at %s", authMode, path)}
	}
}

func checkCodexAuthEnv(_ context.Context, dctx *doctorContext) doctorRow {
	dctx.Env = doctorEnvState{
		CodexAPIKey:      envSet("CODEX_API_KEY"),
		OpenAIAPIKey:     envSet("OPENAI_API_KEY"),
		CodexAccessToken: envSet("CODEX_ACCESS_TOKEN"),
		CodexAuthJSON:    envSet("CODEX_AUTH_JSON"),
	}
	detail := fmt.Sprintf("CODEX_API_KEY=%s OPENAI_API_KEY=%s CODEX_ACCESS_TOKEN=%s",
		setUnset(dctx.Env.CodexAPIKey),
		setUnset(dctx.Env.OpenAIAPIKey),
		setUnset(dctx.Env.CodexAccessToken),
	)
	if dctx.Env.CodexAPIKey || dctx.Env.OpenAIAPIKey || dctx.Env.CodexAccessToken {
		return doctorRow{Status: doctorOK, Check: "Auth env", Detail: detail}
	}
	return doctorRow{Status: doctorInfo, Check: "Auth env", Detail: detail}
}

func checkCodexEffectiveAuth(_ context.Context, dctx *doctorContext) doctorRow {
	// Codex also supports an ephemeral key between CODEX_API_KEY and
	// CODEX_ACCESS_TOKEN, but a normal shell cannot observe that path.
	switch {
	case dctx.Env.CodexAPIKey:
		dctx.EffectiveAuth = "CODEX_API_KEY"
		if dctx.Env.OpenAIAPIKey {
			dctx.EffectiveAuth += " (OPENAI_API_KEY ignored because CODEX_API_KEY is set)"
		}
		return doctorRow{Status: doctorOK, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	case dctx.Env.OpenAIAPIKey && dctx.Auth.Kind == doctorAuthAbsent:
		dctx.EffectiveAuth = "OPENAI_API_KEY auto-mapped to CODEX_API_KEY"
		if dctx.Env.CodexAccessToken {
			dctx.EffectiveAuth += " (CODEX_ACCESS_TOKEN ignored — auto-mapped OPENAI_API_KEY has higher precedence; unset OPENAI_API_KEY to use CODEX_ACCESS_TOKEN)"
			return doctorRow{Status: doctorWarn, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
		}
		return doctorRow{Status: doctorOK, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	case dctx.Env.CodexAccessToken:
		dctx.EffectiveAuth = "CODEX_ACCESS_TOKEN"
		return doctorRow{Status: doctorOK, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	}

	switch dctx.Auth.Kind {
	case doctorAuthChatGPT:
		dctx.EffectiveAuth = "ChatGPT subscription"
		if dctx.Env.OpenAIAPIKey {
			dctx.EffectiveAuth += " (OPENAI_API_KEY shadowed by auth.json gate)"
		}
		return doctorRow{Status: doctorOK, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	case doctorAuthAPIKey:
		dctx.EffectiveAuth = "auth.json auth_mode=apikey (per-token API key auth)"
		return doctorRow{Status: doctorOK, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	case doctorAuthUnparseable:
		dctx.EffectiveAuth = "auth.json unparseable"
		if dctx.Env.OpenAIAPIKey {
			dctx.EffectiveAuth += " (OPENAI_API_KEY shadowed by auth.json gate)"
		}
		return doctorRow{Status: doctorWarn, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	default:
		dctx.EffectiveAuth = "none detected"
		return doctorRow{Status: doctorInfo, Check: "Effective auth", Detail: "Active path: " + dctx.EffectiveAuth}
	}
}

func checkCodexConfig(_ context.Context, _ *doctorContext) doctorRow {
	cfg, err := loadConfig()
	if err != nil {
		if strings.Contains(err.Error(), "no "+config.ConfigFile+" found") {
			return doctorRow{Status: doctorInfo, Check: "Herd config", Detail: "no .herdos.yml found; checks 1-5 still useful for ad-hoc codex usage"}
		}
		return doctorRow{Status: doctorWarn, Check: "Herd config", Detail: err.Error()}
	}

	if cfg.Agent.Provider == "codex" {
		model := strings.TrimSpace(cfg.Agent.Model)
		if model == "" {
			return doctorRow{Status: doctorWarn, Check: "Herd config", Detail: "agent.provider=codex but agent.model is empty; Codex will use its built-in default"}
		}
		return doctorRow{Status: doctorOK, Check: "Herd config", Detail: "agent.provider=codex agent.model=" + model}
	}
	return doctorRow{Status: doctorInfo, Check: "Herd config", Detail: fmt.Sprintf("Codex doctor runs against agent.provider: codex configs; current is %s — checks 1-5 still useful for ad-hoc codex usage", cfg.Agent.Provider)}
}

func checkStaleCodexAuthJSONEnv(_ context.Context, dctx *doctorContext) doctorRow {
	dctx.Env.CodexAuthJSON = envSet("CODEX_AUTH_JSON")
	if dctx.Env.CodexAuthJSON {
		return doctorRow{Status: doctorInfo, Check: "CODEX_AUTH_JSON", Detail: "CODEX_AUTH_JSON is no longer used since the #725 removal; safe to remove from your shell profile"}
	}
	return doctorRow{Status: doctorOK, Check: "CODEX_AUTH_JSON", Detail: "CODEX_AUTH_JSON unset"}
}

func envSet(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

func setUnset(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}
