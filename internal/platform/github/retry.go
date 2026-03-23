package github

import (
	"math"
	"net/http"
	"time"
)

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
// It retries up to maxRetries times with exponential backoff starting at baseDelay.
func newRetryTransport(base http.RoundTripper, maxRetries int, baseDelay time.Duration) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryTransport{
		base:       base,
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
	}
}

// RoundTrip executes the request with retry logic for transient 5xx errors.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		resp, err = t.base.RoundTrip(req)
		if err != nil {
			return nil, err // network errors are not retried
		}
		if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil
		}
		if attempt < t.maxRetries {
			// Drain and close the body before retrying to reuse the connection.
			resp.Body.Close()
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
