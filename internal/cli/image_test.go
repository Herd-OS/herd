package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsumerRunnerImage(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		repo  string
		tag   string
		want  string
	}{
		{
			name:  "lowercases owner and repo",
			owner: "Herd-OS",
			repo:  "Herd",
			tag:   "v1.2.0",
			want:  "ghcr.io/herd-os/herd-herd-runner:v1.2.0",
		},
		{
			name:  "already lowercase",
			owner: "acme",
			repo:  "widgets",
			tag:   "latest",
			want:  "ghcr.io/acme/widgets-herd-runner:latest",
		},
		{
			name:  "mixed case repo",
			owner: "MyOrg",
			repo:  "MyRepo",
			tag:   "v0.1.0",
			want:  "ghcr.io/myorg/myrepo-herd-runner:v0.1.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, consumerRunnerImage(tt.owner, tt.repo, tt.tag))
		})
	}
}

type recordedCommand struct {
	dir  string
	name string
	args []string
}

// installRecorder swaps runCommand for a recorder, restoring it via t.Cleanup.
func installRecorder(t *testing.T) *[]recordedCommand {
	t.Helper()
	var recorded []recordedCommand
	orig := runCommand
	runCommand = func(dir, name string, args ...string) error {
		recorded = append(recorded, recordedCommand{dir: dir, name: name, args: args})
		return nil
	}
	t.Cleanup(func() { runCommand = orig })
	return &recorded
}

// withWorkdir changes the process working directory to dir for the duration of
// the test, restoring it afterwards.
func withWorkdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestImageBuild_InvokesDocker(t *testing.T) {
	dir := setupTestGitRepo(t, "git@github.com:Herd-OS/Herd.git")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), []byte("FROM herd-runner-base"), 0644))
	withWorkdir(t, dir)

	recorded := installRecorder(t)

	tag := ""
	cmd := newImageBuildCmd(&tag)
	require.NoError(t, cmd.RunE(cmd, nil))

	require.Len(t, *recorded, 1)
	rec := (*recorded)[0]
	assert.Equal(t, "docker", rec.name)
	want := consumerRunnerImage("Herd-OS", "Herd", runnerImageTag(version))
	assert.Equal(t, []string{"build", "-f", "Dockerfile.herd_runner", "-t", want, "."}, rec.args)
}

func TestImageBuild_TagOverride(t *testing.T) {
	dir := setupTestGitRepo(t, "git@github.com:Herd-OS/Herd.git")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), []byte("FROM herd-runner-base"), 0644))
	withWorkdir(t, dir)

	recorded := installRecorder(t)

	tag := "v9.9.9"
	cmd := newImageBuildCmd(&tag)
	require.NoError(t, cmd.RunE(cmd, nil))

	require.Len(t, *recorded, 1)
	want := consumerRunnerImage("Herd-OS", "Herd", "v9.9.9")
	assert.Equal(t, []string{"build", "-f", "Dockerfile.herd_runner", "-t", want, "."}, (*recorded)[0].args)
}

func TestImageBuild_MissingDockerfile(t *testing.T) {
	dir := setupTestGitRepo(t, "git@github.com:Herd-OS/Herd.git")
	withWorkdir(t, dir)

	recorded := installRecorder(t)

	tag := ""
	cmd := newImageBuildCmd(&tag)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "herd init")
	assert.Empty(t, *recorded)
}

func TestImagePublish_InvokesDocker(t *testing.T) {
	dir := setupTestGitRepo(t, "git@github.com:Herd-OS/Herd.git")
	withWorkdir(t, dir)

	recorded := installRecorder(t)

	tag := ""
	cmd := newImagePublishCmd(&tag)
	require.NoError(t, cmd.RunE(cmd, nil))

	require.Len(t, *recorded, 1)
	rec := (*recorded)[0]
	assert.Equal(t, "docker", rec.name)
	want := consumerRunnerImage("Herd-OS", "Herd", runnerImageTag(version))
	assert.Equal(t, []string{"push", want}, rec.args)
}

// agentNpmPackages are the two agent CLI packages (with pinned versions where
// applicable) that are baked into images/base/Dockerfile at build time.
var agentNpmPackages = []string{
	"@anthropic-ai/claude-code",
	"opencode-ai",
	"@openai/codex",
}

func TestEntrypoint_NoLongerInstallsNpmAgents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	entrypoint := string(data)

	// The entrypoint must no longer install any npm agent CLI.
	assert.NotContains(t, entrypoint, "npm install -g",
		"entrypoint should not run npm install -g (agent CLIs are baked into the image)")
	for _, pkg := range []string{
		"@anthropic-ai/claude-code",
		"opencode-ai",
		"@openai/codex",
	} {
		assert.NotContains(t, entrypoint, pkg,
			"entrypoint should not reference agent package %q", pkg)
	}

	// Regression guard: runner registration must remain intact.
	assert.Contains(t, entrypoint, "config.sh",
		"entrypoint must still register the runner via config.sh")
	assert.Contains(t, entrypoint, "exec ./run.sh",
		"entrypoint must still exec ./run.sh")
}

func TestDockerfile_BakesAgentNpmPackages(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(data)

	for _, pkg := range agentNpmPackages {
		assert.Contains(t, dockerfile, pkg,
			"Dockerfile should bake agent package %q at its pinned version", pkg)
	}
	assert.Contains(t, dockerfile, "npm config set prefix /home/runner/.npm-global",
		"Dockerfile should set the npm prefix before installing agents")
}
