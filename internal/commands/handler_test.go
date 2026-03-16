package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- tests ---

func TestHandle(t *testing.T) {
	// Ensure a clean registry for each test by saving and restoring.
	savedRegistry := Registry
	t.Cleanup(func() { Registry = savedRegistry })

	t.Run("valid command dispatch", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}
		Register("fix-ci", func(_ context.Context, _ *HandlerContext, cmd *Command) (string, error) {
			return "fixed CI for you", nil
		})

		issues := &mockIssueService{}
		hctx := &HandlerContext{
			Platform:    &mockPlatform{issues: issues},
			CommentID:   123,
			IssueNumber: 1,
		}

		resp, err := Handle(context.Background(), hctx, "/herd fix-ci", "MEMBER")
		require.NoError(t, err)
		assert.Equal(t, "fixed CI for you", resp)
		assert.Contains(t, issues.reactions, "eyes")
	})

	t.Run("unknown command", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}

		issues := &mockIssueService{}
		hctx := &HandlerContext{
			Platform:    &mockPlatform{issues: issues},
			CommentID:   456,
			IssueNumber: 2,
		}

		resp, err := Handle(context.Background(), hctx, "/herd unknown-cmd", "OWNER")
		require.NoError(t, err)
		assert.Contains(t, resp, "Unknown command")
		assert.Contains(t, resp, "unknown-cmd")
	})

	t.Run("permission denied for NONE association", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}
		Register("fix-ci", func(_ context.Context, _ *HandlerContext, cmd *Command) (string, error) {
			return "done", nil
		})

		hctx := &HandlerContext{
			IssueNumber: 3,
			AuthorLogin: "random-user",
		}

		resp, err := Handle(context.Background(), hctx, "/herd fix-ci", "NONE")
		assert.Error(t, err)
		assert.Empty(t, resp)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("bot user allowed", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}
		Register("fix-ci", func(_ context.Context, _ *HandlerContext, cmd *Command) (string, error) {
			return "bot did it", nil
		})

		issues := &mockIssueService{}
		hctx := &HandlerContext{
			Platform:    &mockPlatform{issues: issues},
			CommentID:   789,
			IssueNumber: 4,
			AuthorLogin: "herd-os[bot]",
		}

		// Bot has NONE association but should still be allowed.
		resp, err := Handle(context.Background(), hctx, "/herd fix-ci", "NONE")
		require.NoError(t, err)
		assert.Equal(t, "bot did it", resp)
		assert.Contains(t, issues.reactions, "eyes")
	})

	t.Run("no command returns empty string", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}

		hctx := &HandlerContext{IssueNumber: 5}
		resp, err := Handle(context.Background(), hctx, "just a regular comment", "OWNER")
		require.NoError(t, err)
		assert.Empty(t, resp)
	})

	t.Run("owner association allowed", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}
		Register("review", func(_ context.Context, _ *HandlerContext, cmd *Command) (string, error) {
			return "review done", nil
		})

		issues := &mockIssueService{}
		hctx := &HandlerContext{
			Platform:    &mockPlatform{issues: issues},
			CommentID:   111,
			IssueNumber: 6,
		}

		resp, err := Handle(context.Background(), hctx, "/herd review", "OWNER")
		require.NoError(t, err)
		assert.Equal(t, "review done", resp)
	})

	t.Run("collaborator association allowed", func(t *testing.T) {
		Registry = map[string]HandlerFunc{}
		Register("review", func(_ context.Context, _ *HandlerContext, cmd *Command) (string, error) {
			return "review done", nil
		})

		issues := &mockIssueService{}
		hctx := &HandlerContext{
			Platform:    &mockPlatform{issues: issues},
			CommentID:   222,
			IssueNumber: 7,
		}

		resp, err := Handle(context.Background(), hctx, "/herd review", "COLLABORATOR")
		require.NoError(t, err)
		assert.Equal(t, "review done", resp)
	})
}
