package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTransport is a configurable http.RoundTripper for testing.
type mockTransport struct {
	responses []*http.Response
	errs      []error
	calls     int
}

func (m *mockTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	i := m.calls
	m.calls++
	if i < len(m.errs) && m.errs[i] != nil {
		return nil, m.errs[i]
	}
	return m.responses[i], nil
}

// newResponse creates an *http.Response with the given status code and a no-op body.
func newResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func TestRetryTransport(t *testing.T) {
	tests := []struct {
		name           string
		responses      []*http.Response
		errs           []error
		wantCalls      int
		wantStatusCode int
		wantErr        bool
	}{
		{
			name:           "no retry on success",
			responses:      []*http.Response{newResponse(200)},
			wantCalls:      1,
			wantStatusCode: 200,
		},
		{
			name:           "no retry on 4xx",
			responses:      []*http.Response{newResponse(404)},
			wantCalls:      1,
			wantStatusCode: 404,
		},
		{
			name: "retry on 502 then success",
			responses: []*http.Response{
				newResponse(502),
				newResponse(200),
			},
			wantCalls:      2,
			wantStatusCode: 200,
		},
		{
			name: "retry exhausted",
			responses: []*http.Response{
				newResponse(503),
				newResponse(503),
				newResponse(503),
				newResponse(503),
			},
			wantCalls:      4,
			wantStatusCode: 503,
		},
		{
			name:      "network error not retried",
			responses: []*http.Response{nil},
			errs:      []error{errors.New("connection refused")},
			wantCalls: 1,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockTransport{
				responses: tt.responses,
				errs:      tt.errs,
			}
			transport := newRetryTransport(mock, 3, 1*time.Millisecond)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)

			assert.Equal(t, tt.wantCalls, mock.calls)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatusCode, resp.StatusCode)
		})
	}
}

func TestRetryTransport_RetryableStatusCodes(t *testing.T) {
	codes := []int{500, 502, 503, 504}
	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			mock := &mockTransport{
				responses: []*http.Response{
					newResponse(code),
					newResponse(200),
				},
			}
			transport := newRetryTransport(mock, 3, 1*time.Millisecond)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)

			require.NoError(t, err)
			assert.Equal(t, 2, mock.calls)
			assert.Equal(t, 200, resp.StatusCode)
		})
	}
}

func TestRetryTransport_ContextCancellation(t *testing.T) {
	mock := &mockTransport{
		responses: []*http.Response{
			newResponse(503),
			newResponse(503),
		},
	}
	transport := newRetryTransport(mock, 3, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	require.NoError(t, err)

	// Cancel the context shortly after the first attempt to interrupt the backoff wait.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err = transport.RoundTrip(req)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, mock.calls)
}

func TestNewRetryTransport_NilBase(t *testing.T) {
	transport := newRetryTransport(nil, 3, time.Second)
	assert.Equal(t, http.DefaultTransport, transport.base)
}

// bodyCapturingTransport records the request body bytes on each RoundTrip call.
type bodyCapturingTransport struct {
	bodies    []string
	responses []*http.Response
}

func (b *bodyCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	b.bodies = append(b.bodies, body)
	return b.responses[len(b.bodies)-1], nil
}

func TestRetryTransport_RewindsRequestBody(t *testing.T) {
	bt := &bodyCapturingTransport{
		responses: []*http.Response{
			newResponse(502),
			newResponse(200),
		},
	}
	transport := newRetryTransport(bt, 3, 1*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("payload"))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	require.Len(t, bt.bodies, 2)
	assert.Equal(t, "payload", bt.bodies[0], "first attempt should send full body")
	assert.Equal(t, "payload", bt.bodies[1], "retry should re-send full body")
}

func TestRetryTransport_GetBodyError(t *testing.T) {
	mock := &mockTransport{
		responses: []*http.Response{newResponse(502), newResponse(200)},
	}
	transport := newRetryTransport(mock, 3, 1*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("data"))
	require.NoError(t, err)
	// Override GetBody to return an error.
	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("rewind failed")
	}

	_, err = transport.RoundTrip(req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rewind failed")
}

// drainingBody tracks whether the body was fully read before being closed.
type drainingBody struct {
	io.Reader
	drained bool
	closed  bool
}

func (d *drainingBody) Read(p []byte) (int, error) {
	n, err := d.Reader.Read(p)
	if err == io.EOF {
		d.drained = true
	}
	return n, err
}

func (d *drainingBody) Close() error {
	d.closed = true
	return nil
}

func TestRetryTransport_DrainsResponseBody(t *testing.T) {
	body := &drainingBody{Reader: strings.NewReader("response data")}
	resp1 := &http.Response{
		StatusCode: 502,
		Body:       body,
	}
	mock := &mockTransport{
		responses: []*http.Response{resp1, newResponse(200)},
	}
	transport := newRetryTransport(mock, 3, 1*time.Millisecond)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, body.drained, "response body should be drained before close")
	assert.True(t, body.closed, "response body should be closed")
}
