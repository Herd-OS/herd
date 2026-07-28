package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/herd-os/herd/internal/agent/factory"
	"github.com/herd-os/herd/internal/config"
	cpclient "github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newReviewWorkerCmd() *cobra.Command {
	var prNumber int
	var resultFile string
	var reviewPrompt string
	var manual bool
	cmd := &cobra.Command{
		Use:    "review-worker",
		Short:  "Run hosted review worker (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd review-worker is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			if prNumber <= 0 {
				return fmt.Errorf("--pr is required")
			}
			if resultFile == "" {
				return fmt.Errorf("--result-file is required")
			}
			if err := ensureProductionControlPlaneAuth("herd review-worker"); err != nil {
				return err
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			readToken, err := hostedReviewReadToken(cmd.Context(), cfg)
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return err
			}
			client, err := github.NewWithToken(cfg.Platform.Owner, cfg.Platform.Repo, readToken)
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return fmt.Errorf("creating GitHub client: %w", err)
			}
			ag, err := factory.New(cfg.Agent.Resolve(config.AgentRoleWorkers))
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return fmt.Errorf("getting current directory: %w", err)
			}

			result, err := integrator.Review(cmd.Context(), client, ag, git.New(cwd), cfg, reviewWorkerParams(prNumber, cwd, reviewPrompt, manual))
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed.",
				})
				return err
			}
			if err := writeHostedReviewResult(resultFile, hostedReviewResultFromIntegrator(result)); err != nil {
				return err
			}
			printReviewResultMessage(result)
			return nil
		},
	}
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number")
	cmd.Flags().StringVar(&reviewPrompt, "prompt", "", "Optional review focus or extra instructions")
	cmd.Flags().BoolVar(&manual, "manual", false, "Treat this review as a manual user-triggered review")
	cmd.Flags().StringVar(&resultFile, "result-file", "", "Write hosted review workflow result JSON")
	return cmd
}

func reviewWorkerParams(prNumber int, repoRoot string, reviewPrompt string, manual bool) integrator.ReviewParams {
	return integrator.ReviewParams{
		PRNumber:          prNumber,
		RepoRoot:          repoRoot,
		ExtraInstructions: strings.TrimSpace(reviewPrompt),
		Manual:            manual,
	}
}

func hostedReviewReadToken(ctx context.Context, cfg *config.Config) (string, error) {
	jobID := strings.TrimSpace(os.Getenv("HERD_JOB_ID"))
	if jobID == "" {
		return "", fmt.Errorf("HERD_JOB_ID is required for hosted review read token")
	}
	oidcToken, err := githubActionsOIDCToken(ctx)
	if err != nil {
		return "", err
	}
	cp, err := cpclient.New(cfg.EffectiveControlPlaneURL(), nil)
	if err != nil {
		return "", err
	}
	resp, err := cp.GetReviewReadToken(ctx, jobID, oidcToken)
	if err != nil {
		return "", fmt.Errorf("getting hosted review read token: %w", err)
	}
	return resp.Token, nil
}

func githubActionsOIDCToken(ctx context.Context) (string, error) {
	requestURL := strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"))
	requestToken := strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"))
	if requestURL == "" || requestToken == "" {
		return "", fmt.Errorf("GitHub Actions OIDC request environment is required for hosted review")
	}
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("parse GitHub Actions OIDC request URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("GitHub Actions OIDC request URL must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("GitHub Actions OIDC request URL is invalid")
	}
	q := parsed.Query()
	q.Set("audience", "herd-control-plane")
	parsed.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil) //nolint:gosec // URL is supplied by GitHub Actions OIDC runtime and validated above.
	if err != nil {
		return "", fmt.Errorf("create GitHub Actions OIDC request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+requestToken)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // Request URL is the validated GitHub Actions OIDC runtime endpoint.
	if err != nil {
		return "", fmt.Errorf("fetch GitHub Actions OIDC token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read GitHub Actions OIDC response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch GitHub Actions OIDC token: status %d", resp.StatusCode)
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("decode GitHub Actions OIDC response: %w", err)
	}
	if strings.TrimSpace(body.Value) == "" {
		return "", fmt.Errorf("GitHub Actions OIDC response did not include a token")
	}
	return strings.TrimSpace(body.Value), nil
}
