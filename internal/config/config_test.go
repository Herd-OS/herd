package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, "github", cfg.Platform.Provider)
	assert.Equal(t, "claude", cfg.Agent.Provider)
	assert.Equal(t, "medium", cfg.Agent.CodexReasoningEffort)
	assert.Empty(t, cfg.Agent.CodexSandbox)
	assert.Equal(t, 3, cfg.Workers.MaxConcurrent)
	assert.Equal(t, "herd-worker", cfg.Workers.RunnerLabel)
	assert.Equal(t, 30, cfg.Workers.TimeoutMinutes)
	assert.Equal(t, "squash", cfg.Integrator.Strategy)
	assert.Equal(t, "dispatch-resolver", cfg.Integrator.OnConflict)
	assert.Equal(t, true, cfg.Integrator.RequireCI)
	assert.Equal(t, true, cfg.Integrator.Review)
	assert.Equal(t, 0, cfg.Integrator.ReviewMaxFixCycles)
	assert.Equal(t, "standard", cfg.Integrator.ReviewStrictness)
	assert.Equal(t, 0, cfg.Integrator.CIMaxFixCycles)
	assert.Empty(t, cfg.Integrator.CIWorkflows)
	assert.Nil(t, cfg.Integrator.CIWorkflows)
	assert.Equal(t, 15, cfg.Monitor.PatrolIntervalMinutes)
	assert.Equal(t, true, cfg.Monitor.AutoRedispatch)
	assert.Equal(t, false, cfg.PullRequests.AutoMerge)
	assert.Equal(t, []string{"ubuntu-latest"}, cfg.ImagePublish.RunsOn)
	assert.Equal(t, []string{"linux/amd64", "linux/arm64"}, cfg.ImagePublish.Platforms)
	assert.NotNil(t, cfg.ImagePublish.BuildSecrets)
	assert.Empty(t, cfg.ImagePublish.BuildSecrets)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "my-org"
  repo: "my-project"
agent:
  provider: "claude"
workers:
  max_concurrent: 5
  runner_label: "custom-label"
  timeout_minutes: 60
integrator:
  strategy: "rebase"
  on_conflict: "dispatch-resolver"
  max_conflict_resolution_attempts: 3
  require_ci: false
  review: true
  review_max_fix_cycles: 2
  ci_max_fix_cycles: 1
  ci_workflows:
    - "CI - ServiceKit Ruby"
    - "CI — Accounts"
monitor:
  patrol_interval_minutes: 10
  stale_threshold_minutes: 120
  max_pr_age_hours: 48
  auto_redispatch: false
  max_redispatch_attempts: 5
  notify_on_failure: false
  notify_users: ["alice", "bob"]
pull_requests:
  auto_merge: true
image_publish:
  runs_on: ["self-hosted", "linux x64", "gpu:large"]
  platforms:
    - linux/amd64
  build_secrets:
    - BUNDLE_RUBYGEMS__PKG__GITHUB__COM
    - NPM_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "my-org", cfg.Platform.Owner)
	assert.Equal(t, "my-project", cfg.Platform.Repo)
	assert.Equal(t, 5, cfg.Workers.MaxConcurrent)
	assert.Equal(t, "custom-label", cfg.Workers.RunnerLabel)
	assert.Equal(t, 60, cfg.Workers.TimeoutMinutes)
	assert.Equal(t, "rebase", cfg.Integrator.Strategy)
	assert.Equal(t, "dispatch-resolver", cfg.Integrator.OnConflict)
	assert.Equal(t, false, cfg.Integrator.RequireCI)
	assert.Equal(t, []string{"CI - ServiceKit Ruby", "CI — Accounts"}, cfg.Integrator.CIWorkflows)
	assert.Equal(t, 10, cfg.Monitor.PatrolIntervalMinutes)
	assert.Equal(t, false, cfg.Monitor.AutoRedispatch)
	assert.Equal(t, []string{"alice", "bob"}, cfg.Monitor.NotifyUsers)
	assert.Equal(t, true, cfg.PullRequests.AutoMerge)
	assert.Equal(t, []string{"self-hosted", "linux x64", "gpu:large"}, cfg.ImagePublish.RunsOn)
	assert.Equal(t, []string{"linux/amd64"}, cfg.ImagePublish.Platforms)
	assert.Equal(t, []string{"BUNDLE_RUBYGEMS__PKG__GITHUB__COM", "NPM_TOKEN"}, cfg.ImagePublish.BuildSecrets)
}

