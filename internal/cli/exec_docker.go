package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/herd-os/herd/internal/config"
	"golang.org/x/term"
)

// passEnv lists the host environment variables forwarded into the docker-exec
// container. Each is emitted as `-e VAR` (name only, no value) and only when
// os.LookupEnv reports it set, so unset entries are harmless.
var passEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"CODEX_API_KEY",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	"HERD_GITHUB_TOKEN",
	// Codex subscription auth (opt-in). Only the bare CODEX_AUTH_JSON is forwarded.
	"CODEX_AUTH_JSON",
	"CODEX_ACCESS_TOKEN",
	"CODEX_HOME",
	"HERD_CODEX_KEEPALIVE_INTERVAL",
}

// resolveExecMode determines whether the agent session runs locally or inside
// docker. Precedence: flag > HERD_EXEC env > cfg.Agent.Exec > "local".
// Any empty or unrecognized value resolves to "local".
func resolveExecMode(flagVal, cfgVal string) string {
	v := flagVal
	if v == "" {
		v = os.Getenv("HERD_EXEC")
	}
	if v == "" {
		v = cfgVal
	}
	if v == "docker" {
		return "docker"
	}
	return "local"
}

// execDispatch captures the decision of whether `herd plan` should re-exec the
// agent session inside docker, and whether the recursion guard fired.
type execDispatch struct {
	runDocker bool // run the agent inside docker
	guardHit  bool // docker requested but we are already inside a container
}

// decideExecDispatch resolves the exec mode and applies the recursion guard.
// When the resolved mode is docker but we are already inside a container, it
// returns guardHit so the caller can warn and fall back to local execution.
func decideExecDispatch(flagVal, cfgVal string, insideContainer bool) execDispatch {
	if resolveExecMode(flagVal, cfgVal) != "docker" {
		return execDispatch{}
	}
	if insideContainer {
		return execDispatch{guardHit: true}
	}
	return execDispatch{runDocker: true}
}

// defaultExecImage returns the runner-base image ref derived from the herd
// version. Development builds (version "" or "dev") use the :latest tag.
func defaultExecImage() string {
	if version == "" || version == "dev" {
		return "ghcr.io/herd-os/herd-runner-base:latest"
	}
	return "ghcr.io/herd-os/herd-runner-base:" + version
}

// execImageRef returns the docker image to use for exec=docker. A configured
// ExecImage overrides the version-derived default.
func execImageRef(cfg *config.Config) string {
	if cfg.Agent.ExecImage != "" {
		return cfg.Agent.ExecImage
	}
	return defaultExecImage()
}

// stripExecFlag returns a copy of args with the --exec flag (and its value)
// removed so the inner herd does not re-trigger docker. Both --exec docker
// (two tokens) and --exec=docker (single token) forms are handled. The input
// slice is not mutated; all other args keep their order.
func stripExecFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--exec" {
			// Drop "--exec" and its value token (if present).
			i++
			continue
		}
		if len(a) >= len("--exec=") && a[:len("--exec=")] == "--exec=" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// BuildDockerExecCmd builds (but does not run) the `docker run ...` command
// that executes the inner herd inside the runner-base image. It is pure: it
// only performs read-only env/filesystem lookups and never wires stdio or runs
// the command — the caller does that.
func BuildDockerExecCmd(ctx context.Context, cfg *config.Config, args []string) (*exec.Cmd, error) {
	dockerArgs := []string{"run", "--rm", "--init", "-i"}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dockerArgs = append(dockerArgs, "-t")
	}

	uid := os.Getuid()
	gid := os.Getgid()
	dockerArgs = append(dockerArgs, "--user", fmt.Sprintf("%d:%d", uid, gid))

	pwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}
	dockerArgs = append(dockerArgs, "-v", pwd+":/work", "-w", "/work")
	dockerArgs = append(dockerArgs, "-e", "HOME=/tmp/herd-home")

	// gh auth mount, only if it exists.
	if home, herr := os.UserHomeDir(); herr == nil {
		ghPath := filepath.Join(home, ".config", "gh")
		if fi, serr := os.Stat(ghPath); serr == nil && fi.IsDir() {
			dockerArgs = append(dockerArgs, "-v", ghPath+":/tmp/herd-home/.config/gh:ro")
		}
	}

	// auth/env passthrough: only -e VAR for vars actually set in host env.
	for _, k := range passEnv {
		if _, ok := os.LookupEnv(k); ok {
			dockerArgs = append(dockerArgs, "-e", k)
		}
	}

	dockerArgs = append(dockerArgs, "-e", "HERD_INSIDE_CONTAINER=1")
	dockerArgs = append(dockerArgs, "--entrypoint", "/usr/local/bin/herd")
	dockerArgs = append(dockerArgs, execImageRef(cfg))

	// inner herd args: strip --exec, append the rest.
	dockerArgs = append(dockerArgs, stripExecFlag(args)...)

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	return cmd, nil
}

// runDockerExec builds and runs the docker exec command, wiring stdio and
// returning the inner herd's exit code. It prints a one-time "Pulling..." hint
// to stderr when the image is not already cached locally.
func runDockerExec(ctx context.Context, cfg *config.Config, args []string) (int, error) {
	image := execImageRef(cfg)

	// First-pull hint: if the image is not cached, docker run will pull it.
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	inspect.Stdout = nil
	inspect.Stderr = nil
	if err := inspect.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Pulling %s ... (one-time, may take a minute)\n", image)
	}

	cmd, err := BuildDockerExecCmd(ctx, cfg, args)
	if err != nil {
		return 1, err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 1, err
}
