package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
)

type PendingCommandStore interface {
	ListReconcileCommands(ctx context.Context, createdBefore time.Time, limit int) ([]store.ReconcileCommand, error)
}

type QueueProcessor struct {
	Store   PendingCommandStore
	Handler Handler
	Now     func() time.Time
	Limit   int
}

func (p QueueProcessor) ProcessOnce(ctx context.Context) (int, error) {
	if p.Store == nil {
		return 0, fmt.Errorf("queued command store is not configured")
	}
	now := p.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	items, err := p.Store.ListReconcileCommands(ctx, now(), limit)
	if err != nil {
		return 0, fmt.Errorf("list queued commands: %w", err)
	}
	processed := 0
	for _, item := range items {
		if item.IdempotencySeen && item.Idempotency.Status == "completed" {
			continue
		}
		event, err := queuedIssueCommentEvent(item)
		if err != nil {
			return processed, err
		}
		if _, err := p.Handler.HandleIssueComment(ctx, event); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func queuedIssueCommentEvent(item store.ReconcileCommand) (IssueComment, error) {
	var metadata struct {
		Action            string `json:"action"`
		Raw               string `json:"raw"`
		AuthorAssociation string `json:"author_association"`
		IssueNumber       int    `json:"issue_number"`
		PRNumber          int    `json:"pr_number"`
	}
	if err := json.Unmarshal(item.Command.Metadata, &metadata); err != nil {
		return IssueComment{}, fmt.Errorf("unmarshal queued command metadata: %w", err)
	}
	issueNumber := metadata.IssueNumber
	if issueNumber == 0 {
		issueNumber = metadata.PRNumber
	}
	if issueNumber <= 0 {
		return IssueComment{}, fmt.Errorf("queued command %s/%d/%s is missing issue number", item.Repository.Owner, item.Command.CommentID, item.Command.CommandKey)
	}
	action := strings.TrimSpace(metadata.Action)
	if action == "" {
		action = "created"
	}
	event := IssueComment{
		Action:            action,
		Owner:             item.Repository.Owner,
		Repo:              item.Repository.Name,
		IssueNumber:       issueNumber,
		CommentID:         item.Command.CommentID,
		CommentBody:       metadata.Raw,
		CommentAuthorType: "User",
		SenderLogin:       item.Command.Actor,
		AuthorAssociation: metadata.AuthorAssociation,
	}
	if metadata.PRNumber > 0 {
		event.PullRequestURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", item.Repository.Owner, item.Repository.Name, metadata.PRNumber)
	}
	return event, nil
}
