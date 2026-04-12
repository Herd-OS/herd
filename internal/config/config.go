package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

const ConfigFile = ".herdos.yml"

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

type Agent struct {
	Provider string `yaml:"provider"`
	Binary   string `yaml:"binary"`
	Model    string `yaml:"model"`
	MaxTurns int    `yaml:"max_turns"` // Max agentic turns for headless mode (0 = agent default)
}

type Workers struct {
	MaxConcurrent  int    `yaml:"max_concurrent"`
	RunnerLabel    string `yaml:"runner_label"`
	TimeoutMinutes int    `yaml:"timeout_minutes"`
}

type Integrator struct {
	Strategy                       string `yaml:"strategy"`
	OnConflict                     string `yaml:"on_conflict"`
	MaxConflictResolutionAttempts  int    `yaml:"max_conflict_resolution_attempts"`
	RequireCI                      bool   `yaml:"require_ci"`
	Review                         bool   `yaml:"review"`
	ReviewMaxFixCycles             int    `yaml:"review_max_fix_cycles"`
	ReviewStrictness               string `yaml:"review_strictness"`    // "standard", "strict", "lenient"
	ReviewFixSeverity              string `yaml:"review_fix_severity"`  // minimum severity to fix: "high", "medium", "low" (default: "medium")
	CIMaxFixCycles                 int    `yaml:"ci_max_fix_cycles"`
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
