package images

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// githubAttachmentRe matches GitHub user-uploaded attachment URLs in markdown images.
// Examples:
//
//	![screenshot](https://github.com/user-attachments/assets/abc-123)
//	![alt text](https://github.com/user/repo/assets/12345/image.png)
//	![](https://private-user-images.githubusercontent.com/12345/abc.png?jwt=...)
var githubAttachmentRe = regexp.MustCompile(`!\[([^\]]*)\]\((https://(?:github\.com/user-attachments/assets/|github\.com/[^/]+/[^/]+/assets/|private-user-images\.githubusercontent\.com/)[^)]+)\)`)

// DownloadAndReplace finds GitHub attachment image URLs in markdown text,
// downloads each image to destDir using the provided HTTP client, and
// returns the markdown with URLs replaced by absolute local file paths.
//
// If an image fails to download, the original URL is preserved (best-effort).
// If no images are found, the original text is returned unchanged.
func DownloadAndReplace(ctx context.Context, client *http.Client, markdown string, destDir string) (string, error) {
	matches := githubAttachmentRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return markdown, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("creating image directory %s: %w", destDir, err)
	}

	// Process matches in reverse order so indices remain valid after replacement.
	result := markdown
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		// m[4] and m[5] are the start/end of the URL capture group
		url := markdown[m[4]:m[5]]
		alt := markdown[m[2]:m[3]]

		localPath, err := downloadImage(ctx, client, url, destDir)
		if err != nil {
			// Best-effort: keep original URL
			fmt.Fprintf(os.Stderr, "warning: failed to download image %s: %v\n", url, err)
			continue
		}

		// Replace the entire ![alt](url) with ![alt](localPath)
		replacement := fmt.Sprintf("![%s](%s)", alt, localPath)
		result = result[:m[0]] + replacement + result[m[1]:]
	}

	return result, nil
}

// downloadImage downloads a single image URL to destDir.
// The filename is derived from a hash of the URL to avoid collisions.
// Returns the absolute path to the downloaded file.
func downloadImage(ctx context.Context, client *http.Client, url, destDir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	ext := guessExtension(resp.Header.Get("Content-Type"), url)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))[:12]
	filename := hash + ext
	destPath := filepath.Join(destDir, filename)

	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("writing file: %w", err)
	}

	absPath, err := filepath.Abs(destPath)
	if err != nil {
		return destPath, nil
	}
	return absPath, nil
}

// guessExtension returns a file extension based on Content-Type header,
// falling back to the URL's extension, then ".png" as default.
func guessExtension(contentType, url string) string {
	switch {
	case strings.HasPrefix(contentType, "image/png"):
		return ".png"
	case strings.HasPrefix(contentType, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(contentType, "image/gif"):
		return ".gif"
	case strings.HasPrefix(contentType, "image/webp"):
		return ".webp"
	case strings.HasPrefix(contentType, "image/svg"):
		return ".svg"
	}

	// Try URL extension
	if ext := filepath.Ext(strings.SplitN(url, "?", 2)[0]); ext != "" {
		return ext
	}

	return ".png"
}