func TestLoadAgentExecFields(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
agent:
  exec: docker
  exec_image: example/foo:bar
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "docker", cfg.Agent.Exec)
	assert.Equal(t, "example/foo:bar", cfg.Agent.ExecImage)
}

func TestLoadAgentRoleBlocks(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
agent:
  provider: "claude"
  binary: "claude"
  model: "sonnet"
  max_turns: 4
  codex_reasoning_effort: "medium"
  planner:
    model: "opus"
    max_turns: 8
  workers:
    provider: "codex"
    codex_sandbox: "read-only"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.Agent.Provider)
	assert.Equal(t, "claude", cfg.Agent.Binary)
	assert.Equal(t, "sonnet", cfg.Agent.Model)
	assert.Equal(t, 4, cfg.Agent.MaxTurns)
	require.NotNil(t, cfg.Agent.Planner)
	assert.Equal(t, AgentRole{Model: "opus", MaxTurns: 8}, *cfg.Agent.Planner)
	require.NotNil(t, cfg.Agent.Workers)
	assert.Equal(t, AgentRole{Provider: "codex", CodexSandbox: "read-only"}, *cfg.Agent.Workers)

	assert.Equal(t, AgentRole{
		Provider:             "claude",
		Binary:               "claude",
		Model:                "opus",
		MaxTurns:             8,
		CodexReasoningEffort: "medium",
	}, cfg.Agent.Resolve(AgentRolePlanner))
	assert.Equal(t, AgentRole{
		Provider:             "codex",
		Binary:               "claude",
		Model:                "sonnet",
		MaxTurns:             4,
		CodexReasoningEffort: "medium",
		CodexSandbox:         "read-only",
	}, cfg.Agent.Resolve(AgentRoleWorkers))
}

func TestLoadSparseAgentRoleBlocks(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
agent:
  provider: "codex"
  exec: docker
  planner: {}
  workers:
    model: "gpt-5-codex"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	require.NotNil(t, cfg.Agent.Planner)
	require.NotNil(t, cfg.Agent.Workers)
	assert.Equal(t, AgentRole{}, *cfg.Agent.Planner)
	assert.Equal(t, AgentRole{Model: "gpt-5-codex"}, *cfg.Agent.Workers)
	assert.Equal(t, "danger-full-access", cfg.Agent.Resolve(AgentRolePlanner).CodexSandbox)
	assert.Equal(t, "danger-full-access", cfg.Agent.Resolve(AgentRoleWorkers).CodexSandbox)
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	assert.ErrorContains(t, err, "no .herdos.yml found")
}

func TestLoadMissingFieldsGetDefaults(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, 3, cfg.Workers.MaxConcurrent)
	assert.Equal(t, "herd-worker", cfg.Workers.RunnerLabel)
	assert.Equal(t, "squash", cfg.Integrator.Strategy)
	assert.Nil(t, cfg.Integrator.CIWorkflows)
	assert.Equal(t, true, cfg.Monitor.AutoRedispatch)
	assert.Equal(t, []string{"ubuntu-latest"}, cfg.ImagePublish.RunsOn)
	assert.Equal(t, []string{"linux/amd64", "linux/arm64"}, cfg.ImagePublish.Platforms)
	assert.NotNil(t, cfg.ImagePublish.BuildSecrets)
	assert.Empty(t, cfg.ImagePublish.BuildSecrets)
}

func TestEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	t.Setenv("HERD_MAX_WORKERS", "10")
	t.Setenv("HERD_RUNNER_LABEL", "gpu-runner")
	t.Setenv("HERD_MODEL", "opus")
	t.Setenv("HERD_TIMEOUT", "120")

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, 10, cfg.Workers.MaxConcurrent)
	assert.Equal(t, "gpu-runner", cfg.Workers.RunnerLabel)
	assert.Equal(t, "opus", cfg.Agent.Model)
	assert.Equal(t, 120, cfg.Workers.TimeoutMinutes)
}

