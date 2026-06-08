package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCodexBinary(t *testing.T) {
	tests := []struct {
		name       string
		withBinary bool
		wantStatus doctorStatus
		wantDetail string
	}{
		{name: "found", withBinary: true, wantStatus: doctorOK, wantDetail: "found at "},
		{name: "missing", wantStatus: doctorErr, wantDetail: "not found in PATH="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.withBinary {
				writeFakeDoctorCodex(t, dir, "printf 'codex test\\n'\nexit 0\n")
			}
			t.Setenv("PATH", dir)

			row := checkCodexBinary(context.Background(), &doctorContext{})

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, "Codex binary", row.Check)
			assert.Contains(t, row.Detail, tt.wantDetail)
		})
	}
}

func TestCheckCodexVersion(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus doctorStatus
		wantDetail string
	}{
		{
			name:       "success with output",
			body:       "printf 'codex-cli 1.2.3\\n'\nexit 0\n",
			wantStatus: doctorOK,
			wantDetail: "codex-cli 1.2.3",
		},
		{
			name:       "success no output",
			body:       "exit 0\n",
			wantStatus: doctorOK,
			wantDetail: "codex --version completed with no output",
		},
		{
			name:       "failure includes output",
			body:       "printf 'bad version\\n' >&2\nexit 7\n",
			wantStatus: doctorErr,
			wantDetail: "codex --version failed: exit status 7: bad version",
		},
		{
			name:       "timeout",
			body:       "/bin/sleep 3\n",
			wantStatus: doctorErr,
			wantDetail: "codex --version timed out after 2s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFakeDoctorCodex(t, dir, tt.body)
			t.Setenv("PATH", dir)

			row := checkCodexVersion(context.Background(), &doctorContext{})

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, "Codex version", row.Check)
			assert.Contains(t, row.Detail, tt.wantDetail)
		})
	}
}

func TestCheckCodexAuthJSON(t *testing.T) {
	tests := []struct {
		name       string
		content    *string
		wantStatus doctorStatus
		wantKind   doctorAuthKind
		wantDetail string
	}{
		{
			name:       "absent",
			wantStatus: doctorInfo,
			wantKind:   doctorAuthAbsent,
			wantDetail: "absent at ",
		},
		{
			name:       "chatgpt",
			content:    ptr(`{"auth_mode":"chatgpt","token":"secret"}`),
			wantStatus: doctorOK,
			wantKind:   doctorAuthChatGPT,
			wantDetail: "chatgpt subscription auth at ",
		},
		{
			name:       "apikey",
			content:    ptr(`{"auth_mode":"apikey","api_key":"secret"}`),
			wantStatus: doctorInfo,
			wantKind:   doctorAuthAPIKey,
			wantDetail: "auth_mode=apikey at ",
		},
		{
			name:       "unparseable json",
			content:    ptr(`{`),
			wantStatus: doctorWarn,
			wantKind:   doctorAuthUnparseable,
			wantDetail: "but unparseable:",
		},
		{
			name:       "empty mode",
			content:    ptr(`{}`),
			wantStatus: doctorWarn,
			wantKind:   doctorAuthUnparseable,
			wantDetail: "auth_mode=(empty) at ",
		},
		{
			name:       "unknown mode",
			content:    ptr(`{"auth_mode":"other"}`),
			wantStatus: doctorWarn,
			wantKind:   doctorAuthUnparseable,
			wantDetail: "auth_mode=other at ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			if tt.content != nil {
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte(*tt.content), 0o600))
			}
			dctx := &doctorContext{}

			row := checkCodexAuthJSON(context.Background(), dctx)

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, tt.wantKind, dctx.Auth.Kind)
			assert.Equal(t, filepath.Join(home, "auth.json"), dctx.Auth.Path)
			assert.Contains(t, row.Detail, tt.wantDetail)
			assert.NotContains(t, row.Detail, "secret")
		})
	}
}

func TestCheckCodexAuthEnv(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantStatus doctorStatus
		wantDetail string
	}{
		{
			name:       "unset",
			wantStatus: doctorInfo,
			wantDetail: "CODEX_API_KEY=unset OPENAI_API_KEY=unset CODEX_ACCESS_TOKEN=unset",
		},
		{
			name: "set values without echoing secrets",
			env: map[string]string{
				"CODEX_API_KEY":      "secret-codex",
				"OPENAI_API_KEY":     "secret-openai",
				"CODEX_ACCESS_TOKEN": "secret-token",
				"CODEX_AUTH_JSON":    "secret-json",
			},
			wantStatus: doctorOK,
			wantDetail: "CODEX_API_KEY=set OPENAI_API_KEY=set CODEX_ACCESS_TOKEN=set",
		},
		{
			name: "whitespace is unset",
			env: map[string]string{
				"CODEX_API_KEY":      " ",
				"OPENAI_API_KEY":     "\t",
				"CODEX_ACCESS_TOKEN": "\n",
			},
			wantStatus: doctorInfo,
			wantDetail: "CODEX_API_KEY=unset OPENAI_API_KEY=unset CODEX_ACCESS_TOKEN=unset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			row := checkCodexAuthEnv(context.Background(), &doctorContext{})

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, tt.wantDetail, row.Detail)
			assert.NotContains(t, row.Detail, "secret")
			assert.NotContains(t, row.Detail, "CODEX_AUTH_JSON")
		})
	}
}

