package cli

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config [key] [value]",
		Short: "View/edit configuration",
		Long:  "View and edit HerdOS configuration.\nWith no arguments, prints usage.\nWith one argument, prints the current value.\nWith two arguments, sets the value.",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runConfigGetSet,
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show all config values",
			Args:  cobra.NoArgs,
			RunE:  runConfigList,
		},
		&cobra.Command{
			Use:   "edit",
			Short: "Open .herdos.yml in $EDITOR",
			Args:  cobra.NoArgs,
			RunE:  runConfigEdit,
		},
	)

	return cmd
}

func runConfigList(_ *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	for _, kv := range flattenConfig(cfg) {
		fmt.Printf("%s: %s\n", kv.key, kv.value)
	}
	return nil
}

func runConfigEdit(_ *cobra.Command, _ []string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	path, err := configPath()
	if err != nil {
		return err
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runConfigGetSet(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: herd config <key> [value]\n  herd config list    — show all values\n  herd config edit    — open in editor")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	key := args[0]

	if len(args) == 1 {
		// Get
		val, err := getConfigValue(cfg, key)
		if err != nil {
			return err
		}
		fmt.Println(val)
		return nil
	}

	// Set
	oldVal, err := getConfigValue(cfg, key)
	if err != nil {
		return err
	}

	if err := setConfigValue(cfg, key, args[1]); err != nil {
		return err
	}

	if ve := config.Validate(cfg); ve != nil {
		return fmt.Errorf("invalid value: %s", ve.Errors[0])
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := config.Save(dir, cfg); err != nil {
		return err
	}

	fmt.Printf("Updated %s: %s → %s\n", key, oldVal, args[1])
	return nil
}

func loadConfig() (*config.Config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return config.Load(dir)
}

func configPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := dir + "/" + config.ConfigFile
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no %s found (run 'herd init' first)", config.ConfigFile)
	}
	return path, nil
}

type keyValue struct {
	key   string
	value string
}

func flattenConfig(cfg *config.Config) []keyValue {
	var kvs []keyValue
	kvs = append(kvs, keyValue{"platform.provider", cfg.Platform.Provider})
	kvs = append(kvs, keyValue{"platform.owner", cfg.Platform.Owner})
	kvs = append(kvs, keyValue{"platform.repo", cfg.Platform.Repo})
	kvs = append(kvs, keyValue{"agent.provider", cfg.Agent.Provider})
	kvs = append(kvs, keyValue{"agent.binary", displayValue(cfg.Agent.Binary)})
	kvs = append(kvs, keyValue{"agent.model", displayValue(cfg.Agent.Model)})
	kvs = append(kvs, keyValue{"workers.max_concurrent", itoa(cfg.Workers.MaxConcurrent)})
	kvs = append(kvs, keyValue{"workers.runner_label", cfg.Workers.RunnerLabel})
	kvs = append(kvs, keyValue{"workers.timeout_minutes", itoa(cfg.Workers.TimeoutMinutes)})
	kvs = append(kvs, keyValue{"workers.progress_interval_seconds", itoa(cfg.Workers.ProgressIntervalSeconds)})
	kvs = append(kvs, keyValue{"integrator.strategy", cfg.Integrator.Strategy})
	kvs = append(kvs, keyValue{"integrator.on_conflict", cfg.Integrator.OnConflict})
	kvs = append(kvs, keyValue{"integrator.max_conflict_resolution_attempts", itoa(cfg.Integrator.MaxConflictResolutionAttempts)})
	kvs = append(kvs, keyValue{"integrator.require_ci", btoa(cfg.Integrator.RequireCI)})
	kvs = append(kvs, keyValue{"integrator.review", btoa(cfg.Integrator.Review)})
	kvs = append(kvs, keyValue{"integrator.review_max_fix_cycles", itoa(cfg.Integrator.ReviewMaxFixCycles)})
	kvs = append(kvs, keyValue{"integrator.review_strictness", displayValue(cfg.Integrator.ReviewStrictness)})
	kvs = append(kvs, keyValue{"integrator.review_fix_severity", displayValue(cfg.Integrator.ReviewFixSeverity)})
	kvs = append(kvs, keyValue{"integrator.ci_max_fix_cycles", itoa(cfg.Integrator.CIMaxFixCycles)})
	kvs = append(kvs, keyValue{"monitor.patrol_interval_minutes", itoa(cfg.Monitor.PatrolIntervalMinutes)})
	kvs = append(kvs, keyValue{"monitor.stale_threshold_minutes", itoa(cfg.Monitor.StaleThresholdMinutes)})
	kvs = append(kvs, keyValue{"monitor.max_pr_age_hours", itoa(cfg.Monitor.MaxPRHAgeHours)})
	kvs = append(kvs, keyValue{"monitor.auto_redispatch", btoa(cfg.Monitor.AutoRedispatch)})
	kvs = append(kvs, keyValue{"monitor.max_redispatch_attempts", itoa(cfg.Monitor.MaxRedispatchAttempts)})
	kvs = append(kvs, keyValue{"monitor.notify_on_failure", btoa(cfg.Monitor.NotifyOnFailure)})
	kvs = append(kvs, keyValue{"monitor.notify_users", formatStringSlice(cfg.Monitor.NotifyUsers)})
	kvs = append(kvs, keyValue{"pull_requests.auto_merge", btoa(cfg.PullRequests.AutoMerge)})
	kvs = append(kvs, keyValue{"pull_requests.co_author_email", displayValue(cfg.PullRequests.CoAuthorEmail)})
	return kvs
}

func getConfigValue(cfg *config.Config, key string) (string, error) {
	for _, kv := range flattenConfig(cfg) {
		if kv.key == key {
			return kv.value, nil
		}
	}
	return "", fmt.Errorf("unknown config key: %s", key)
}

func setConfigValue(cfg *config.Config, key, value string) error {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid key format: %s (expected section.field)", key)
	}

	section, field := parts[0], parts[1]

	// Use reflection to set the field
	cfgVal := reflect.ValueOf(cfg).Elem()
	sectionField := findField(cfgVal, section)
	if !sectionField.IsValid() {
		return fmt.Errorf("unknown config section: %s", section)
	}

	targetField := findField(sectionField, field)
	if !targetField.IsValid() {
		return fmt.Errorf("unknown config key: %s", key)
	}

	switch targetField.Kind() {
	case reflect.String:
		targetField.SetString(value)
	case reflect.Int:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be a number, got %q", key, value)
		}
		targetField.SetInt(int64(n))
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be true or false, got %q", key, value)
		}
		targetField.SetBool(b)
	default:
		return fmt.Errorf("cannot set %s via CLI (use 'herd config edit')", key)
	}

	return nil
}

func findField(v reflect.Value, yamlName string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		tag = strings.Split(tag, ",")[0]
		if tag == yamlName {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

func displayValue(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func itoa(n int) string    { return strconv.Itoa(n) }
func btoa(b bool) string   { return strconv.FormatBool(b) }

func formatStringSlice(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	return "[" + strings.Join(ss, ", ") + "]"
}
