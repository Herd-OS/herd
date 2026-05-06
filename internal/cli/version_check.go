package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// latestReleaseURL is the GitHub API endpoint for the latest published
// (non-prerelease, non-draft) release of the herd repository. It is a
// package-level var so tests can override it to point at a test server.
var latestReleaseURL = "https://api.github.com/repos/Herd-OS/herd/releases/latest"

// versionCheckTimeout bounds the version-check HTTP request so the check
// never noticeably delays the user. It is a var so tests can shrink it.
var versionCheckTimeout = 3 * time.Second

// checkLatestVersion performs a best-effort lookup of the latest published
// herd release tag and returns it together with ok=true if (and only if) the
// latest release is strictly newer than the currently-running binary's
// `version` (the package-level variable defined in root.go).
//
// It returns ("", false) without surfacing an error in any of these cases:
//   - version == "" or version == "dev" (development build)
//   - the current binary is a pre-release (tag contains "-"), to avoid
//     suggesting a downgrade when running an rc/beta against an older stable
//   - HTTP request error, non-200 status, decode error, or context timeout
//   - latest tag parses but is not strictly newer than the current version
func checkLatestVersion(ctx context.Context) (string, bool) {
	current := version
	if current == "" || current == "dev" {
		return "", false
	}
	if strings.Contains(current, "-") {
		return "", false
	}

	reqCtx, cancel := context.WithTimeout(ctx, versionCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "herd-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", false
	}
	latest := strings.TrimSpace(payload.TagName)
	if latest == "" {
		return "", false
	}

	if !semverNewer(latest, current) {
		return "", false
	}
	return latest, true
}

// semverNewer reports whether tag `a` is strictly newer than tag `b`. Both
// arguments may carry a leading "v". Either side carrying a pre-release
// suffix (anything after a "-") is treated as not-newer, since callers
// already filter out pre-release current versions and we never want to
// suggest pre-release upgrades from this helper. Non-numeric or malformed
// segments cause the function to return false.
func semverNewer(a, b string) bool {
	aMaj, aMin, aPatch, ok := parseSemver(a)
	if !ok {
		return false
	}
	bMaj, bMin, bPatch, ok := parseSemver(b)
	if !ok {
		return false
	}
	if aMaj != bMaj {
		return aMaj > bMaj
	}
	if aMin != bMin {
		return aMin > bMin
	}
	return aPatch > bPatch
}

// parseSemver parses tags of the form "vMAJOR.MINOR.PATCH" (the leading "v"
// is optional). Anything containing a pre-release ("-") or build ("+")
// suffix is rejected (returns ok=false).
func parseSemver(tag string) (int, int, int, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if strings.ContainsAny(s, "-+") {
		return 0, 0, 0, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	patch, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return maj, min, patch, true
}