func TestCheckCodexEffectiveAuth(t *testing.T) {
	tests := []struct {
		name       string
		auth       doctorAuthState
		env        doctorEnvState
		wantStatus doctorStatus
		wantAuth   string
	}{
		{
			name:       "codex api key wins",
			env:        doctorEnvState{CodexAPIKey: true, OpenAIAPIKey: true, CodexAccessToken: true},
			auth:       doctorAuthState{Kind: doctorAuthChatGPT},
			wantStatus: doctorOK,
			wantAuth:   "CODEX_API_KEY (OPENAI_API_KEY ignored because CODEX_API_KEY is set)",
		},
		{
			name:       "openai key shadowed by chatgpt auth json",
			env:        doctorEnvState{OpenAIAPIKey: true},
			auth:       doctorAuthState{Kind: doctorAuthChatGPT},
			wantStatus: doctorOK,
			wantAuth:   "ChatGPT subscription (OPENAI_API_KEY shadowed by auth.json gate)",
		},
		{
			name:       "openai key auto maps without auth json",
			env:        doctorEnvState{OpenAIAPIKey: true},
			auth:       doctorAuthState{Kind: doctorAuthAbsent},
			wantStatus: doctorOK,
			wantAuth:   "OPENAI_API_KEY auto-mapped to CODEX_API_KEY",
		},
		{
			name:       "openai auto map beats access token when auth json absent",
			env:        doctorEnvState{OpenAIAPIKey: true, CodexAccessToken: true},
			auth:       doctorAuthState{Kind: doctorAuthAbsent},
			wantStatus: doctorOK,
			wantAuth:   "OPENAI_API_KEY auto-mapped to CODEX_API_KEY",
		},
		{
			name:       "access token wins before auth json",
			env:        doctorEnvState{CodexAccessToken: true},
			auth:       doctorAuthState{Kind: doctorAuthChatGPT},
			wantStatus: doctorOK,
			wantAuth:   "CODEX_ACCESS_TOKEN",
		},
		{
			name:       "auth json apikey",
			auth:       doctorAuthState{Kind: doctorAuthAPIKey},
			wantStatus: doctorOK,
			wantAuth:   "auth.json auth_mode=apikey (per-token API key auth)",
		},
		{
			name:       "auth json chatgpt",
			auth:       doctorAuthState{Kind: doctorAuthChatGPT},
			wantStatus: doctorOK,
			wantAuth:   "ChatGPT subscription",
		},
		{
			name:       "unparseable auth json shadows openai",
			env:        doctorEnvState{OpenAIAPIKey: true},
			auth:       doctorAuthState{Kind: doctorAuthUnparseable},
			wantStatus: doctorWarn,
			wantAuth:   "auth.json unparseable (OPENAI_API_KEY shadowed by auth.json gate)",
		},
		{
			name:       "none",
			auth:       doctorAuthState{Kind: doctorAuthAbsent},
			wantStatus: doctorInfo,
			wantAuth:   "none detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dctx := &doctorContext{Auth: tt.auth, Env: tt.env}

			row := checkCodexEffectiveAuth(context.Background(), dctx)

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, tt.wantAuth, dctx.EffectiveAuth)
			assert.Equal(t, "Active path: "+tt.wantAuth, row.Detail)
		})
	}
}

func TestCheckCodexConfig(t *testing.T) {
	tests := []struct {
		name       string
		content    *string
		wantStatus doctorStatus
		wantDetail string
	}{
		{
			name:       "missing config",
			wantStatus: doctorInfo,
			wantDetail: "no .herdos.yml found; checks 1-5 still useful for ad-hoc codex usage",
		},
		{
			name:       "parse error",
			content:    ptr("{{invalid yaml"),
			wantStatus: doctorWarn,
			wantDetail: "parsing .herdos.yml",
		},
		{
			name:       "codex empty model",
			content:    ptr("version: 1\nagent:\n  provider: codex\n  model: \"\"\n"),
			wantStatus: doctorWarn,
			wantDetail: "agent.provider=codex but agent.model is empty; Codex will use its built-in default",
		},
		{
			name:       "codex model set",
			content:    ptr("version: 1\nagent:\n  provider: codex\n  model: gpt-5\n"),
			wantStatus: doctorOK,
			wantDetail: "agent.provider=codex agent.model=gpt-5",
		},
		{
			name:       "non codex provider",
			content:    ptr("version: 1\nagent:\n  provider: claude\n"),
			wantStatus: doctorInfo,
			wantDetail: "current is claude - checks 1-5 still useful for ad-hoc codex usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdir(t, dir)
			if tt.content != nil {
				require.NoError(t, os.WriteFile(filepath.Join(dir, config.ConfigFile), []byte(*tt.content), 0o644))
			}

			row := checkCodexConfig(context.Background(), &doctorContext{})

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Contains(t, row.Detail, tt.wantDetail)
		})
	}
}

