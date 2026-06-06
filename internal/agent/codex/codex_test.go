package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeCodex creates an executable shell script that mimics the `codex`
// CLI for tests. It records its argv (NUL-separated) to argvDump and its
// received environment (newline-separated) to envDump. If the argv contains
// "--output-last-message <FILE>", it writes outputContent to that file.
// Finally it echoes stdoutLine (when non-empty) and exits with exitCode.
//
// Argv elements are NUL-separated so multi-line argument values (e.g. the
// combined system+initial prompt) round-trip without being split on newlines.
func writeFakeCodex(t *testing.T, outputContent, stdoutLine string, exitCode int) (binary, argvDump, envDump string) {
	t.Helper()
	dir := t.TempDir()
	argvDump = filepath.Join(dir, "argv.bin")
	envDump = filepath.Join(dir, "env.txt")
	binary = filepath.Join(dir, "codex.sh")

	// The script walks argv to find --output-last-message and writes the canned
	// content to the file path that follows it. Env is dumped via `env`.
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(fmt.Sprintf("printf '%%s\\0' \"$@\" > '%s'\n", argvDump))
	b.WriteString(fmt.Sprintf("env > '%s'\n", envDump))
	b.WriteString("out=''\n")
	b.WriteString("prev=''\n")
	b.WriteString("for a in \"$@\"; do\n")
	b.WriteString("  if [ \"$prev\" = \"--output-last-message\" ]; then out=\"$a\"; fi\n")
	b.WriteString("  prev=\"$a\"\n")
	b.WriteString("done\n")
	if outputContent != "" {
		b.WriteString(fmt.Sprintf("if [ -n \"$out\" ]; then printf '%%s' '%s' > \"$out\"; fi\n", outputContent))
	}
	if stdoutLine != "" {
		b.WriteString(fmt.Sprintf("printf '%%s\\n' '%s'\n", stdoutLine))
	}
	b.WriteString(fmt.Sprintf("exit %d\n", exitCode))

	require.NoError(t, os.WriteFile(binary, []byte(b.String()), 0o755))
	return binary, argvDump, envDump
}

// writeFakeInteractiveCodex creates an executable shell script that mimics the
// interactive `codex` CLI for tests. Like writeFakeCodex it records argv
// (NUL-separated) to argvDump and its environment (newline-separated) to
// envDump. Because the interactive path passes the output file path via the
// HERD_PLAN_OUT env var rather than the --output-last-message flag, this fake
// writes outputContent to "$HERD_PLAN_OUT" when that variable is non-empty.
func writeFakeInteractiveCodex(t *testing.T, outputContent string, exitCode int) (binary, argvDump, envDump string) {
	t.Helper()
	dir := t.TempDir()
	argvDump = filepath.Join(dir, "argv.bin")
	envDump = filepath.Join(dir, "env.txt")
	binary = filepath.Join(dir, "codex.sh")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(fmt.Sprintf("printf '%%s\\0' \"$@\" > '%s'\n", argvDump))
	b.WriteString(fmt.Sprintf("env > '%s'\n", envDump))
	if outputContent != "" {
		b.WriteString(fmt.Sprintf("if [ -n \"$HERD_PLAN_OUT\" ]; then printf '%%s' '%s' > \"$HERD_PLAN_OUT\"; fi\n", outputContent))
	}
	b.WriteString(fmt.Sprintf("exit %d\n", exitCode))

	require.NoError(t, os.WriteFile(binary, []byte(b.String()), 0o755))
	return binary, argvDump, envDump
}

// readArgvDump reads a NUL-separated argv dump written by the fake codex
// binary and returns the argv as a slice.
func readArgvDump(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	raw := strings.TrimRight(string(data), "\x00")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}

// readEnvDump parses a `env`-style dump into a map of name->value.
func readEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if i := strings.Index(line, "="); i >= 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

