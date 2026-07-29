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
		if queuedCommandTerminal(item) {
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

func queuedCommandTerminal(item store.ReconcileCommand) bool {
	switch strings.TrimSpace(item.Command.Status) {
	case "dispatched", StatusIgnored:
		return true
	default:
		return false
	}
}

func queuedIssueCommentEvent(item store.ReconcileCommand) (IssueComment, error) {
	var metadata struct {
		Action            string `json:"action"`
		Raw               string `json:"raw"`
		Prompt            string `json:"prompt"`
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
		CommentBody:       queuedCommentBody(metadata.Raw, metadata.Prompt),
		CommentAuthorType: "User",
		SenderLogin:       item.Command.Actor,
		AuthorAssociation: metadata.AuthorAssociation,
	}
	if metadata.PRNumber > 0 {
		event.PullRequestURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", item.Repository.Owner, item.Repository.Name, metadata.PRNumber)
	}
	return event, nil
}

func queuedCommentBody(raw string, prompt string) string {
	raw = strings.TrimRight(raw, "\r\n")
	prompt = strings.TrimRight(prompt, "\r\n")
	if prompt == "" || strings.Contains(raw, "\n") {
		return raw
	}
	if raw == "" {
		return prompt
	}
	firstLine, rest := firstNonEmptyLineWithRest(prompt)
	if firstLine != "" && (strings.HasSuffix(raw, firstLine) || rawQuotedPrompt(raw) == firstLine) {
		if strings.TrimRight(rest, "\r\n") == "" {
			return raw
		}
		return raw + "\n" + strings.TrimRight(rest, "\r\n")
	}
	return raw + "\n" + prompt
}

func rawQuotedPrompt(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return ""
	}
	afterMention := strings.TrimSpace(raw[len(fields[0]):])
	afterCommand := strings.TrimSpace(afterMention[len(fields[1]):])
	if !strings.HasPrefix(afterCommand, "\"") {
		return ""
	}
	quoted := afterCommand[1:]
	end := strings.Index(quoted, "\"")
	if end < 0 {
		return ""
	}
	firstLinePrompt := quoted[:end]
	if trailingPrompt := strings.TrimSpace(quoted[end+1:]); trailingPrompt != "" {
		firstLinePrompt = strings.TrimSpace(firstLinePrompt + " " + trailingPrompt)
	}
	return firstLinePrompt
}
