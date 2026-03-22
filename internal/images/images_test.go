package images

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPNG is a minimal valid PNG header for test data.
var testPNG = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func TestDownloadAndReplace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/not-found" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG)
	}))
	defer ts.Close()

	// We need URLs that match the regex pattern.
	// The regex requires specific GitHub domains, but we need to download from our test server.
	// So we test the regex matching and URL replacement logic separately from actual downloads.

	t.Run("single attachment URL downloads and replaces", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		// Construct a URL that matches the regex but points to our test server.
		// We'll use a custom transport to redirect GitHub URLs to our test server.
		transport := &rewriteTransport{
			base:      http.DefaultTransport,
			targetURL: ts.URL,
		}
		client := &http.Client{Transport: transport}

		md := "![screenshot](https://github.com/user-attachments/assets/abc-123)"
		result, err := DownloadAndReplace(context.Background(), client, md, imgDir)
		require.NoError(t, err)

		assert.NotEqual(t, md, result)
		assert.Contains(t, result, "![screenshot](")
		assert.Contains(t, result, ".png)")
		assert.NotContains(t, result, "https://github.com")

		// Verify file was created
		files, err := os.ReadDir(imgDir)
		require.NoError(t, err)
		assert.Len(t, files, 1)

		// Verify file content
		content, err := os.ReadFile(filepath.Join(imgDir, files[0].Name()))
		require.NoError(t, err)
		assert.Equal(t, testPNG, content)
	})

	t.Run("multiple images all downloaded and replaced", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		transport := &rewriteTransport{base: http.DefaultTransport, targetURL: ts.URL}
		client := &http.Client{Transport: transport}

		md := "![img1](https://github.com/user-attachments/assets/abc-123)\n![img2](https://github.com/owner/repo/assets/12345/image.png)"
		result, err := DownloadAndReplace(context.Background(), client, md, imgDir)
		require.NoError(t, err)

		assert.NotContains(t, result, "https://github.com")
		assert.Contains(t, result, "![img1](")
		assert.Contains(t, result, "![img2](")

		files, err := os.ReadDir(imgDir)
		require.NoError(t, err)
		assert.Len(t, files, 2)
	})

	t.Run("mixed content only replaces GitHub attachment URLs", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		transport := &rewriteTransport{base: http.DefaultTransport, targetURL: ts.URL}
		client := &http.Client{Transport: transport}

		md := "![gh](https://github.com/user-attachments/assets/abc-123)\n![logo](https://example.com/logo.png)"
		result, err := DownloadAndReplace(context.Background(), client, md, imgDir)
		require.NoError(t, err)

		assert.Contains(t, result, "![logo](https://example.com/logo.png)")
		assert.NotContains(t, result, "https://github.com/user-attachments")
	})

	t.Run("download failure preserves original URL", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		// Use a transport that always returns 404
		transport := &rewriteTransport{
			base:      http.DefaultTransport,
			targetURL: ts.URL + "/not-found",
		}
		client := &http.Client{Transport: transport}

		md := "![broken](https://github.com/user-attachments/assets/abc-123)"
		result, err := DownloadAndReplace(context.Background(), client, md, imgDir)
		require.NoError(t, err)

		assert.Equal(t, md, result)
	})

	t.Run("private user images URL matched and downloaded", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		transport := &rewriteTransport{base: http.DefaultTransport, targetURL: ts.URL}
		client := &http.Client{Transport: transport}

		md := "![private](https://private-user-images.githubusercontent.com/12345/abc.png?jwt=token123)"
		result, err := DownloadAndReplace(context.Background(), client, md, imgDir)
		require.NoError(t, err)

		assert.NotContains(t, result, "private-user-images.githubusercontent.com")
		assert.Contains(t, result, "![private](")
		assert.Contains(t, result, ".png)")
	})

	t.Run("no images returns text unchanged without creating directory", func(t *testing.T) {
		destDir := t.TempDir()
		imgDir := filepath.Join(destDir, "images")

		result, err := DownloadAndReplace(context.Background(), http.DefaultClient, "no images here", imgDir)
		require.NoError(t, err)

		assert.Equal(t, "no images here", result)
		_, err = os.Stat(imgDir)
		assert.True(t, os.IsNotExist(err))
	})
}