// argvHasFlagValue returns true if argv contains flag immediately followed by
// value.
func argvHasFlagValue(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func TestNewAgent(t *testing.T) {
	tests := []struct {
		name            string
		binaryPath      string
		model           string
		reasoningEffort string
		wantBinary      string
		wantModel       string
		wantEffort      string
	}{
		{
			name:       "defaults applied",
			wantBinary: "codex",
			wantModel:  "",
			wantEffort: "medium",
		},
		{
			name:            "explicit values preserved",
			binaryPath:      "/usr/local/bin/codex",
			model:           "gpt-5-codex",
			reasoningEffort: "high",
			wantBinary:      "/usr/local/bin/codex",
			wantModel:       "gpt-5-codex",
			wantEffort:      "high",
		},
		{
			name:            "empty effort defaults to medium",
			binaryPath:      "codex",
			model:           "gpt-5",
			reasoningEffort: "",
			wantBinary:      "codex",
			wantModel:       "gpt-5",
			wantEffort:      "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAgent(tc.binaryPath, tc.model, tc.reasoningEffort)
			assert.Equal(t, tc.wantBinary, a.BinaryPath)
			assert.Equal(t, tc.wantModel, a.Model)
			assert.Equal(t, tc.wantEffort, a.ReasoningEffort)
		})
	}
}

func TestBuildExecBaseArgs(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		effort        string
		wantContains  []string
		wantNotInArgs []string
	}{
		{
			name:   "default medium, no model",
			effort: "medium",
			wantContains: []string{
				"exec", "--sandbox", "workspace-write", "--skip-git-repo-check",
				"--ephemeral", "--ignore-user-config", "-c", "model_reasoning_effort=medium",
			},
			wantNotInArgs: []string{"--model", "--full-auto"},
		},
		{
			name:   "with model and high effort",
			model:  "gpt-5-codex",
			effort: "high",
			wantContains: []string{
				"exec", "--model", "gpt-5-codex", "-c", "model_reasoning_effort=high",
			},
			wantNotInArgs: []string{"--full-auto"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewAgent("codex", tc.model, tc.effort)
			args := c.buildExecBaseArgs()

			// exec must be the first element.
			require.NotEmpty(t, args)
			assert.Equal(t, "exec", args[0], "exec subcommand must be first")

			for _, want := range tc.wantContains {
				assert.Contains(t, args, want, "args missing %q", want)
			}
			for _, notWant := range tc.wantNotInArgs {
				assert.NotContains(t, args, notWant)
			}
			// Model flag must carry the model value when set.
			if tc.model != "" {
				assert.True(t, argvHasFlagValue(args, "--model", tc.model),
					"--model must be followed by %q", tc.model)
			}
			assert.True(t, argvHasFlagValue(args, "-c", "model_reasoning_effort="+tc.effort),
				"-c must carry the reasoning effort")
		})
	}
}

func TestChildEnv_MapsOpenAIKeyWhenCodexUnset(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-123")

	env := childEnv()

	// CODEX_API_KEY must be appended with the OPENAI_API_KEY value. Because the
	// mapping appends, the LAST CODEX_API_KEY entry wins in the child process.
	got := lastEnvValue(env, "CODEX_API_KEY")
	assert.Equal(t, "sk-openai-123", got)
}

func TestChildEnv_PreservesExplicitCodexKey(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "sk-codex-explicit")
	t.Setenv("OPENAI_API_KEY", "sk-openai-123")

	env := childEnv()

	got := lastEnvValue(env, "CODEX_API_KEY")
	assert.Equal(t, "sk-codex-explicit", got,
		"explicit CODEX_API_KEY must be preserved, not overwritten by OPENAI_API_KEY")
}

func TestChildEnv_NoKeysNoMapping(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	env := childEnv()
	assert.Equal(t, "", lastEnvValue(env, "CODEX_API_KEY"),
		"with neither key set, no CODEX_API_KEY should be added")
}

// lastEnvValue returns the value of the last NAME=value entry in env for the
// given name, or "" if not present. The last entry reflects what the OS uses
// for the child process when duplicate names exist.
func lastEnvValue(env []string, name string) string {
	prefix := name + "="
	val := ""
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			val = strings.TrimPrefix(e, prefix)
			found = true
		}
	}
	if !found {
		return ""
	}
	return val
}
