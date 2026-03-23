package github

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// defaultMaxRetries is the number of retry attempts for transient HTTP errors.
const defaultMaxRetries = 3

// retryableStatusCodes are the HTTP status codes that trigger a retry.
var retryableStatusCodes = map[int]bool{
	500: true,
	502: true,
	503: true,
	504: true,
}

// retryTransport wraps an http.RoundTripper with retry logic for transient 5xx errors.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
}

// newRetryTransport creates a retryTransport wrapping the given base transport.
// It retries up to defaultMaxRetries times with exponential backoff starting at baseDelay.
func newRetryTransport(base http.RoundTripper, baseDelay time.Duration) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryTransport{
		base:       base,
		maxRetries: defaultMaxRetries,
		baseDelay:  baseDelay,
	}
}

// RoundTrip executes the request with retry logic for transient 5xx errors.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		// Re-wind request body for retries so non-GET requests send the full payload.
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("retry: failed to rewind request body: %w", err)
			}
			req.Body = body
		}

		resp, err = t.base.RoundTrip(req)
		if err != nil {
			return nil, err // network errors are not retried
		}
		if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil
		}
		if attempt < t.maxRetries {
			// Drain and close the body before retrying to allow connection reuse.
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain
			_ = resp.Body.Close()
			delay := t.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}
	}
	return resp, nil
}