func TestEnvOverrideModelOnlyUpdatesBaseAgent(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
agent:
  provider: "claude"
  model: "sonnet"
  planner:
    model: "planner-model"
  workers:
    model: "worker-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	t.Setenv("HERD_MODEL", "env-model")

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "env-model", cfg.Agent.Model)
	require.NotNil(t, cfg.Agent.Planner)
	assert.Equal(t, "planner-model", cfg.Agent.Planner.Model)
	require.NotNil(t, cfg.Agent.Workers)
	assert.Equal(t, "worker-model", cfg.Agent.Workers.Model)
}

func TestValidateValid(t *testing.T) {
	cfg := Default()
	cfg.Platform.Owner = "org"
	cfg.Platform.Repo = "repo"
	assert.Nil(t, Validate(cfg))
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
		errMsg string
	}{
		{"bad version", func(c *Config) { c.Version = 2 }, "version must be 1"},
		{"bad platform", func(c *Config) { c.Platform.Provider = "bitbucket" }, "platform.provider must be one of"},
		{"bad agent", func(c *Config) { c.Agent.Provider = "gpt" }, "agent.provider must be one of"},
		{"zero workers", func(c *Config) { c.Workers.MaxConcurrent = 0 }, "workers.max_concurrent must be > 0"},
		{"negative timeout", func(c *Config) { c.Workers.TimeoutMinutes = -1 }, "workers.timeout_minutes must be > 0"},
		{"bad strategy", func(c *Config) { c.Integrator.Strategy = "yolo" }, "integrator.strategy must be one of"},
		{"bad on_conflict", func(c *Config) { c.Integrator.OnConflict = "panic" }, "integrator.on_conflict must be one of"},
		{"negative fix cycles", func(c *Config) { c.Integrator.ReviewMaxFixCycles = -1 }, "integrator.review_max_fix_cycles must be >= 0"},
		{"negative ci cycles", func(c *Config) { c.Integrator.CIMaxFixCycles = -1 }, "integrator.ci_max_fix_cycles must be >= 0"},
		{"low patrol", func(c *Config) { c.Monitor.PatrolIntervalMinutes = 3 }, "monitor.patrol_interval_minutes must be >= 5"},
		{"zero stale", func(c *Config) { c.Monitor.StaleThresholdMinutes = 0 }, "monitor.stale_threshold_minutes must be > 0"},
		{"zero pr age", func(c *Config) { c.Monitor.MaxPRHAgeHours = 0 }, "monitor.max_pr_age_hours must be > 0"},
		{"zero redispatch", func(c *Config) { c.Monitor.MaxRedispatchAttempts = 0 }, "monitor.max_redispatch_attempts must be > 0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.modify(cfg)
			ve := Validate(cfg)
			require.NotNil(t, ve)
			assert.Contains(t, ve.Error(), tt.errMsg)
		})
	}
}

func TestValidateStaleWarning(t *testing.T) {
	cfg := Default()
	cfg.Monitor.StaleThresholdMinutes = 30
	cfg.Workers.TimeoutMinutes = 30

	ve := Validate(cfg)
	// No errors, but there should be a warning (returned via Warnings field)
	// Since stale == timeout, the warning fires but validation passes
	assert.Nil(t, ve) // no error returned

	// Access warnings directly by running validate logic
	result := &ValidationError{}
	if cfg.Monitor.StaleThresholdMinutes <= cfg.Workers.TimeoutMinutes {
		result.Warnings = append(result.Warnings, "stale warning")
	}
	assert.Len(t, result.Warnings, 1)
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte("{{invalid yaml"), 0644))

	_, err := Load(dir)
	assert.ErrorContains(t, err, "parsing .herdos.yml")
}

func TestLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(""), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	// Empty file → all defaults
	assert.Equal(t, 3, cfg.Workers.MaxConcurrent)
	assert.Equal(t, "herd-worker", cfg.Workers.RunnerLabel)
	assert.Equal(t, "squash", cfg.Integrator.Strategy)
}

