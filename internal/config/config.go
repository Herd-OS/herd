package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

const ConfigFile = ".herdos.yml"

const (
	AgentRolePlanner = "planner"
	AgentRoleWorkers = "workers"
)

type Config struct {
	Version      int          `yaml:"version"`
	Platform     Platform     `yaml:"platform"`
	Agent        Agent        `yaml:"agent"`
	Workers      Workers      `yaml:"workers"`
	Integrator   Integrator   `yaml:"integrator"`
	Monitor      Monitor      `yaml:"monitor"`
	PullRequests PullRequests `yaml:"pull_requests"`
}

type Platform struct {
	Provider string `yaml:"provider"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
}

type AgentRole struct {
	Provider string `yaml:"provider,omitempty"`
	Binary   string `yaml:"binary,omitempty"`
	Model    string `yaml:"model,omitempty"`
	MaxTurns int    `yaml:"max_turns,omitempty"`
	// CodexReasoningEffort controls the Codex provider's reasoning depth.
	// One of minimal|low|medium|high (Codex provider only; default medium).
	// Maps to `-c model_reasoning_effort=<value>` on every Codex invocation.
	CodexReasoningEffort string `yaml:"codex_reasoning_effort,omitempty"`
	// CodexSandbox selects the Codex CLI sandbox policy. Empty uses Codex's
	// own default (workspace-write). One of read-only|workspace-write|
	// danger-full-access. Maps to `--sandbox <value>` on every Codex
	// invocation.
	CodexSandbox string `yaml:"codex_sandbox,omitempty"`
}

type Agent struct {
	AgentRole `yaml:",inline"`

	Exec      string `yaml:"exec,omitempty"`       // local | docker (empty = local). Where `herd plan` runs the agent.
	ExecImage string `yaml:"exec_image,omitempty"` // Override image for exec=docker (empty = default ghcr.io/herd-os/herd-runner-base:<version>)

	Planner *AgentRole `yaml:"planner,omitempty"`
	Workers *AgentRole `yaml:"workers,omitempty"`
}

// Resolve returns the effective agent configuration for role.
func (a *Agent) Resolve(role string) AgentRole {
	resolved := a.AgentRole

	switch role {
	case AgentRolePlanner:
		overlayAgentRole(&resolved, a.Planner)
	case AgentRoleWorkers:
		overlayAgentRole(&resolved, a.Workers)
	default:
		return resolved
	}

	if resolved.Provider == "codex" && resolved.CodexSandbox == "" {
		switch role {
		case AgentRolePlanner:
			if a.Exec == "docker" {
				resolved.CodexSandbox = "danger-full-access"
			}
		case AgentRoleWorkers:
			resolved.CodexSandbox = "danger-full-access"
		}
	}

	return resolved
}

// ResolveOrDefault returns the effective agent configuration for a known role.
func (a *Agent) ResolveOrDefault(role string) (AgentRole, error) {
	switch role {
	case AgentRolePlanner, AgentRoleWorkers:
		return a.Resolve(role), nil
	default:
		return AgentRole{}, fmt.Errorf("unknown agent role %q (supported: planner, workers)", role)
	}
}

func overlayAgentRole(base *AgentRole, override *AgentRole) {
	if override == nil {
		return
	}
	if override.Provider != "" {
		base.Provider = override.Provider
	}
	if override.Binary != "" {
		base.Binary = override.Binary
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.MaxTurns != 0 {
		base.MaxTurns = override.MaxTurns
	}
	if override.CodexReasoningEffort != "" {
		base.CodexReasoningEffort = override.CodexReasoningEffort
	}
	if override.CodexSandbox != "" {
		base.CodexSandbox = override.CodexSandbox
	}
}

type Workers struct {
	MaxConcurrent           int      `yaml:"max_concurrent"`
	RunnerLabel             string   `yaml:"runner_label"`
	TimeoutMinutes          int      `yaml:"timeout_minutes"`
	ProgressIntervalSeconds int      `yaml:"progress_interval_seconds"` // how often to post progress updates (0 = disabled)
	ExtraEnv                []string `yaml:"extra_env"`                 // GitHub Actions secret names to pass through to workers
}

type Integrator struct {
	Strategy                      string `yaml:"strategy"`
	OnConflict                    string `yaml:"on_conflict"`
	MaxConflictResolutionAttempts int    `yaml:"max_conflict_resolution_attempts"`
	RequireCI                     bool   `yaml:"require_ci"`
	Review                        bool   `yaml:"review"`
	ReviewMaxFixCycles            int    `yaml:"review_max_fix_cycles"`
	ReviewStrictness              string `yaml:"review_strictness"`   // "standard", "strict", "lenient"
	ReviewFixSeverity             string `yaml:"review_fix_severity"` // minimum severity to fix: "high", "medium", "low" (default: "medium")
	CIMaxFixCycles                int    `yaml:"ci_max_fix_cycles"`
}

type Monitor struct {
	PatrolIntervalMinutes int      `yaml:"patrol_interval_minutes"`
	StaleThresholdMinutes int      `yaml:"stale_threshold_minutes"`
	MaxPRHAgeHours        int      `yaml:"max_pr_age_hours"`
	AutoRedispatch        bool     `yaml:"auto_redispatch"`
	MaxRedispatchAttempts int      `yaml:"max_redispatch_attempts"`
	NotifyOnFailure       bool     `yaml:"notify_on_failure"`
	NotifyUsers           []string `yaml:"notify_users"`
}

type PullRequests struct {
	AutoMerge     bool   `yaml:"auto_merge"`
	CoAuthorEmail string `yaml:"co_author_email"` // e.g. "123456+herd-os[bot]@users.noreply.github.com"
}

// Load reads and parses .herdos.yml from the given directory.
func Load(dir string) (*Config, error) {
	path := dir + "/" + ConfigFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s found in %s (run 'herd init' first)", ConfigFile, dir)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ConfigFile, err)
	}

	applyEnvOverrides(cfg)

	return cfg, nil
}

// Save writes the config to .herdos.yml in the given directory.
func Save(dir string, cfg *Config) error {
	path := dir + "/" + ConfigFile
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("HERD_MAX_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workers.MaxConcurrent = n
		}
	}
	if v := os.Getenv("HERD_RUNNER_LABEL"); v != "" {
		cfg.Workers.RunnerLabel = v
	}
	if v := os.Getenv("HERD_MODEL"); v != "" {
		cfg.Agent.Model = v
	}
	if v := os.Getenv("HERD_REVIEW_STRICTNESS"); v != "" {
		cfg.Integrator.ReviewStrictness = v
	}
	if v := os.Getenv("HERD_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workers.TimeoutMinutes = n
		}
	}
}
