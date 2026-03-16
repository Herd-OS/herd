package config

// Default returns a Config with all default values.
func Default() *Config {
	return &Config{
		Version: 1,
		Platform: Platform{
			Provider: "github",
		},
		Agent: Agent{
			Provider: "claude",
		},
		Workers: Workers{
			MaxConcurrent:  3,
			RunnerLabel:    "herd-worker",
			TimeoutMinutes: 30,
		},
		Integrator: Integrator{
			Strategy:                      "squash",
			OnConflict:                    "dispatch-resolver",
			MaxConflictResolutionAttempts: 2,
			RequireCI:                     true,
			Review:                        true,
			ReviewMaxFixCycles:            10,
			CIMaxFixCycles:                10,
		},
		Monitor: Monitor{
			PatrolIntervalMinutes: 15,
			StaleThresholdMinutes: 30,
			MaxPRHAgeHours:        24,
			AutoRedispatch:        true,
			MaxRedispatchAttempts: 3,
			NotifyOnFailure:       true,
			NotifyUsers:           []string{},
		},
		PullRequests: PullRequests{
			AutoMerge: false,
		},
	}
}
