package artifacts

import (
	"fmt"

	herdgit "github.com/herd-os/herd/internal/git"
)

func CreateBinaryDiff(repoDir, baseSHA, headSHA string) ([]byte, error) {
	g := herdgit.New(repoDir)
	diff, err := g.BinaryDiff(baseSHA, headSHA)
	if err != nil {
		return nil, fmt.Errorf("create binary git diff: %w", err)
	}
	return diff, nil
}