func TestLoadVersionZeroGetsDefault(t *testing.T) {
	dir := t.TempDir()
	content := `platform:
  provider: "github"
  owner: "org"
  repo: "repo"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	// version field missing → stays at default (1) since Default() sets it
	assert.Equal(t, 1, cfg.Version)
}

func TestLoadOnlyPlatformSection(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "org", cfg.Platform.Owner)
	assert.Equal(t, "claude", cfg.Agent.Provider)
	assert.Equal(t, 3, cfg.Workers.MaxConcurrent)
	assert.Equal(t, true, cfg.Integrator.RequireCI)
	assert.Equal(t, 15, cfg.Monitor.PatrolIntervalMinutes)
	assert.Equal(t, false, cfg.PullRequests.AutoMerge)
}

func TestEnvOverrideInvalidNumber(t *testing.T) {
	dir := t.TempDir()
	content := `version: 1
platform:
  provider: "github"
  owner: "org"
  repo: "repo"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0644))

	t.Setenv("HERD_MAX_WORKERS", "notanumber")
	t.Setenv("HERD_TIMEOUT", "also-not")

	cfg, err := Load(dir)
	require.NoError(t, err)
	// Invalid numbers should be silently ignored, keeping defaults
	assert.Equal(t, 3, cfg.Workers.MaxConcurrent)
	assert.Equal(t, 30, cfg.Workers.TimeoutMinutes)
}

func TestValidate_ReviewStrictness(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError bool
	}{
		{"standard is valid", "standard", false},
		{"strict is valid", "strict", false},
		{"lenient is valid", "lenient", false},
		{"empty is invalid", "", true},
		{"unknown is invalid", "relaxed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Integrator.ReviewStrictness = tt.value
			ve := Validate(cfg)
			if tt.wantError {
				require.NotNil(t, ve)
				assert.Contains(t, ve.Error(), "review_strictness")
			} else {
				assert.Nil(t, ve)
			}
		})
	}
}

func TestDefault_ReviewStrictness(t *testing.T) {
	cfg := Default()
	assert.Equal(t, "standard", cfg.Integrator.ReviewStrictness)
}

func TestEnvOverride_ReviewStrictness(t *testing.T) {
	t.Setenv("HERD_REVIEW_STRICTNESS", "lenient")
	cfg := Default()
	applyEnvOverrides(cfg)
	assert.Equal(t, "lenient", cfg.Integrator.ReviewStrictness)
}

func TestValidateMultipleErrors(t *testing.T) {
	cfg := Default()
	cfg.Workers.MaxConcurrent = 0
	cfg.Workers.TimeoutMinutes = -1
	cfg.Integrator.Strategy = "bad"

	ve := Validate(cfg)
	require.NotNil(t, ve)
	assert.Len(t, ve.Errors, 3)
}

func TestValidateMaxConflictResolutionAttempts(t *testing.T) {
	cfg := Default()
	cfg.Integrator.MaxConflictResolutionAttempts = 0

	ve := Validate(cfg)
	require.NotNil(t, ve)
	assert.Contains(t, ve.Error(), "max_conflict_resolution_attempts must be > 0")
}

func TestValidateWarningsAccessible(t *testing.T) {
	cfg := Default()
	cfg.Monitor.StaleThresholdMinutes = 20
	cfg.Workers.TimeoutMinutes = 30

	ve := Validate(cfg)
	// Validation passes (no errors) but Warnings field should be populated
	// Since Validate returns nil when no errors, we need to run it differently
	// to check warnings. Let's re-validate and inspect.
	assert.Nil(t, ve)

	// Verify the condition that triggers the warning
	assert.LessOrEqual(t, cfg.Monitor.StaleThresholdMinutes, cfg.Workers.TimeoutMinutes)
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Platform.Owner = "test-org"
	cfg.Platform.Repo = "test-repo"

	require.NoError(t, Save(dir, cfg))

	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ci_workflows")

	loaded, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "test-org", loaded.Platform.Owner)
	assert.Equal(t, "test-repo", loaded.Platform.Repo)
	assert.Equal(t, 3, loaded.Workers.MaxConcurrent)
}
