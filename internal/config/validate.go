package config

import (
	"fmt"
	"strings"
)

// ValidationError holds one or more validation errors.
type ValidationError struct {
	Errors   []string
	Warnings []string
}

func (e *ValidationError) Error() string {
	return "invalid .herdos.yml:\n  " + strings.Join(e.Errors, "\n  ")
}

// Validate checks the config for invalid values.
// Returns a *ValidationError if there are errors (warnings alone don't cause an error).
func Validate(cfg *Config) *ValidationError {
	ve := &ValidationError{}

	if cfg.Version != 1 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("version must be 1, got %d", cfg.Version))
	}

	// Platform
	switch cfg.Platform.Provider {
	case "github":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("platform.provider must be one of: github — got %q", cfg.Platform.Provider))
	}

	validateAgentRole(ve, "agent", cfg.Agent.AgentRole, true)
	if cfg.Agent.Planner != nil {
		validateAgentRole(ve, "agent.planner", *cfg.Agent.Planner, false)
	}
	if cfg.Agent.Workers != nil {
		validateAgentRole(ve, "agent.workers", *cfg.Agent.Workers, false)
	}
	switch cfg.Agent.Exec {
	case "", "local", "docker":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("agent.exec must be one of: local, docker (or empty) — got %q", cfg.Agent.Exec))
	}

	// Workers
	if cfg.Workers.MaxConcurrent <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("workers.max_concurrent must be > 0, got %d", cfg.Workers.MaxConcurrent))
	}
	if cfg.Workers.TimeoutMinutes <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("workers.timeout_minutes must be > 0, got %d", cfg.Workers.TimeoutMinutes))
	}

	// Image publish
	if len(cfg.ImagePublish.RunsOn) == 0 {
		ve.Errors = append(ve.Errors, "image_publish.runs_on must contain at least one runner label")
	}
	for i, label := range cfg.ImagePublish.RunsOn {
		if strings.TrimSpace(label) == "" {
			ve.Errors = append(ve.Errors, fmt.Sprintf("image_publish.runs_on[%d] must be a non-empty label", i))
		}
	}
	seen := map[string]int{}
	for i, label := range cfg.ImagePublish.RunsOn {
		if first, ok := seen[label]; ok {
			ve.Errors = append(ve.Errors, fmt.Sprintf("image_publish.runs_on[%d] duplicates image_publish.runs_on[%d] (%q)", i, first, label))
		}
		seen[label] = i
	}

	// Integrator
	switch cfg.Integrator.Strategy {
	case "squash", "rebase", "merge":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.strategy must be one of: squash, rebase, merge — got %q", cfg.Integrator.Strategy))
	}
	switch cfg.Integrator.OnConflict {
	case "notify", "dispatch-resolver":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.on_conflict must be one of: notify, dispatch-resolver — got %q", cfg.Integrator.OnConflict))
	}
	if cfg.Integrator.MaxConflictResolutionAttempts <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.max_conflict_resolution_attempts must be > 0, got %d", cfg.Integrator.MaxConflictResolutionAttempts))
	}
	if cfg.Integrator.ReviewMaxFixCycles < 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.review_max_fix_cycles must be >= 0 (0 = unlimited), got %d", cfg.Integrator.ReviewMaxFixCycles))
	}
	switch cfg.Integrator.ReviewStrictness {
	case "standard", "strict", "lenient":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.review_strictness must be one of: standard, strict, lenient — got %q", cfg.Integrator.ReviewStrictness))
	}
	if cfg.Integrator.CIMaxFixCycles < 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.ci_max_fix_cycles must be >= 0 (0 = unlimited), got %d", cfg.Integrator.CIMaxFixCycles))
	}
	seenCIWorkflows := map[string]struct{}{}
	for i, workflow := range cfg.Integrator.CIWorkflows {
		if strings.TrimSpace(workflow) == "" {
			ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.ci_workflows[%d] must not be blank", i))
			continue
		}
		if _, ok := seenCIWorkflows[workflow]; ok {
			ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.ci_workflows[%d] duplicates workflow name %q", i, workflow))
			continue
		}
		seenCIWorkflows[workflow] = struct{}{}
	}

	// Monitor
	if cfg.Monitor.PatrolIntervalMinutes < 5 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("monitor.patrol_interval_minutes must be >= 5, got %d", cfg.Monitor.PatrolIntervalMinutes))
	}
	if cfg.Monitor.StaleThresholdMinutes <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("monitor.stale_threshold_minutes must be > 0, got %d", cfg.Monitor.StaleThresholdMinutes))
	}
	if cfg.Monitor.MaxPRHAgeHours <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("monitor.max_pr_age_hours must be > 0, got %d", cfg.Monitor.MaxPRHAgeHours))
	}
	if cfg.Monitor.MaxRedispatchAttempts <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("monitor.max_redispatch_attempts must be > 0, got %d", cfg.Monitor.MaxRedispatchAttempts))
	}

	// Warnings
	if cfg.Monitor.StaleThresholdMinutes <= cfg.Workers.TimeoutMinutes {
		ve.Warnings = append(ve.Warnings, fmt.Sprintf(
			"monitor.stale_threshold_minutes (%d) should be greater than workers.timeout_minutes (%d) to avoid false stale detections",
			cfg.Monitor.StaleThresholdMinutes, cfg.Workers.TimeoutMinutes,
		))
	}

	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

func validateAgentRole(ve *ValidationError, path string, role AgentRole, requireProvider bool) {
	switch role.Provider {
	case "claude", "opencode", "codex":
	case "":
		if requireProvider {
			ve.Errors = append(ve.Errors, fmt.Sprintf("%s.provider must be one of: claude, opencode, codex — got %q", path, role.Provider))
		}
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("%s.provider must be one of: claude, opencode, codex — got %q", path, role.Provider))
	}
	switch role.CodexReasoningEffort {
	case "", "minimal", "low", "medium", "high":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("%s.codex_reasoning_effort must be one of: minimal, low, medium, high — got %q", path, role.CodexReasoningEffort))
	}
	switch role.CodexSandbox {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("%s.codex_sandbox must be one of: read-only, workspace-write, danger-full-access (or empty) — got %q", path, role.CodexSandbox))
	}
}
