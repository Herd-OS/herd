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

	// Agent
	switch cfg.Agent.Provider {
	case "claude":
	default:
		ve.Errors = append(ve.Errors, fmt.Sprintf("agent.provider must be one of: claude — got %q", cfg.Agent.Provider))
	}

	// Workers
	if cfg.Workers.MaxConcurrent <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("workers.max_concurrent must be > 0, got %d", cfg.Workers.MaxConcurrent))
	}
	if cfg.Workers.TimeoutMinutes <= 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("workers.timeout_minutes must be > 0, got %d", cfg.Workers.TimeoutMinutes))
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
	if cfg.Integrator.CIMaxFixCycles < 0 {
		ve.Errors = append(ve.Errors, fmt.Sprintf("integrator.ci_max_fix_cycles must be >= 0 (0 = unlimited), got %d", cfg.Integrator.CIMaxFixCycles))
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
