package design

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostedGitHubAppPermissionTableDocumentsRequiredPermissions(t *testing.T) {
	content, err := os.ReadFile("github-integration.md")
	require.NoError(t, err)

	requiredRows := []string{
		"| Actions | Read and write | Dispatch worker workflows and list runs |",
		"| Administration | Read and write | Create short-lived self-hosted runner registration tokens |",
		"| Checks | Read | Read CI check state for diagnostics |",
		"| Commit statuses | Read and write | Set `Herd Review` |",
		"| Contents | Read and write | Push branches and read repo contents |",
		"| Issues | Read and write | Create/update/label issues and milestones |",
		"| Metadata | Read | Required by GitHub Apps |",
		"| Pull requests | Read and write | Create/update/comment/review PRs |",
		"| Workflows | Read and write | Allow App-authored worker commits that modify workflow files |",
	}
	for _, row := range requiredRows {
		t.Run(row, func(t *testing.T) {
			assert.Contains(t, string(content), row)
		})
	}
}
