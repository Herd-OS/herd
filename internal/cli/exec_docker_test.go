package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// containsPair reports whether args contains the consecutive pair a, b.
func containsPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// clearAuthEnv unsets all auth/env passthrough vars for a clean test baseline.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"OPENCODE_AUTH_JSON", "OPENCODE_AUTH_FORCE_SEED", "GITHUB_TOKEN",
		"GH_TOKEN", "HERD_GITHUB_TOKEN",
	} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestBuildDockerExecCmd_Defaults(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{}
	cfg.Agent.Exec = "docker"

	args := []string{"plan", "fix the typo", "--exec", "docker"}
	cmd, err := BuildDockerExecCmd(context.Background(), cfg, args)
	require.NoError(t, err)

	a := cmd.Args
	// cmd.Args[0] is the resolved "docker" path; the docker subcommand follows.
	require.GreaterOrEqual(t, len(a), 2)
	assert.Contains(t, a, "run")
	assert.Contains(t, a, "--rm")
	assert.Contains(t, a, "--init")
	assert.Contains(t, a, "-i")

	uid := os.Getuid()
	gid := os.Getgid()
	assert.True(t, containsPair(a, "--user", fmt.Sprintf("%d:%d", uid, gid)), "user pair missing: %v", a)

	pwd, err := os.Getwd()
	require.NoError(t, err)
	assert.True(t, containsPair(a, "-v", pwd+":/work"), "work mount missing: %v", a)
	assert.True(t, containsPair(a, "-w", "/work"), "workdir missing: %v", a)
	assert.True(t, containsPair(a, "-e", "HERD_INSIDE_CONTAINER=1"), "recursion guard env missing: %v", a)
	assert.True(t, containsPair(a, "-e", "HOME=/tmp/herd-home"), "HOME env missing: %v", a)
	assert.True(t, containsPair(a, "--entrypoint", "/usr/local/bin/herd"), "entrypoint missing: %v", a)
	assert.Contains(t, a, "ghcr.io/herd-os/herd-runner-base:latest")

	// original args, --exec removed, in order at the tail.
	assert.Contains(t, a, "plan")
	assert.Contains(t, a, "fix the typo")
	assert.NotContains(t, a, "--exec")
}

func TestBuildDockerExecCmd_TTYPropagation(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{}
	cmd, err := BuildDockerExecCmd(context.Background(), cfg, []string{"plan"})
	require.NoError(t, err)

	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a TTY; -t-present case is documented but not asserted in CI")
	}
	// Common CI case: stdin is not a TTY, so -t must be absent.
	assert.NotContains(t, cmd.Args, "-t")
}

func TestBuildDockerExecCmd_AuthEnvPassthrough(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	require.NoError(t, os.Unsetenv("OPENAI_API_KEY"))

	cfg := &config.Config{}
	cmd, err := BuildDockerExecCmd(context.Background(), cfg, []string{"plan"})
	require.NoError(t, err)

	a := cmd.Args
	assert.True(t, containsPair(a, "-e", "ANTHROPIC_API_KEY"), "set var should be passed: %v", a)
	assert.False(t, containsPair(a, "-e", "OPENAI_API_KEY"), "unset var must not be passed: %v", a)
	// -e entries carry the NAME only (no value) so docker reads it at runtime.
	assert.NotContains(t, a, "ANTHROPIC_API_KEY=sk-secret")
}

func TestBuildDockerExecCmd_GHConfigMountSkippedWhenMissing(t *testing.T) {
	clearAuthEnv(t)
	cfg := &config.Config{}

	t.Run("missing", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		cmd, err := BuildDockerExecCmd(context.Background(), cfg, []string{"plan"})
		require.NoError(t, err)
		ghMount := filepath.Join(tmp, ".config", "gh") + ":/tmp/herd-home/.config/gh:ro"
		assert.NotContains(t, cmd.Args, ghMount)
	})

	t.Run("present", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".config", "gh"), 0o755))
		cmd, err := BuildDockerExecCmd(context.Background(), cfg, []string{"plan"})
		require.NoError(t, err)
		ghMount := filepath.Join(tmp, ".config", "gh") + ":/tmp/herd-home/.config/gh:ro"
		assert.True(t, containsPair(cmd.Args, "-v", ghMount), "gh mount missing: %v", cmd.Args)
	})
}

