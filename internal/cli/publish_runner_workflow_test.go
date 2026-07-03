package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishRunnerWorkflow_Rendered(t *testing.T) {
	cfg := config.Default()
	cfg.Platform.Owner = "acme"
	cfg.Platform.Repo = "widgets"

	out := renderPublishRunnerWorkflow(t, cfg)
	rendered := string(out)

	wants := []string{
		"name: Herd Publish Runner",
		"packages: write",
		"-f Dockerfile.herd_runner",
		"--platform linux/amd64,linux/arm64",
		"if: vars.HERD_ENABLED == 'true'",
	}
	for _, want := range wants {
		assert.Contains(t, rendered, want, "rendered workflow should contain %q", want)
	}

	// GitHub expressions must be rendered as literal ${{ ... }}, not Go template actions.
	assert.NotContains(t, rendered, "{{`", "template escaping should be fully resolved")
	assert.Contains(t, rendered, "${{ github.repository_owner }}")

	// The `release: types: [published]` trigger remains absent: GitHub
	// silently blocks events caused by the default GITHUB_TOKEN (which
	// creates the release) from cascading into other workflows, so it
	// never fired in practice. The push-on-Dockerfile.herd_runner trigger
	// below is the real auto-rebuild path; release-event triggering would
	// be dead weight even if it worked.
	assert.NotContains(t, rendered, "types: [published]", "broken release trigger should not be present")
	assert.NotContains(t, rendered, "release:", "broken release trigger should not be present")
	assert.NotContains(t, rendered, "env:", "default publish workflow should not render build secret env mappings")
	assert.NotContains(t, rendered, "--secret", "default publish workflow should not render BuildKit secrets")

	// The push-on-Dockerfile.herd_runner trigger MUST be present.
	// Without it, consumer repos that merge an `Update HerdOS to <tag>`
	// PR (which bumps Dockerfile.herd_runner's FROM line) get no
	// automatic wrapper-image rebuild, so workers continue running with
	// stale baked-in agent CLIs and project-specific tools until a
	// maintainer manually fires `gh workflow run
	// herd-publish-runner.yml`. The trigger was briefly removed in #713
	// because of a duplicate-build concern that only applies to
	// herd-os/herd itself (which has a release.yml that ALSO rebuilds
	// the wrapper); consumer repos have no release.yml and need this
	// trigger as their only auto-rebuild path. See the template comment
	// for the full rationale.
	assert.Contains(t, rendered, "workflow_dispatch:", "workflow_dispatch must remain the manual trigger")
	assert.Contains(t, rendered, "push:", "push trigger must be present for consumer auto-rebuild on Dockerfile.herd_runner changes")
	assert.Contains(t, rendered, "'Dockerfile.herd_runner'", "push paths must scope to Dockerfile.herd_runner so unrelated pushes don't trigger image rebuilds")
	assert.Contains(t, rendered, "branches: [ main ]", "push trigger must be scoped to main so feature-branch pushes don't fire it")
}

func TestPublishRunnerWorkflow_RunsOn(t *testing.T) {
	t.Run("default single label matches committed workflow", func(t *testing.T) {
		cfg := config.Default()
		rendered := renderPublishRunnerWorkflow(t, cfg)

		assert.Contains(t, string(rendered), "runs-on: ubuntu-latest")
		assert.NotContains(t, string(rendered), `runs-on: "ubuntu-latest"`)
		assert.NotContains(t, string(rendered), "runs-on: [")

		onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-publish-runner.yml"))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(rendered, onDisk),
			"rendered publish runner workflow with default config must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)
	})

	tests := []struct {
		name   string
		runsOn []string
		want   string
	}{
		{
			name:   "multi label flow list",
			runsOn: []string{"self-hosted", "herd-publisher"},
			want:   `runs-on: ["self-hosted", "herd-publisher"]`,
		},
		{
			name:   "quoted labels",
			runsOn: []string{"self-hosted", "linux x64", "gpu:large"},
			want:   `runs-on: ["self-hosted", "linux x64", "gpu:large"]`,
		},
		{
			name:   "escaping guard",
			runsOn: []string{"self-hosted", `label"quote`, `path\\runner`},
			want:   `runs-on: ["self-hosted", ` + strconv.Quote(`label"quote`) + `, ` + strconv.Quote(`path\\runner`) + `]`,
		},
		{
			name:   "single label with space",
			runsOn: []string{"linux x64"},
			want:   `runs-on: "linux x64"`,
		},
		{
			name:   "single label with colon",
			runsOn: []string{"gpu:large"},
			want:   `runs-on: "gpu:large"`,
		},
		{
			name:   "single label with quote",
			runsOn: []string{`label"quote`},
			want:   `runs-on: ` + strconv.Quote(`label"quote`),
		},
		{
			name:   "single label with backslash",
			runsOn: []string{`path\\runner`},
			want:   `runs-on: ` + strconv.Quote(`path\\runner`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.ImagePublish.RunsOn = tt.runsOn

			rendered := renderPublishRunnerWorkflow(t, cfg)
			assert.Contains(t, string(rendered), tt.want)
		})
	}
}

