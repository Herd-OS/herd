package cli

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunInitCheck_VersionWarningDoesNotBlock proves the version check inserted
// at the top of runInitCheck does not block on an unreachable network endpoint.
// runInitCheck is run in a non-git temp dir so checkPrerequisites fails fast
// after the version check call site executes; the assertion is on elapsed time,
// not on the specific error returned.
func TestRunInitCheck_VersionWarningDoesNotBlock(t *testing.T) {
	setLatestReleaseURL(t, "http://127.0.0.1:1")
	setVersionCheckTimeout(t, 100*time.Millisecond)
	setVersionForTest(t, "v0.5.3")

	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- runInitCheck()
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 2*time.Second,
			"runInitCheck should not block past the version-check timeout when the endpoint is unreachable")
	case <-time.After(2 * time.Second):
		t.Fatal("runInitCheck blocked past 2s — version check is not respecting its timeout")
	}
}
