package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_AgentExec(t *testing.T) {
	tests := []struct {
		name      string
		exec      string
		wantError bool
		errSubstr string
	}{
		{"empty is valid", "", false, ""},
		{"local is valid", "local", false, ""},
		{"docker is valid", "docker", false, ""},
		{"bogus is invalid", "bogus", true, "agent.exec must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.Exec = tt.exec

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentExecImageFreeForm(t *testing.T) {
	cfg := Default()
	cfg.Platform.Owner = "org"
	cfg.Platform.Repo = "repo"
	cfg.Agent.ExecImage = "example/foo:bar"

	assert.Nil(t, Validate(cfg))
}

func TestValidate_IntegratorCIWorkflows(t *testing.T) {
	tests := []struct {
		name      string
		workflows []string
		wantError bool
		errSubstr string
	}{
		{"missing is valid", nil, false, ""},
		{"empty list is valid", []string{}, false, ""},
		{"one valid workflow is valid", []string{"CI"}, false, ""},
		{"multiple valid workflows are valid", []string{"CI - ServiceKit Ruby", "CI — Accounts"}, false, ""},
		{"blank entry is invalid", []string{""}, true, "integrator.ci_workflows[0] must not be blank"},
		{"whitespace-only entry is invalid", []string{" \t\n"}, true, "integrator.ci_workflows[0] must not be blank"},
		{"duplicate entry is invalid", []string{"CI", "CI"}, true, "integrator.ci_workflows[1] duplicates workflow name \"CI\""},
		{"ascii hyphen and unicode dash are distinct", []string{"CI - Accounts", "CI — Accounts"}, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Integrator.CIWorkflows = tt.workflows

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_ImagePublishRunsOn(t *testing.T) {
	tests := []struct {
		name       string
		runsOn     []string
		useDefault bool
		wantError  bool
		errSubstr  string
	}{
		{"default valid", nil, true, false, ""},
		{"nil list invalid", nil, false, true, "image_publish.runs_on must contain at least one runner label"},
		{"empty list invalid", []string{}, false, true, "image_publish.runs_on must contain at least one runner label"},
		{"one empty string invalid", []string{""}, false, true, "image_publish.runs_on[0] must be a non-empty label"},
		{"one whitespace-only string invalid", []string{" \t\n"}, false, true, "image_publish.runs_on[0] must be a non-empty label"},
		{"duplicate labels invalid", []string{"self-hosted", "self-hosted"}, false, true, `image_publish.runs_on[1] duplicates image_publish.runs_on[0] ("self-hosted")`},
		{"explicit ubuntu latest valid", []string{"ubuntu-latest"}, false, false, ""},
		{"explicit self-hosted multi-label valid", []string{"self-hosted", "herd-publisher"}, false, false, ""},
		{"explicit quoted labels valid", []string{"self-hosted", "linux x64", "gpu:large"}, false, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			if !tt.useDefault {
				cfg.ImagePublish.RunsOn = tt.runsOn
			}

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_ImagePublishPlatforms(t *testing.T) {
	tests := []struct {
		name       string
		platforms  []string
		useDefault bool
		wantError  bool
		errSubstr  string
	}{
		{"default valid", nil, true, false, ""},
		{"nil list invalid", nil, false, true, "image_publish.platforms must contain at least one platform"},
		{"empty list invalid", []string{}, false, true, "image_publish.platforms must contain at least one platform"},
		{"linux amd64 valid", []string{"linux/amd64"}, false, false, ""},
		{"linux arm64 valid", []string{"linux/arm64"}, false, false, ""},
		{"both supported platforms valid", []string{"linux/amd64", "linux/arm64"}, false, false, ""},
		{"unsupported platform invalid", []string{"linux/s390x"}, false, true, `image_publish.platforms[0] must be one of: linux/amd64, linux/arm64 — got "linux/s390x"`},
		{"empty string invalid", []string{""}, false, true, `image_publish.platforms[0] must be one of: linux/amd64, linux/arm64 — got ""`},
		{"whitespace-only string invalid", []string{" \t\n"}, false, true, "image_publish.platforms[0] must be one of: linux/amd64, linux/arm64"},
		{"duplicate linux amd64 invalid", []string{"linux/amd64", "linux/amd64"}, false, true, `image_publish.platforms[1] duplicates image_publish.platforms[0] ("linux/amd64")`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			if !tt.useDefault {
				cfg.ImagePublish.Platforms = tt.platforms
			}

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_ImagePublishBuildSecrets(t *testing.T) {
	tests := []struct {
		name         string
		buildSecrets []string
		wantError    bool
		errSubstr    string
	}{
		{"nil list valid", nil, false, ""},
		{"empty list valid", []string{}, false, ""},
		{"valid examples", []string{"BUNDLE_RUBYGEMS__PKG__GITHUB__COM", "NPM_TOKEN", "GIT_AUTH_TOKEN"}, false, ""},
		{"empty string invalid", []string{""}, true, "image_publish.build_secrets[0] must be a non-empty secret/env name"},
		{"whitespace string invalid", []string{" \t\n"}, true, "image_publish.build_secrets[0] must be a non-empty secret/env name"},
		{"starts with digit invalid", []string{"1TOKEN"}, true, `image_publish.build_secrets[0] must match ^[A-Za-z_][A-Za-z0-9_]*$ — got "1TOKEN"`},
		{"hyphen invalid", []string{"TOKEN-NAME"}, true, `image_publish.build_secrets[0] must match ^[A-Za-z_][A-Za-z0-9_]*$ — got "TOKEN-NAME"`},
		{"dot invalid", []string{"TOKEN.NAME"}, true, `image_publish.build_secrets[0] must match ^[A-Za-z_][A-Za-z0-9_]*$ — got "TOKEN.NAME"`},
		{"duplicate configured names invalid", []string{"NPM_TOKEN", "NPM_TOKEN"}, true, `image_publish.build_secrets[1] duplicates image_publish.build_secrets[0] ("NPM_TOKEN")`},
		{"duplicate normalized ids invalid", []string{"FOO__BAR", "FOO_BAR"}, true, `image_publish.build_secrets[1] normalizes to duplicate BuildKit secret id "foo_bar" from image_publish.build_secrets[0]`},
		{"empty normalized id invalid", []string{"_"}, true, "image_publish.build_secrets[0] normalizes to empty BuildKit secret id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.ImagePublish.BuildSecrets = tt.buildSecrets

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestBuildSecretID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"npm token", "NPM_TOKEN", "npm_token"},
		{"git auth token", "GIT_AUTH_TOKEN", "git_auth_token"},
		{"bundle rubygems package github", "BUNDLE_RUBYGEMS__PKG__GITHUB__COM", "bundle_rubygems_pkg_github_com"},
		{"trims separators", "__NPM_TOKEN__", "npm_token"},
		{"collapses separator runs", "FOO---BAR...BAZ", "foo_bar_baz"},
		{"ascii only lowercasing", "Ä_TOKEN", "token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BuildSecretID(tt.in))
		})
	}
}

func TestBuildSecretIDs(t *testing.T) {
	tests := []struct {
		name      string
		in        []string
		want      []string
		wantError string
	}{
		{"preserves order", []string{"NPM_TOKEN", "GIT_AUTH_TOKEN"}, []string{"npm_token", "git_auth_token"}, ""},
		{"empty normalized id invalid", []string{"_"}, nil, `image_publish.build_secrets[0] ("_") normalizes to empty BuildKit secret id`},
		{"duplicate normalized id invalid", []string{"FOO__BAR", "FOO_BAR"}, nil, `image_publish.build_secrets[1] ("FOO_BAR") normalizes to duplicate BuildKit secret id "foo_bar" from image_publish.build_secrets[0] ("FOO__BAR")`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildSecretIDs(tt.in)
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidate_AgentProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		wantError bool
		errSubstr string
	}{
		{"claude is valid", "claude", false, ""},
		{"opencode is valid", "opencode", false, ""},
		{"codex is valid", "codex", false, ""},
		{"case mismatch is invalid", "codeX", true, "agent.provider must be one of: claude, opencode, codex"},
		{"empty is invalid", "", true, "agent.provider must be one of: claude, opencode, codex"},
		{"unknown provider is invalid", "gpt", true, "agent.provider must be one of: claude, opencode, codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.Provider = tt.provider

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleProviders(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "base empty provider is invalid",
			modify: func(cfg *Config) {
				cfg.Agent.Provider = ""
			},
			wantError: true,
			errSubstr: "agent.provider must be one of: claude, opencode, codex",
		},
		{
			name: "planner sparse provider is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers sparse provider is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{Model: "worker-model"}
			},
		},
		{
			name: "planner invalid provider has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{Provider: "gpt"}
			},
			wantError: true,
			errSubstr: "agent.planner.provider must be one of: claude, opencode, codex",
		},
		{
			name: "workers invalid provider has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{Provider: "gpt"}
			},
			wantError: true,
			errSubstr: "agent.workers.provider must be one of: claude, opencode, codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentCodexReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		effort    string
		wantError bool
		errSubstr string
	}{
		{"empty is valid", "", false, ""},
		{"minimal is valid", "minimal", false, ""},
		{"low is valid", "low", false, ""},
		{"medium is valid", "medium", false, ""},
		{"high is valid", "high", false, ""},
		{"unknown is invalid", "extreme", true, "agent.codex_reasoning_effort must be one of: minimal, low, medium, high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.CodexReasoningEffort = tt.effort

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleCodexReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "planner empty effort is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers high effort is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexReasoningEffort: "high"}
			},
		},
		{
			name: "planner invalid effort has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{CodexReasoningEffort: "extreme"}
			},
			wantError: true,
			errSubstr: "agent.planner.codex_reasoning_effort must be one of: minimal, low, medium, high",
		},
		{
			name: "workers invalid effort has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexReasoningEffort: "extreme"}
			},
			wantError: true,
			errSubstr: "agent.workers.codex_reasoning_effort must be one of: minimal, low, medium, high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentCodexSandbox(t *testing.T) {
	tests := []struct {
		name      string
		sandbox   string
		wantError bool
		errSubstr string
	}{
		{"empty is valid (uses Codex default)", "", false, ""},
		{"read-only is valid", "read-only", false, ""},
		{"workspace-write is valid", "workspace-write", false, ""},
		{"danger-full-access is valid (container workers)", "danger-full-access", false, ""},
		{"unknown is invalid", "off", true, "agent.codex_sandbox must be one of: read-only, workspace-write, danger-full-access"},
		{"typo'd dangerous mode is invalid", "danger", true, "agent.codex_sandbox must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			cfg.Agent.CodexSandbox = tt.sandbox

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestValidate_AgentRoleCodexSandbox(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*Config)
		wantError bool
		errSubstr string
	}{
		{
			name: "planner empty sandbox is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{}
			},
		},
		{
			name: "workers explicit read-only is valid",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexSandbox: "read-only"}
			},
		},
		{
			name: "planner invalid sandbox has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Planner = &AgentRole{CodexSandbox: "off"}
			},
			wantError: true,
			errSubstr: `agent.planner.codex_sandbox must be one of: read-only, workspace-write, danger-full-access (or empty) — got "off"`,
		},
		{
			name: "workers invalid sandbox has scoped path",
			modify: func(cfg *Config) {
				cfg.Agent.Workers = &AgentRole{CodexSandbox: "off"}
			},
			wantError: true,
			errSubstr: `agent.workers.codex_sandbox must be one of: read-only, workspace-write, danger-full-access (or empty) — got "off"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Platform.Owner = "org"
			cfg.Platform.Repo = "repo"
			tt.modify(cfg)

			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), tt.errSubstr)
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}