func TestBuildDockerExecCmd_ImageOverride(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{}
	cfg.Agent.ExecImage = "myfork/herd-base:custom"

	cmd, err := BuildDockerExecCmd(context.Background(), cfg, []string{"plan"})
	require.NoError(t, err)
	assert.Contains(t, cmd.Args, "myfork/herd-base:custom")
	assert.NotContains(t, cmd.Args, "ghcr.io/herd-os/herd-runner-base:latest")
}

func TestBuildDockerExecCmd_VersionPinning(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"real tag", "v1.2.3", "ghcr.io/herd-os/herd-runner-base:v1.2.3"},
		{"dev", "dev", "ghcr.io/herd-os/herd-runner-base:latest"},
		{"empty", "", "ghcr.io/herd-os/herd-runner-base:latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.version
			assert.Equal(t, tt.want, defaultExecImage())
		})
	}
}

func TestExecImageRef(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })
	version = "dev"

	t.Run("default", func(t *testing.T) {
		cfg := &config.Config{}
		assert.Equal(t, "ghcr.io/herd-os/herd-runner-base:latest", execImageRef(cfg))
	})
	t.Run("override", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Agent.ExecImage = "x/y:z"
		assert.Equal(t, "x/y:z", execImageRef(cfg))
	})
}

func TestStripExecFlag(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "two tokens",
			in:   []string{"plan", "--exec", "docker", "fix typo"},
			want: []string{"plan", "fix typo"},
		},
		{
			name: "single token",
			in:   []string{"plan", "--exec=docker", "fix typo"},
			want: []string{"plan", "fix typo"},
		},
		{
			name: "no exec flag",
			in:   []string{"plan", "--no-dispatch", "fix typo"},
			want: []string{"plan", "--no-dispatch", "fix typo"},
		},
		{
			name: "exec at end with no value",
			in:   []string{"plan", "--exec"},
			want: []string{"plan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.in)
			got := stripExecFlag(in)
			assert.Equal(t, tt.want, got)
			// input slice must not be mutated.
			assert.Equal(t, tt.in, in, "input slice was mutated")
		})
	}
}

func TestExecMode_FlagBeatsEnvBeatsConfig(t *testing.T) {
	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv("HERD_EXEC", "local")
		assert.Equal(t, "docker", resolveExecMode("docker", "local"))
	})
	t.Run("env beats config when flag empty", func(t *testing.T) {
		t.Setenv("HERD_EXEC", "local")
		assert.Equal(t, "local", resolveExecMode("", "docker"))
	})
	t.Run("config wins when flag and env empty", func(t *testing.T) {
		require.NoError(t, os.Unsetenv("HERD_EXEC"))
		assert.Equal(t, "docker", resolveExecMode("", "docker"))
	})
	t.Run("empty everything is local", func(t *testing.T) {
		require.NoError(t, os.Unsetenv("HERD_EXEC"))
		assert.Equal(t, "local", resolveExecMode("", ""))
	})
	t.Run("unknown value is local", func(t *testing.T) {
		require.NoError(t, os.Unsetenv("HERD_EXEC"))
		assert.Equal(t, "local", resolveExecMode("podman", ""))
	})
	t.Run("env docker beats empty flag and config", func(t *testing.T) {
		t.Setenv("HERD_EXEC", "docker")
		assert.Equal(t, "docker", resolveExecMode("", ""))
	})
}

func TestExecMode_RecursionGuard(t *testing.T) {
	t.Run("docker requested but inside container falls back to local", func(t *testing.T) {
		d := decideExecDispatch("docker", "docker", true)
		assert.False(t, d.runDocker, "must not run docker inside a container")
		assert.True(t, d.guardHit, "guard must fire to emit a single warning")
	})
	t.Run("docker requested and not inside container runs docker", func(t *testing.T) {
		d := decideExecDispatch("docker", "", false)
		assert.True(t, d.runDocker)
		assert.False(t, d.guardHit)
	})
	t.Run("local mode never runs docker and never warns", func(t *testing.T) {
		require.NoError(t, os.Unsetenv("HERD_EXEC"))
		d := decideExecDispatch("", "", true)
		assert.False(t, d.runDocker)
		assert.False(t, d.guardHit)
	})
}
