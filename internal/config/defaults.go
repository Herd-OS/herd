package config

// Default returns a Config with all default values.
func Default() *Config {
	return &Config{
		Version: 1,
		Platform: Platform{
			Provider: "github",
		},
		Agent: Agent{
			AgentRole: AgentRole{
				Provider:             "claude",
				CodexReasoningEffort: "medium",
			},
		},
		Workers: Workers{
			MaxConcurrent:           3,
			RunnerLabel:             "herd-worker",
			TimeoutMinutes:          30,
			ProgressIntervalSeconds: 30,
			ExtraEnv:                []string{},
		},
		Integrator: Integrator{
			Strategy:                      "squash",
			OnConflict:                    "dispatch-resolver",
			MaxConflictResolutionAttempts: 2,
			RequireCI:                     true,
			Review:                        true,
			ReviewMaxFixCycles:            0,
			ReviewStrictness:              "standard",
			ReviewFixSeverity:             "low",
			CIMaxFixCycles:                0,
			CIWorkflows:                   nil,
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
		ImagePublish: ImagePublish{
			RunsOn:       []string{"ubuntu-latest"},
			Platforms:    []string{"linux/amd64", "linux/arm64"},
			BuildSecrets: []string{},
		},
	}
}
