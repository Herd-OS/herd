package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/spf13/cobra"
)

// runCommand executes an external command in dir, wiring stdout/stderr to the
// process streams. It is a package var so tests can swap in a recorder instead
// of spawning real processes (e.g. docker).
var runCommand = func(dir, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// consumerRunnerImage returns the GHCR reference for a consumer's customized
// runner image, e.g. ghcr.io/<owner>/<repo>-herd-runner:<tag>.
func consumerRunnerImage(owner, repo, tag string) string {
	return fmt.Sprintf("ghcr.io/%s/%s-herd-runner:%s", strings.ToLower(owner), strings.ToLower(repo), tag)
}

// resolveImageRef detects owner/repo from dir's git remote and builds the
// consumer runner image reference using the effective tag (flag override or
// the herd version).
func resolveImageRef(dir, tag string) (string, error) {
	owner, repo, err := detectOwnerRepo(dir)
	if err != nil {
		return "", fmt.Errorf("could not detect repository: %w — make sure a GitHub remote is configured", err)
	}
	effective := tag
	if effective == "" {
		effective = runnerImageTag(version)
	}
	return consumerRunnerImage(owner, repo, effective), nil
}

func newImageCmd() *cobra.Command {
	var tag string
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build and publish the customized runner image",
		Args:  cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&tag, "tag", "", "Image tag (default: herd version, or 'latest' for dev builds)")
	cmd.AddCommand(newImageBuildCmd(&tag))
	cmd.AddCommand(newImagePublishCmd(&tag))
	return cmd
}

func newImageBuildCmd(tag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Build the customized runner image from Dockerfile.herd_runner",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(dir, "Dockerfile.herd_runner")); os.IsNotExist(err) {
				//nolint:staticcheck // ST1005: "Dockerfile.herd_runner" is a literal filename used as the sentence subject; lowercasing it ("dockerfile.herd_runner not found...") would be misleading.
				return fmt.Errorf("Dockerfile.herd_runner not found — run `herd init` first")
			}
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			if ve := config.Validate(cfg); ve != nil {
				return fmt.Errorf("invalid config: %w", ve)
			}
			var missing []string
			for _, name := range cfg.ImagePublish.BuildSecrets {
				if _, ok := os.LookupEnv(name); !ok {
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("missing build secret environment variable(s): %s", strings.Join(missing, ", "))
			}
			ref, err := resolveImageRef(dir, *tag)
			if err != nil {
				return err
			}
			dockerArgs := []string{"build", "-f", "Dockerfile.herd_runner"}
			for _, name := range cfg.ImagePublish.BuildSecrets {
				dockerArgs = append(dockerArgs, "--secret", fmt.Sprintf("id=%s,env=%s", config.BuildSecretID(name), name))
			}
			dockerArgs = append(dockerArgs, "-t", ref, ".")
			if err := runCommand(dir, "docker", dockerArgs...); err != nil {
				return fmt.Errorf("docker build: %w", err)
			}
			fmt.Println("Built " + ref)
			return nil
		},
		SilenceUsage: true,
	}
}

func newImagePublishCmd(tag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "publish",
		Short: "Push the customized runner image to GHCR",
		Long:  "Push the customized runner image to GHCR. Run `docker login ghcr.io` first.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			ref, err := resolveImageRef(dir, *tag)
			if err != nil {
				return err
			}
			if err := runCommand(dir, "docker", "push", ref); err != nil {
				return fmt.Errorf("docker push: %w", err)
			}
			fmt.Println("Pushed " + ref)
			return nil
		},
		SilenceUsage: true,
	}
}
