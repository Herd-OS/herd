package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	herdgit "github.com/herd-os/herd/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateBinaryDiffCoversFileOperations(t *testing.T) {
	dir := initArtifactRepo(t)
	g := herdgit.New(dir)
	base := mustHead(t, g)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "added.txt"), []byte("added\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "modified.txt"), []byte("modified\n"), 0644))
	require.NoError(t, os.Remove(filepath.Join(dir, "deleted.txt")))
	require.NoError(t, os.Rename(filepath.Join(dir, "renamed.txt"), filepath.Join(dir, "renamed-new.txt")))
	require.NoError(t, os.Chmod(filepath.Join(dir, "mode.sh"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0x00, 0x01, 0xfe, 0xff}, 0644))
	require.NoError(t, g.Add("."))
	require.NoError(t, g.Commit("change files"))
	head := mustHead(t, g)

	diff, err := CreateBinaryDiff(dir, base, head)
	require.NoError(t, err)
	text := string(diff)
	assert.Contains(t, text, "added.txt")
	assert.Contains(t, text, "modified.txt")
	assert.Contains(t, text, "deleted.txt")
	assert.Contains(t, text, "renamed-new.txt")
	assert.Contains(t, text, "old mode 100644")
	assert.Contains(t, text, "new mode 100755")
	assert.Contains(t, text, "GIT binary patch")
}
