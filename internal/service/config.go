package service

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane"
)

const (
	envGitHubAppID         = "HERD_GITHUB_APP_ID"
	envGitHubAppPrivateKey = "HERD_GITHUB_APP_PRIVATE_KEY"
	envWebhookSecret       = "HERD_WEBHOOK_SECRET" // #nosec G101 -- environment variable name, not a secret value.
	envPublicURL           = "HERD_PUBLIC_URL"
	envDatabaseURL         = "HERD_DATABASE_URL"
	envEnv                 = "HERD_ENV"
	envGitHubAppLogin      = "HERD_GITHUB_APP_LOGIN"
	envOIDCAudience        = "HERD_OIDC_AUDIENCE"
	envReconcilerEnabled   = "HERD_RECONCILER_ENABLED"
	envReconcilerInterval  = "HERD_RECONCILER_INTERVAL"

	defaultEnv            = "production"
	defaultGitHubAppLogin = "herd-os"
)

type Config struct {
	GitHubAppID         int64
	GitHubAppPrivateKey string
	WebhookSecret       string
	PublicURL           string
	DatabaseURL         string
	Env                 string
	AppLogin            string
	OIDCAudience        string
	ReconcilerEnabled   bool
	ReconcilerInterval  string
}

func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		GitHubAppPrivateKey: os.Getenv(envGitHubAppPrivateKey),
		WebhookSecret:       os.Getenv(envWebhookSecret),
		PublicURL:           os.Getenv(envPublicURL),
		DatabaseURL:         os.Getenv(envDatabaseURL),
		Env:                 envOrDefault(envEnv, defaultEnv),
		AppLogin:            envOrDefault(envGitHubAppLogin, defaultGitHubAppLogin),
		OIDCAudience:        envOrDefault(envOIDCAudience, controlplane.DefaultOIDCAudience),
		ReconcilerInterval:  os.Getenv(envReconcilerInterval),
	}

	appID := os.Getenv(envGitHubAppID)
	if appID != "" {
		parsed, err := strconv.ParseInt(appID, 10, 64)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer", envGitHubAppID)
		}
		cfg.GitHubAppID = parsed
	}
	if enabled := os.Getenv(envReconcilerEnabled); enabled != "" {
		parsed, err := strconv.ParseBool(enabled)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a boolean", envReconcilerEnabled)
		}
		cfg.ReconcilerEnabled = parsed
	}

	return cfg, nil
}

func (cfg Config) Validate() error {
	var validationErrs []error

	if strings.TrimSpace(cfg.PublicURL) != "" {
		if err := validatePublicURL(cfg.PublicURL); err != nil {
			validationErrs = append(validationErrs, err)
		}
	}
	if strings.TrimSpace(cfg.ReconcilerInterval) != "" {
		if _, err := time.ParseDuration(cfg.ReconcilerInterval); err != nil {
			validationErrs = append(validationErrs, fmt.Errorf("%s must be a valid duration", envReconcilerInterval))
		}
	}

	if cfg.Env != "production" {
		return errors.Join(validationErrs...)
	}

	if cfg.GitHubAppID <= 0 {
		validationErrs = append(validationErrs, fmt.Errorf("%s is required and must be a positive integer", envGitHubAppID))
	}
	if strings.TrimSpace(cfg.GitHubAppPrivateKey) == "" {
		validationErrs = append(validationErrs, fmt.Errorf("%s is required", envGitHubAppPrivateKey))
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		validationErrs = append(validationErrs, fmt.Errorf("%s is required", envWebhookSecret))
	}
	if strings.TrimSpace(cfg.PublicURL) == "" {
		validationErrs = append(validationErrs, fmt.Errorf("%s is required", envPublicURL))
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		validationErrs = append(validationErrs, fmt.Errorf("%s is required", envDatabaseURL))
	}

	return errors.Join(validationErrs...)
}

func envOrDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func validatePublicURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid absolute http or https URL", envPublicURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", envPublicURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a host", envPublicURL)
	}
	return nil
}