func TestPublishRunnerWorkflow_BuildSecrets(t *testing.T) {
	tests := []struct {
		name              string
		platforms         []string
		buildSecrets      []string
		wantBuildCommand  string
		wantPushCommand   string
		notWant           []string
		wantMultilineBase []string
	}{
		{
			name:             "multi-platform buildx with one secret",
			buildSecrets:     []string{"BUNDLE_RUBYGEMS__PKG__GITHUB__COM"},
			wantBuildCommand: "docker buildx build",
			notWant: []string{
				"docker build --platform",
				"docker push ${IMAGE}:latest",
			},
			wantMultilineBase: []string{
				"            --platform linux/amd64,linux/arm64 \\",
				"            -f Dockerfile.herd_runner \\",
				"            -t ${IMAGE}:latest \\",
				"            --push .",
			},
		},
		{
			name:             "multi-platform buildx with multiple secrets preserves order",
			buildSecrets:     []string{"NPM_TOKEN", "GIT_AUTH_TOKEN", "BUNDLE_RUBYGEMS__PKG__GITHUB__COM"},
			wantBuildCommand: "docker buildx build",
			notWant: []string{
				"docker build --platform",
				"docker push ${IMAGE}:latest",
			},
			wantMultilineBase: []string{
				"            --platform linux/amd64,linux/arm64 \\",
				"            -f Dockerfile.herd_runner \\",
				"            -t ${IMAGE}:latest \\",
				"            --push .",
			},
		},
		{
			name:             "single linux amd64 docker build with secrets",
			platforms:        []string{"linux/amd64"},
			buildSecrets:     []string{"NPM_TOKEN", "BUNDLE_RUBYGEMS__PKG__GITHUB__COM"},
			wantBuildCommand: "docker build \\",
			wantPushCommand:  "docker push ${IMAGE}:latest",
			notWant: []string{
				"docker buildx build",
				"--push .",
			},
			wantMultilineBase: []string{
				"            --platform linux/amd64 \\",
				"            -f Dockerfile.herd_runner \\",
				"            -t ${IMAGE}:latest .",
			},
		},
		{
			name:             "single linux arm64 docker build with secrets",
			platforms:        []string{"linux/arm64"},
			buildSecrets:     []string{"GIT_AUTH_TOKEN"},
			wantBuildCommand: "docker build \\",
			wantPushCommand:  "docker push ${IMAGE}:latest",
			notWant: []string{
				"docker buildx build",
				"--push .",
			},
			wantMultilineBase: []string{
				"            --platform linux/arm64 \\",
				"            -f Dockerfile.herd_runner \\",
				"            -t ${IMAGE}:latest .",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			if tt.platforms != nil {
				cfg.ImagePublish.Platforms = tt.platforms
			}
			cfg.ImagePublish.BuildSecrets = tt.buildSecrets

			rendered := string(renderPublishRunnerWorkflow(t, cfg))

			assert.Contains(t, rendered, "        env:\n")
			assert.Contains(t, rendered, tt.wantBuildCommand)
			if tt.wantPushCommand != "" {
				assert.Contains(t, rendered, tt.wantPushCommand)
			}
			for _, want := range tt.wantMultilineBase {
				assert.Contains(t, rendered, want)
			}
			for _, notWant := range tt.notWant {
				assert.NotContains(t, rendered, notWant)
			}
			assert.NotContains(t, rendered, "--build-arg")
			assert.NotContains(t, rendered, "super-secret-local-value")

			wantEnvLines := make([]string, 0, len(tt.buildSecrets))
			wantSecretLines := make([]string, 0, len(tt.buildSecrets))
			for _, name := range tt.buildSecrets {
				id := config.BuildSecretID(name)
				wantEnvLines = append(wantEnvLines, "          "+name+": ${{ secrets."+name+" }}")
				wantSecretLines = append(wantSecretLines, "            --secret id="+id+",env="+name+" \\")
			}
			assertLinesInOrder(t, rendered, wantEnvLines)
			assertLinesInOrder(t, rendered, wantSecretLines)
			for _, want := range append(wantEnvLines, wantSecretLines...) {
				assert.Equal(t, 1, strings.Count(rendered, want), "%q should render exactly once", want)
			}
		})
	}
}

