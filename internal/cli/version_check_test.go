package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setVersionForTest overrides the package-level version variable for the
// duration of the test, restoring the previous value via t.Cleanup.
func setVersionForTest(t *testing.T, v string) {
	t.Helper()
	prev := version
	version = v
	t.Cleanup(func() { version = prev })
}

// setLatestReleaseURL overrides the latestReleaseURL package var for the
// duration of the test, restoring the previous value via t.Cleanup.
func setLatestReleaseURL(t *testing.T, url string) {
	t.Helper()
	prev := latestReleaseURL
	latestReleaseURL = url
	t.Cleanup(func() { latestReleaseURL = prev })
}

// setVersionCheckTimeout overrides the package-level timeout for the
// duration of the test, restoring the previous value via t.Cleanup.
func setVersionCheckTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := versionCheckTimeout
	versionCheckTimeout = d
	t.Cleanup(func() { versionCheckTimeout = prev })
}

func TestCheckLatestVersion_NewerAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.4"}`))
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3")

	latest, ok := checkLatestVersion(context.Background())
	require.True(t, ok)
	assert.Equal(t, "v0.5.4", latest)
}

func TestCheckLatestVersion_UpToDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.3"}`))
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_OlderRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.2"}`))
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_NetworkFailure(t *testing.T) {
	setLatestReleaseURL(t, "http://127.0.0.1:1")
	setVersionForTest(t, "v0.5.3")
	setVersionCheckTimeout(t, 200*time.Millisecond)

	start := time.Now()
	latest, ok := checkLatestVersion(context.Background())
	elapsed := time.Since(start)

	require.False(t, ok)
	assert.Equal(t, "", latest)
	assert.Less(t, elapsed, time.Second, "checkLatestVersion should respect the timeout")
}

func TestCheckLatestVersion_Non200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_BadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_DevBuild(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("HTTP server must not be hit when version is 'dev'")
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "dev")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_EmptyVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("HTTP server must not be hit when version is empty")
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestCheckLatestVersion_PreReleaseCurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("HTTP server must not be hit when current version is a pre-release")
	}))
	t.Cleanup(server.Close)

	setLatestReleaseURL(t, server.URL)
	setVersionForTest(t, "v0.5.3-rc.1")

	latest, ok := checkLatestVersion(context.Background())
	require.False(t, ok)
	assert.Equal(t, "", latest)
}

func TestSemverNewer(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		expected bool
	}{
		{"patch newer", "v0.5.4", "v0.5.3", true},
		{"equal", "v0.5.3", "v0.5.3", false},
		{"older", "v0.5.2", "v0.5.3", false},
		{"major newer", "v1.0.0", "v0.99.99", true},
		{"minor numeric not lexical", "v0.10.0", "v0.9.99", true},
		{"a is pre-release", "v0.5.3-rc.1", "v0.5.2", false},
		{"a is garbage", "garbage", "v0.5.3", false},
		{"b has build metadata", "v0.5.4", "v0.5.4+meta", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, semverNewer(tt.a, tt.b))
		})
	}
}
