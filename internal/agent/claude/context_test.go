package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatherDirTree(t *testing.T) {
	tmp := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "a", "b"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(tmp, "c"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(tmp, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("hi"), 0o644))

	out := gatherDirTree(tmp)

	assert.Contains(t, out, "a/")
	assert.Contains(t, out, "  b/")
	assert.Contains(t, out, "c/")
	assert.Contains(t, out, "file.txt")
	assert.NotContains(t, out, ".git")
}

func TestGatherDirTree_ExcludesAllDirs(t *testing.T) {
	tmp := t.TempDir()

	for _, d := range []string{"node_modules", "vendor", "dist", "bin", "src"} {
		require.NoError(t, os.Mkdir(filepath.Join(tmp, d), 0o755))
	}

	out := gatherDirTree(tmp)

	assert.Contains(t, out, "src/")
	assert.NotContains(t, out, "node_modules")
	assert.NotContains(t, out, "vendor")
	assert.NotContains(t, out, "dist/\n") // avoid matching "... (truncated)"
	assert.NotContains(t, out, "bin/\n")
}

func TestGatherDirTree_ExcludesNestedDirs(t *testing.T) {
	tmp := t.TempDir()

	// Create a top-level dir with an excluded child dir
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cmd", ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cmd", "node_modules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cmd", "vendor"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cmd", "src"), 0o755))

	out := gatherDirTree(tmp)

	assert.Contains(t, out, "cmd/")
	assert.Contains(t, out, "  src/")
	assert.NotContains(t, out, ".git")
	assert.NotContains(t, out, "node_modules")
	assert.NotContains(t, out, "vendor")
}

func TestGatherDirTree_Truncation(t *testing.T) {
	tmp := t.TempDir()

	for i := 0; i < 60; i++ {
		name := strings.Repeat("d", 30) + "_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		require.NoError(t, os.Mkdir(filepath.Join(tmp, name), 0o755))
	}

	out := gatherDirTree(tmp)

	assert.LessOrEqual(t, len(out), maxDirTreeChars+len("\n... (truncated)"))
	assert.True(t, strings.HasSuffix(out, "... (truncated)"))
}

func TestGatherKeyFile_Exists(t *testing.T) {
	tmp := t.TempDir()
	content := "hello world"
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "readme.md"), []byte(content), 0o644))

	out := gatherKeyFile(tmp, "readme.md", maxFileChars)

	assert.Equal(t, content, out)
}

func TestGatherKeyFile_NotExists(t *testing.T) {
	tmp := t.TempDir()

	out := gatherKeyFile(tmp, "nope.txt", maxFileChars)

	assert.Equal(t, "", out)
}

func TestGatherKeyFile_Truncation(t *testing.T) {
	tmp := t.TempDir()
	content := strings.Repeat("x", 3000)
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "big.txt"), []byte(content), 0o644))

	out := gatherKeyFile(tmp, "big.txt", maxFileChars)

	assert.LessOrEqual(t, len([]rune(out)), maxFileChars+len([]rune("\n... (truncated)")))
	assert.True(t, strings.HasSuffix(out, "... (truncated)"))
}

func TestGatherKeyFile_UTF8Truncation(t *testing.T) {
	tmp := t.TempDir()
	// Each character is 3 bytes in UTF-8; 10 runes = 30 bytes
	content := strings.Repeat("日", 10)
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "utf8.txt"), []byte(content), 0o644))

	out := gatherKeyFile(tmp, "utf8.txt", 5)

	// Should contain exactly 5 runes + suffix, not split mid-rune
	assert.Equal(t, "日日日日日\n... (truncated)", out)
	assert.True(t, strings.HasSuffix(out, "... (truncated)"))
}

func TestGatherGitLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmp := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, string(out))
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o644))
	run("git", "add", ".")
	run("git", "commit", "-m", "first commit")

	require.NoError(t, os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("b"), 0o644))
	run("git", "add", ".")
	run("git", "commit", "-m", "second commit")

	out := gatherGitLog(tmp)

	assert.Contains(t, out, "first commit")
	assert.Contains(t, out, "second commit")
}

func TestGatherGitLog_NotARepo(t *testing.T) {
	tmp := t.TempDir()

	out := gatherGitLog(tmp)

	assert.Equal(t, "", out)
}

func TestDetectManifestFile(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{
			name:  "go.mod present",
			files: []string{"go.mod"},
			want:  "go.mod",
		},
		{
			name:  "package.json only",
			files: []string{"package.json"},
			want:  "package.json",
		},
		{
			name:  "Cargo.toml only",
			files: []string{"Cargo.toml"},
			want:  "Cargo.toml",
		},
		{
			name:  "go.mod takes priority over package.json",
			files: []string{"go.mod", "package.json"},
			want:  "go.mod",
		},
		{
			name:  "empty dir",
			files: nil,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			for _, f := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(tmp, f), []byte(""), 0o644))
			}
			assert.Equal(t, tt.want, detectManifestFile(tmp))
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		max       int
		want      string
		truncated bool
	}{
		{
			name:  "short string unchanged",
			input: "hello",
			max:   100,
			want:  "hello",
		},
		{
			name:  "exact length unchanged",
			input: "hello",
			max:   5,
			want:  "hello",
		},
		{
			name:      "over length truncated",
			input:     "hello world",
			max:       5,
			want:      "hello\n... (truncated)",
			truncated: true,
		},
		{
			name:      "multi-byte runes not split",
			input:     "日本語テスト",
			max:       3,
			want:      "日本語\n... (truncated)",
			truncated: true,
		},
		{
			name:  "multi-byte under limit unchanged",
			input: "日本",
			max:   5,
			want:  "日本",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.max)
			assert.Equal(t, tt.want, got)
			if tt.truncated {
				assert.True(t, strings.HasSuffix(got, "... (truncated)"))
			}
		})
	}
}