func TestPublishRunnerWorkflow_BuildSecretsTemplateError(t *testing.T) {
	cfg := config.Default()
	cfg.ImagePublish.BuildSecrets = []string{"NPM_TOKEN", "NPM__TOKEN"}

	wf := workflowFile{
		SrcName:  "herd-publish-runner.yml.tmpl",
		DestName: "herd-publish-runner.yml",
		Template: true,
	}

	_, err := RenderWorkflow(wf, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executing workflow template herd-publish-runner.yml.tmpl")
	assert.Contains(t, err.Error(), "duplicate BuildKit secret id")
}

func TestPublishRunnerWorkflow_Platforms(t *testing.T) {
	tests := []struct {
		name      string
		platforms []string
		want      []string
		notWant   []string
	}{
		{
			name: "default multi-platform buildx workflow",
			want: []string{
				"docker/setup-qemu-action@v3",
				"docker/setup-buildx-action@v3",
				"docker buildx build",
				"--platform linux/amd64,linux/arm64",
				"--push .",
			},
			notWant: []string{
				"docker push ${IMAGE}:latest",
			},
		},
		{
			name:      "single linux amd64 plain docker workflow",
			platforms: []string{"linux/amd64"},
			want: []string{
				"docker build --platform linux/amd64 -f Dockerfile.herd_runner -t ${IMAGE}:latest .",
				"docker push ${IMAGE}:latest",
			},
			notWant: []string{
				"docker/setup-qemu-action@v3",
				"docker/setup-buildx-action@v3",
				"docker buildx build",
				"--push .",
			},
		},
		{
			name:      "single linux arm64 plain docker workflow",
			platforms: []string{"linux/arm64"},
			want: []string{
				"docker build --platform linux/arm64 -f Dockerfile.herd_runner -t ${IMAGE}:latest .",
				"docker push ${IMAGE}:latest",
			},
			notWant: []string{
				"docker/setup-qemu-action@v3",
				"docker/setup-buildx-action@v3",
				"docker buildx build",
				"--push .",
			},
		},
		{
			name:      "multi-platform preserves configured order",
			platforms: []string{"linux/arm64", "linux/amd64"},
			want: []string{
				"docker/setup-qemu-action@v3",
				"docker/setup-buildx-action@v3",
				"docker buildx build",
				"--platform linux/arm64,linux/amd64",
				"--push .",
			},
			notWant: []string{
				"docker push ${IMAGE}:latest",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			if tt.platforms != nil {
				cfg.ImagePublish.Platforms = tt.platforms
			}

			rendered := string(renderPublishRunnerWorkflow(t, cfg))
			for _, want := range tt.want {
				assert.Contains(t, rendered, want, "rendered workflow should contain %q", want)
			}
			for _, notWant := range tt.notWant {
				assert.NotContains(t, rendered, notWant, "rendered workflow should not contain %q", notWant)
			}
		})
	}
}

func TestBuildSecretTemplateDataFor(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		want    []buildSecretTemplateData
		wantErr string
	}{
		{
			name:  "empty",
			names: []string{},
			want:  []buildSecretTemplateData{},
		},
		{
			name:  "normalizes and preserves order",
			names: []string{"NPM_TOKEN", "GIT_AUTH_TOKEN", "BUNDLE_RUBYGEMS__PKG__GITHUB__COM"},
			want: []buildSecretTemplateData{
				{Name: "NPM_TOKEN", ID: "npm_token"},
				{Name: "GIT_AUTH_TOKEN", ID: "git_auth_token"},
				{Name: "BUNDLE_RUBYGEMS__PKG__GITHUB__COM", ID: "bundle_rubygems_pkg_github_com"},
			},
		},
		{
			name:    "propagates duplicate normalized id error",
			names:   []string{"NPM_TOKEN", "NPM__TOKEN"},
			wantErr: "duplicate BuildKit secret id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSecretTemplateDataFor(tt.names)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestYAMLScalar(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "default github runner label stays plain",
			value: "ubuntu-latest",
			want:  "ubuntu-latest",
		},
		{
			name:  "underscores and dots stay plain",
			value: "runner_1.2",
			want:  "runner_1.2",
		},
		{
			name:  "space is quoted",
			value: "linux x64",
			want:  `"linux x64"`,
		},
		{
			name:  "colon is quoted",
			value: "gpu:large",
			want:  `"gpu:large"`,
		},
		{
			name:  "quote is escaped",
			value: `label"quote`,
			want:  strconv.Quote(`label"quote`),
		},
		{
			name:  "backslash is escaped",
			value: `path\\runner`,
			want:  strconv.Quote(`path\\runner`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, yamlScalar(tt.value))
		})
	}
}

func assertLinesInOrder(t *testing.T, rendered string, wantLines []string) {
	t.Helper()

	last := -1
	for _, want := range wantLines {
		idx := strings.Index(rendered, want)
		require.NotEqual(t, -1, idx, "rendered workflow should contain %q", want)
		assert.Greater(t, idx, last, "%q should render in config order", want)
		last = idx
	}
}

func renderPublishRunnerWorkflow(t *testing.T, cfg *config.Config) []byte {
	t.Helper()

	wf := workflowFile{
		SrcName:  "herd-publish-runner.yml.tmpl",
		DestName: "herd-publish-runner.yml",
		Template: true,
	}

	out, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)
	return out
}
