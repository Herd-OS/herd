package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const registerRepositoryPath = "/api/v1/github/repositories/register"

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
