package cli

import (
	"fmt"
	"os"
)

const localGitHubAuthOverrideEnv = "HERD_LOCAL_GITHUB_AUTH"

func ensureProductionControlPlaneAuth(command string) error {
	if os.Getenv("HERD_RUNNER") != "true" {
		return nil
	}
	if os.Getenv(localGitHubAuthOverrideEnv) == "true" {
		return nil
	}
	if os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_TOKEN") != "" || os.Getenv("HERD_GITHUB_TOKEN") != "" {
		return fmt.Errorf("%s cannot use GITHUB_TOKEN, GH_TOKEN, or HERD_GITHUB_TOKEN for production Herd orchestration; register the repository with the Herd control plane and remove legacy PAT secrets", command)
	}
	return nil
}