func TestCheckStaleCodexAuthJSONEnv(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantStatus doctorStatus
		wantDetail string
	}{
		{name: "unset", wantStatus: doctorOK, wantDetail: "CODEX_AUTH_JSON unset"},
		{name: "whitespace unset", value: " ", wantStatus: doctorOK, wantDetail: "CODEX_AUTH_JSON unset"},
		{name: "set", value: "{}", wantStatus: doctorInfo, wantDetail: "CODEX_AUTH_JSON is no longer used since the #725 removal; safe to remove from your shell profile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_AUTH_JSON", tt.value)

			row := checkStaleCodexAuthJSONEnv(context.Background(), &doctorContext{})

			assert.Equal(t, tt.wantStatus, row.Status)
			assert.Equal(t, tt.wantDetail, row.Detail)
		})
	}
}

func TestRunCodexDoctorReturnsErrorForErrRows(t *testing.T) {
	orig := codexDoctorChecks
	t.Cleanup(func() { codexDoctorChecks = orig })
	codexDoctorChecks = []doctorCheck{
		func(context.Context, *doctorContext) doctorRow {
			return doctorRow{Status: doctorErr, Check: "broken", Detail: "failed"}
		},
		func(_ context.Context, dctx *doctorContext) doctorRow {
			dctx.EffectiveAuth = "none detected"
			return doctorRow{Status: doctorWarn, Check: "warn", Detail: "warning"}
		},
	}

	var out bytes.Buffer
	err := runCodexDoctor(context.Background(), &out)

	assert.ErrorIs(t, err, errCodexDoctorFailed)
	assert.Contains(t, out.String(), "STATUS  CHECK")
	assert.Contains(t, out.String(), "ERR     broken")
	assert.Contains(t, out.String(), "Active path: none detected")
	assert.False(t, strings.Contains(out.String(), "\n\n"))
}

func TestRunCodexDoctorReturnsNilWithoutErrRows(t *testing.T) {
	orig := codexDoctorChecks
	t.Cleanup(func() { codexDoctorChecks = orig })
	codexDoctorChecks = []doctorCheck{
		func(_ context.Context, dctx *doctorContext) doctorRow {
			dctx.EffectiveAuth = "ChatGPT subscription"
			return doctorRow{Status: doctorWarn, Check: "warn", Detail: "warning"}
		},
	}

	var out bytes.Buffer
	err := runCodexDoctor(context.Background(), &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "WARN    warn")
	assert.Contains(t, out.String(), "Active path: ChatGPT subscription")
}

func TestCodexDoctorCommandEndToEnd(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.ConfigFile), []byte("version: 1\nagent:\n  provider: codex\n  model: \"\"\n"), 0o644))

	binDir := t.TempDir()
	writeFakeDoctorCodex(t, binDir, "printf 'codex-cli fixture\\n'\nexit 0\n")
	t.Setenv("PATH", binDir)
	t.Setenv("OPENAI_API_KEY", "secret-openai")
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","access_token":"secret-token"}`), 0o600))

	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"codex", "doctor"})
	err := root.Execute()

	require.NoError(t, err)
	output := out.String()
	assert.Contains(t, output, "STATUS  CHECK")
	assert.Contains(t, output, "Codex binary")
	assert.Contains(t, output, "Codex version")
	assert.Contains(t, output, "auth.json")
	assert.Contains(t, output, "Auth env")
	assert.Contains(t, output, "Effective auth")
	assert.Contains(t, output, "Herd config")
	assert.Contains(t, output, "CODEX_AUTH_JSON")
	assert.Contains(t, output, "OPENAI_API_KEY shadowed by auth.json gate")
	assert.Contains(t, output, "Active path: ChatGPT subscription (OPENAI_API_KEY shadowed by auth.json gate)")
	assert.NotContains(t, output, "secret-openai")
	assert.NotContains(t, output, "secret-token")
}

func TestCodexDoctorCommandReturnsErrorForErrRows(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"codex", "doctor"})
	err := root.Execute()

	assert.ErrorIs(t, err, errCodexDoctorFailed)
	assert.True(t, errors.Is(err, errCodexDoctorFailed))
	assert.Contains(t, out.String(), "not found in PATH=")
}

func writeFakeDoctorCodex(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(old))
	})
}

func ptr(s string) *string {
	return &s
}
