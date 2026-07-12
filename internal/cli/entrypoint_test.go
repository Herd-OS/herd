package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readEntrypoint(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh"))
	require.NoError(t, err)
	return string(data)
}

// TestEntrypoint_RemapsRunnerUIDWhenRoot verifies that the entrypoint's
// privilege-drop block is present, correctly ordered (before the cleanup trap
// and runner registration), and uses gosu to drop into the runner user.
func TestEntrypoint_RemapsRunnerUIDWhenRoot(t *testing.T) {
	script := readEntrypoint(t)

	rootCheck := `if [ "$(id -u)" = "0" ]; then`
	gosuExec := `exec gosu runner:runner "$0" "$@"`
	rejectZero := `RUNNER_UID/RUNNER_GID must not be 0`
	chownPaths := `chown -R "${target_uid}:${target_gid}" /runner /opt/herd /home/runner`

	for _, want := range []string{rootCheck, gosuExec, rejectZero, chownPaths} {
		assert.Contains(t, script, want, "entrypoint must contain %q", want)
	}

	rootIdx := strings.Index(script, rootCheck)
	trapIdx := strings.Index(script, "trap cleanup")
	execIdx := strings.Index(script, "exec ./run.sh")
	require.NotEqual(t, -1, rootIdx)
	require.NotEqual(t, -1, trapIdx)
	require.NotEqual(t, -1, execIdx)
	assert.Less(t, rootIdx, trapIdx,
		"root-drop block must come before the cleanup trap so the trap is "+
			"installed in the dropped child process, not the root parent")
	assert.Less(t, rootIdx, execIdx,
		"root-drop block must come before 'exec ./run.sh'")
}

// TestEntrypoint_BashSyntax shells out to `bash -n` to verify the script
// parses cleanly. Catches mismatched quoting/braces that a contains-check
// would miss.
func TestEntrypoint_BashSyntax(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available on this host")
	}
	path := filepath.Join("..", "..", "images", "base", "entrypoint.herd.sh")
	cmd := exec.Command("bash", "-n", path)
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "bash -n failed: %s", out)
}

func TestEntrypoint_ControlPlaneRunnerRegistration(t *testing.T) {
	script := readEntrypoint(t)

	tests := []struct {
		name string
		want string
	}{
		{
			name: "default control plane URL",
			want: `HERD_CONTROL_PLANE_URL:-https://api.herd-os.com`,
		},
		{
			name: "self hosted URL override",
			want: `normalize_control_plane_url`,
		},
		{
			name: "invalid URL failure",
			want: `HERD_CONTROL_PLANE_URL must be an absolute http or https URL`,
		},
		{
			name: "missing bootstrap token failure",
			want: `HERD_RUNNER_BOOTSTRAP_TOKEN is required for runner registration`,
		},
		{
			name: "registration token exchange endpoint",
			want: `/api/v1/runners/registration-token`,
		},
		{
			name: "registration token parsed from response",
			want: `jq -r '.token // empty'`,
		},
		{
			name: "bootstrap token sent in payload",
			want: `bootstrap_token: env.HERD_RUNNER_BOOTSTRAP_TOKEN`,
		},
		{
			name: "request nonce sent in payload",
			want: `request_nonce: $request_nonce`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, script, tt.want)
		})
	}
}

func TestEntrypoint_RunnerRegistrationDoesNotUseGitHubToken(t *testing.T) {
	script := readEntrypoint(t)
	getTokenIdx := strings.Index(script, "get_token()")
	require.NotEqual(t, -1, getTokenIdx)
	getTokenEnd := strings.Index(script[getTokenIdx:], "\n}\n")
	require.NotEqual(t, -1, getTokenEnd)
	getTokenBody := script[getTokenIdx : getTokenIdx+getTokenEnd]

	assert.NotContains(t, getTokenBody, "GITHUB_TOKEN")
	assert.NotContains(t, getTokenBody, "api.github.com/repos")
	assert.Contains(t, getTokenBody, "HERD_RUNNER_BOOTSTRAP_TOKEN")
	assert.NotContains(t, getTokenBody, `--arg bootstrap_token "$HERD_RUNNER_BOOTSTRAP_TOKEN"`)
	assert.NotContains(t, getTokenBody, `$bootstrap_token`)
}
