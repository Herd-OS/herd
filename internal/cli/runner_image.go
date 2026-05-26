package cli

import "strings"

// runnerImageTag normalizes the herd binary version into the image tag used to
// pin the runner base image on ghcr.io/herd-os/. Released builds (e.g.
// "v1.4.2") pin to that exact tag; dev or empty builds fall back to "latest"
// because a tag like ":dev" does not exist in GHCR and would produce a confusing
// image-pull failure at worker build time.
func runnerImageTag(version string) string {
	v := strings.TrimSpace(version)
	if v == "" || v == "dev" {
		return "latest"
	}
	return v
}
