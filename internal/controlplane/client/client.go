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
)

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
		return nil, fmt.Errorf("control plane URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("control plane URL must be an absolute http or https URL")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

func (c *Client) RegisterRepository(ctx context.Context, req RegisterRepositoryRequest) (RegisterRepositoryResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("encoding repository registration request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/github/repositories/register", bytes.NewReader(body))
	if err != nil {
		return RegisterRepositoryResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("registering repository with Herd control plane: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("reading repository registration response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RegisterRepositoryResponse{}, fmt.Errorf("registering repository with Herd control plane: %s", responseErrorMessage(resp.StatusCode, data))
	}

	var out RegisterRepositoryResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return RegisterRepositoryResponse{}, fmt.Errorf("decoding repository registration response: %w", err)
	}
	if out.RunnerBootstrapToken == "" {
		return RegisterRepositoryResponse{}, fmt.Errorf("repository registration response missing runner bootstrap token")
	}
	return out, nil
}

func responseErrorMessage(status int, data []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		return payload.Error
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return http.StatusText(status)
	}
	return text
}
