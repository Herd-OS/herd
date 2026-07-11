package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
)

const (
	StatusIgnored      = "ignored"
	StatusAcknowledged = "acknowledged"
)

type Store interface {
	GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	RecordCommand(ctx context.Context, c store.CommandRecord) (created bool, err error)
}

type AppGitHub interface {
	AddIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) (commentID int64, err error)
}

type CommandDispatcher interface {
	DispatchCommand(ctx context.Context, cmd DispatchCommand) error
}

type DispatchCommand struct {
	RepositoryID int64
	Owner        string
	Repo         string
	IssueNumber  int
	CommentID    int64
	Actor        string
	Command      ParsedCommand
}

type Handler struct {
	AppLogin   string
	Store      Store
	GitHub     AppGitHub
	Dispatcher CommandDispatcher
}

type IssueComment struct {
	Action            string
	Owner             string
	Repo              string
	IssueNumber       int
	PullRequestURL    string
	CommentID         int64
	CommentBody       string
	CommentAuthorType string
	SenderLogin       string
	AuthorAssociation string
}

type Result struct {
	Status  string
	Command ParsedCommand
}

func (h Handler) HandleIssueComment(ctx context.Context, event IssueComment) (Result, error) {
	if h.Store == nil {
		return Result{}, fmt.Errorf("command store is not configured")
	}
	if h.isBotComment(event) {
		return Result{Status: StatusIgnored}, nil
	}
	if event.Action != "created" && event.Action != "edited" {
		return Result{Status: StatusIgnored}, nil
	}

	if isLegacyHerdCommand(event.CommentBody) {
		if !isAuthorized(event.AuthorAssociation) {
			return Result{Status: StatusIgnored}, nil
		}
		if h.GitHub == nil {
			return Result{}, fmt.Errorf("command GitHub client is not configured")
		}
		if err := h.recordAndAck(ctx, event, "migration", migrationResponse(h.AppLogin), store.CommandRecord{
			CommandName: "migration",
			Status:      StatusAcknowledged,
		}); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusIgnored}, nil
	}

	cmd, ok, err := ParseMentionCommand(h.AppLogin, event.CommentBody)
	if err != nil {
		if ok && !isAuthorized(event.AuthorAssociation) {
			return Result{Status: StatusIgnored}, nil
		}
		return Result{}, err
	}
	if !ok {
		return Result{Status: StatusIgnored}, nil
	}
	if !isAuthorized(event.AuthorAssociation) {
		return Result{Status: StatusIgnored, Command: cmd}, nil
	}
	if h.GitHub == nil {
		return Result{}, fmt.Errorf("command GitHub client is not configured")
	}

	metadata, err := json.Marshal(map[string]any{
		"args":               cmd.Args,
		"raw":                cmd.Raw,
		"author_association": event.AuthorAssociation,
		"action":             event.Action,
	})
	if err != nil {
		return Result{}, fmt.Errorf("marshal command metadata: %w", err)
	}
	record := store.CommandRecord{
		CommandKey:  string(cmd.Kind),
		CommandName: string(cmd.Kind),
		Actor:       event.SenderLogin,
		Status:      StatusAcknowledged,
		Metadata:    metadata,
	}
	if err := h.recordAndAck(ctx, event, string(cmd.Kind), acknowledgement(cmd), record); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusAcknowledged, Command: cmd}, nil
}

func (h Handler) recordAndAck(ctx context.Context, event IssueComment, commandKey, ackBody string, record store.CommandRecord) error {
	repo, err := h.Store.GetRepository(ctx, event.Owner, event.Repo)
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}

	idempotencyKey := fmt.Sprintf("repo:%d:comment:%d:command:%s", repo.ID, event.CommentID, commandKey)
	created, err := h.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       idempotencyKey,
		Scope:     "issue_comment_command",
		Status:    "started",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("acquire command idempotency key: %w", err)
	}
	if !created {
		return nil
	}

	ackID, err := h.GitHub.AddIssueComment(ctx, event.Owner, event.Repo, event.IssueNumber, ackBody)
	if err != nil {
		return fmt.Errorf("add acknowledgement comment: %w", err)
	}

	record.RepositoryID = repo.ID
	record.CommentID = event.CommentID
	record.CommandKey = commandKey
	if record.Actor == "" {
		record.Actor = event.SenderLogin
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if len(record.Metadata) == 0 {
		record.Metadata = json.RawMessage(`{}`)
	}
	if _, err := h.Store.RecordCommand(ctx, record); err != nil {
		return fmt.Errorf("record command: %w", err)
	}
	if err := h.Store.CompleteIdempotencyKey(ctx, idempotencyKey, fmt.Sprintf("issue_comment:%d", ackID)); err != nil {
		return fmt.Errorf("complete command idempotency key: %w", err)
	}
	return nil
}

func isAuthorized(association string) bool {
	switch strings.ToUpper(strings.TrimSpace(association)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func (h Handler) isBotComment(event IssueComment) bool {
	if strings.EqualFold(event.CommentAuthorType, "Bot") {
		return true
	}
	if strings.EqualFold(event.SenderLogin, h.AppLogin+"[bot]") || strings.EqualFold(event.SenderLogin, h.AppLogin) {
		return true
	}
	return false
}

func isLegacyHerdCommand(body string) bool {
	fields := strings.Fields(firstNonEmptyLine(body))
	return len(fields) > 0 && fields[0] == "/herd"
}

func acknowledgement(cmd ParsedCommand) string {
	return fmt.Sprintf("Acknowledged `@herd-os %s`.", cmd.Kind)
}

func migrationResponse(appLogin string) string {
	login := strings.TrimSpace(appLogin)
	if login == "" {
		login = "herd-os"
	}
	return fmt.Sprintf("`/herd` comments are no longer dispatched. Use `@%s <command>` instead.", login)
}