func TestDownloadAndReplace_AuthHeaderPassed(t *testing.T) {
	var receivedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG)
	}))
	defer ts.Close()

	transport := &authRewriteTransport{
		base:      http.DefaultTransport,
		targetURL: ts.URL,
		token:     "Bearer test-token-123",
	}
	client := &http.Client{Transport: transport}

	destDir := t.TempDir()
	imgDir := filepath.Join(destDir, "images")

	md := "![auth](https://github.com/user-attachments/assets/abc-123)"
	_, err := DownloadAndReplace(context.Background(), client, md, imgDir)
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-token-123", receivedAuth)
}

func TestGuessExtension(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		url         string
		want        string
	}{
		{name: "image/png", contentType: "image/png", url: "", want: ".png"},
		{name: "image/jpeg", contentType: "image/jpeg", url: "", want: ".jpg"},
		{name: "image/gif", contentType: "image/gif", url: "", want: ".gif"},
		{name: "image/webp", contentType: "image/webp", url: "", want: ".webp"},
		{name: "image/svg+xml", contentType: "image/svg+xml", url: "", want: ".svg"},
		{name: "empty with .jpg in URL", contentType: "", url: "https://example.com/photo.jpg", want: ".jpg"},
		{name: "empty with .jpg in URL with query", contentType: "", url: "https://example.com/photo.jpg?size=large", want: ".jpg"},
		{name: "empty with no extension", contentType: "", url: "https://example.com/photo", want: ".png"},
		{name: "unknown content type with URL extension", contentType: "application/octet-stream", url: "https://example.com/photo.gif", want: ".gif"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guessExtension(tt.contentType, tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGithubAttachmentRegex(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		matches bool
	}{
		{
			name:    "user-attachments URL",
			input:   "![screenshot](https://github.com/user-attachments/assets/abc-123)",
			matches: true,
		},
		{
			name:    "repo assets URL",
			input:   "![alt](https://github.com/owner/repo/assets/12345/image.png)",
			matches: true,
		},
		{
			name:    "private user images URL",
			input:   "![](https://private-user-images.githubusercontent.com/12345/abc.png?jwt=token)",
			matches: true,
		},
		{
			name:    "regular URL not matched",
			input:   "![logo](https://example.com/logo.png)",
			matches: false,
		},
		{
			name:    "regular GitHub URL not matched",
			input:   "![img](https://github.com/owner/repo/blob/main/image.png)",
			matches: false,
		},
		{
			name:    "empty alt text",
			input:   "![](https://github.com/user-attachments/assets/uuid-here)",
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := githubAttachmentRe.MatchString(tt.input)
			assert.Equal(t, tt.matches, got)
		})
	}
}

// rewriteTransport rewrites all request URLs to point to a test server.
type rewriteTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	target := t.targetURL
	if !strings.HasPrefix(target, "http") {
		target = "http://" + target
	}
	parsed, err := newReq.URL.Parse(target)
	if err != nil {
		return nil, err
	}
	newReq.URL = parsed
	newReq.Host = parsed.Host
	return t.base.RoundTrip(newReq)
}

// authRewriteTransport adds an auth header and rewrites URLs.
type authRewriteTransport struct {
	base      http.RoundTripper
	targetURL string
	token     string
}

func (t *authRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.Header.Set("Authorization", t.token)
	parsed, err := newReq.URL.Parse(t.targetURL)
	if err != nil {
		return nil, err
	}
	newReq.URL = parsed
	newReq.Host = parsed.Host
	return t.base.RoundTrip(newReq)
}
