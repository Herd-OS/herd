package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const registerRepositoryPath = "/api/v1/github/repositories/register"
const maxCallbackPayloadBytes = 1 << 20

type RegisterRepositoryRequest struct {
	Repository string `json:"repository"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SetupToken string `json:"setup_token"`
	AppLogin   string `json:"app_login,omitempty"`
}

type RegisterRepositoryResponse struct {
	RepositoryID         int64  `json:"repository_id"`
	InstallationID       int64  `json:"installation_id"`
	RunnerBootstrapToken string `json:"runner_bootstrap_token"`
	ControlPlaneURL      string `json:"control_plane_url,omitempty"`
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type RetryOptions struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Sleep          func(context.Context, time.Duration) error
	Logger         *log.Logger
}

func New(baseURL string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("control-plane URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("control-plane URL must be an absolute http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("control-plane URL must use http or https")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

func (c *Client) RegisterRepository(ctx context.Context, req RegisterRepositoryRequest) (RegisterRepositoryResponse, error) {
	if c == nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("control-plane client is nil")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("marshal repository registration request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+registerRepositoryPath, bytes.NewReader(body))
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("create repository registration request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("read repository registration response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && strings.TrimSpace(errBody.Error) != "" {
			msg = errBody.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: %s", msg)
	}

	var out RegisterRepositoryResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("decode repository registration response: %w", err)
	}
	if out.RepositoryID == 0 || out.InstallationID == 0 || strings.TrimSpace(out.RunnerBootstrapToken) == "" {
		return RegisterRepositoryResponse{}, fmt.Errorf("control-plane registration response is missing repository_id, installation_id, or runner_bootstrap_token")
	}
	return out, nil
}

func (c *Client) SubmitJobResultWithRetry(ctx context.Context, jobID string, payload []byte, bearerToken string, opts RetryOptions) error {
	if c == nil {
		return fmt.Errorf("control-plane client is nil")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return fmt.Errorf("job result payload is required")
	}
	if len(payload) > maxCallbackPayloadBytes {
		return fmt.Errorf("job result payload exceeds %d bytes", maxCallbackPayloadBytes)
	}
	opts = normalizeRetryOptions(opts)
	path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/results"
	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		err := c.submitJobResult(ctx, path, payload, bearerToken)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableCallbackError(err) || attempt == opts.MaxAttempts {
			if opts.Logger != nil {
				opts.Logger.Printf("worker callback failed permanently job_id=%s attempts=%d error=%v", jobID, attempt, err)
			}
			return fmt.Errorf("submit worker callback after %d attempt(s): %w", attempt, err)
		}
		delay := BoundedExponentialBackoff(attempt, opts.InitialBackoff, opts.MaxBackoff)
		if opts.Logger != nil {
			opts.Logger.Printf("worker callback retrying job_id=%s attempt=%d next_delay=%s error=%v", jobID, attempt, delay, err)
		}
		if err := opts.Sleep(ctx, delay); err != nil {
			return fmt.Errorf("wait before worker callback retry: %w", err)
		}
	}
	return lastErr
}

func BoundedExponentialBackoff(attempt int, initialBackoff, maxBackoff time.Duration) time.Duration {
	if initialBackoff <= 0 {
		initialBackoff = 500 * time.Millisecond
	}
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	if attempt <= 1 {
		return initialBackoff
	}
	delay := initialBackoff
	for i := 1; i < attempt; i++ {
		if delay >= maxBackoff/2 {
			return maxBackoff
		}
		delay *= 2
	}
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

type callbackError struct {
	statusCode int
	message    string
}

func (e callbackError) Error() string {
	if e.statusCode == 0 {
		return e.message
	}
	return fmt.Sprintf("control-plane callback returned HTTP %d: %s", e.statusCode, e.message)
}

func (c *Client) submitJobResult(ctx context.Context, path string, payload []byte, bearerToken string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create worker callback request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if strings.TrimSpace(bearerToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return callbackError{message: fmt.Sprintf("send worker callback: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCallbackPayloadBytes))
	if err != nil {
		return fmt.Errorf("read worker callback response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return callbackError{statusCode: resp.StatusCode, message: msg}
	}
	return nil
}

func normalizeRetryOptions(opts RetryOptions) RetryOptions {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 4
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = 500 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.Sleep == nil {
		opts.Sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return opts
}

func isRetryableCallbackError(err error) bool {
	var callbackErr callbackError
	if !errors.As(err, &callbackErr) {
		return false
	}
	if callbackErr.statusCode == 0 {
		return true
	}
	return callbackErr.statusCode == http.StatusTooManyRequests || callbackErr.statusCode >= 500
}
