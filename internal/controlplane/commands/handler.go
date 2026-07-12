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
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	RecordCommand(ctx context.Context, c store.CommandRecord) (created bool, err error)
}

type QueueStore interface {
	GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	RecordCommand(ctx context.Context, c store.CommandRecord) (created bool, err error)
}

type AppGitHub interface {
	AddIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) (commentID int64, err error)
}

type CommandDispatcher interface {
	DispatchCommand(ctx context.Context, cmd DispatchCommand) error
}

type DispatchCommand struct {
	RepositoryID   int64
	InstallationID int64
	Owner          string
	Repo           string
	IssueNumber    int
	PRNumber       int
	CommentID      int64
	Actor          string
	Command        ParsedCommand
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
		if _, _, _, err := h.recordAndAck(ctx, event, "migration", migrationResponse(h.AppLogin), store.CommandRecord{
			CommandName: "migration",
			Status:      StatusAcknowledged,
		}, false); err != nil {
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
	dispatchable := shouldDispatch(cmd.Kind)
	repo, dispatchPending, idempotencyKey, err := h.recordAndAck(ctx, event, string(cmd.Kind), acknowledgement(cmd), record, dispatchable)
	if err != nil {
		return Result{}, err
	}
	if dispatchPending && dispatchable {
		if h.Dispatcher == nil {
			return Result{}, fmt.Errorf("command dispatcher is not configured")
		}
		if err := h.Dispatcher.DispatchCommand(ctx, DispatchCommand{
			RepositoryID:   repo.ID,
			InstallationID: repo.InstallationID,
			Owner:          event.Owner,
			Repo:           event.Repo,
			IssueNumber:    event.IssueNumber,
			PRNumber:       event.IssueNumber,
			CommentID:      event.CommentID,
			Actor:          event.SenderLogin,
			Command:        cmd,
		}); err != nil {
			return Result{}, fmt.Errorf("dispatch command: %w", err)
		}
		if err := h.Store.CompleteIdempotencyKey(ctx, idempotencyKey, "dispatch:completed"); err != nil {
			return Result{}, fmt.Errorf("complete command idempotency key: %w", err)
		}
	}
	return Result{Status: StatusAcknowledged, Command: cmd}, nil
}

func EnqueueIssueCommentCommand(ctx context.Context, st QueueStore, appLogin string, event IssueComment) error {
	if st == nil {
		return fmt.Errorf("command store is not configured")
	}
	if isBotComment(appLogin, event) {
		return nil
	}
	if event.Action != "created" && event.Action != "edited" {
		return nil
	}
	if isLegacyHerdCommand(event.CommentBody) {
		if !isAuthorized(event.AuthorAssociation) {
			return nil
		}
		return recordQueuedCommand(ctx, st, event, "migration", "migration", nil)
	}
	cmd, ok, err := ParseMentionCommand(appLogin, event.CommentBody)
	if err != nil {
		if ok && !isAuthorized(event.AuthorAssociation) {
			return nil
		}
		return err
	}
	if !ok || !isAuthorized(event.AuthorAssociation) {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{
		"args":               cmd.Args,
		"raw":                cmd.Raw,
		"author_association": event.AuthorAssociation,
		"action":             event.Action,
	})
	if err != nil {
		return fmt.Errorf("marshal command metadata: %w", err)
	}
	return recordQueuedCommand(ctx, st, event, string(cmd.Kind), string(cmd.Kind), metadata)
}

func recordQueuedCommand(ctx context.Context, st QueueStore, event IssueComment, commandKey, commandName string, metadata json.RawMessage) error {
	repo, err := st.GetRepository(ctx, event.Owner, event.Repo)
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err = st.RecordCommand(ctx, store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    event.CommentID,
		CommandKey:   commandKey,
		CommandName:  commandName,
		Actor:        event.SenderLogin,
		Status:       StatusAcknowledged,
		Metadata:     metadata,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("record command: %w", err)
	}
	return nil
}

func (h Handler) recordAndAck(ctx context.Context, event IssueComment, commandKey, ackBody string, record store.CommandRecord, dispatchable bool) (store.Repository, bool, string, error) {
	repo, err := h.Store.GetRepository(ctx, event.Owner, event.Repo)
	if err != nil {
		return store.Repository{}, false, "", fmt.Errorf("get repository: %w", err)
	}

	idempotencyKey := fmt.Sprintf("repo:%d:comment:%d:command:%s", repo.ID, event.CommentID, commandKey)
	created, err := h.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       idempotencyKey,
		Scope:     "issue_comment_command",
		Status:    "started",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return store.Repository{}, false, "", fmt.Errorf("acquire command idempotency key: %w", err)
	}
	if !created {
		record = prepareCommandRecord(repo, event, commandKey, record)
		if _, err := h.Store.RecordCommand(ctx, record); err != nil {
			return store.Repository{}, false, "", fmt.Errorf("record command: %w", err)
		}
		existing, err := h.Store.GetIdempotencyKey(ctx, idempotencyKey)
		if err != nil {
			return store.Repository{}, false, "", fmt.Errorf("get command idempotency key: %w", err)
		}
		return repo, existing.Status != "completed", idempotencyKey, nil
	}

	ackID, err := h.GitHub.AddIssueComment(ctx, event.Owner, event.Repo, event.IssueNumber, ackBody)
	if err != nil {
		return store.Repository{}, false, "", fmt.Errorf("add acknowledgement comment: %w", err)
	}

	record = prepareCommandRecord(repo, event, commandKey, record)
	if _, err := h.Store.RecordCommand(ctx, record); err != nil {
		return store.Repository{}, false, "", fmt.Errorf("record command: %w", err)
	}
	if !dispatchable {
		if err := h.Store.CompleteIdempotencyKey(ctx, idempotencyKey, fmt.Sprintf("issue_comment:%d", ackID)); err != nil {
			return store.Repository{}, false, "", fmt.Errorf("complete command idempotency key: %w", err)
		}
	}
	return repo, true, idempotencyKey, nil
}

func prepareCommandRecord(repo store.Repository, event IssueComment, commandKey string, record store.CommandRecord) store.CommandRecord {
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
	return record
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
	return isBotComment(h.AppLogin, event)
}

func isBotComment(appLogin string, event IssueComment) bool {
	if strings.EqualFold(event.CommentAuthorType, "Bot") {
		return true
	}
	if strings.EqualFold(event.SenderLogin, appLogin+"[bot]") || strings.EqualFold(event.SenderLogin, appLogin) {
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

func shouldDispatch(kind CommandKind) bool {
	switch kind {
	case CommandReview, CommandFix, CommandFixCI:
		return true
	default:
		return false
	}
}

func migrationResponse(appLogin string) string {
	login := strings.TrimSpace(appLogin)
	if login == "" {
		login = "herd-os"
	}
	return fmt.Sprintf("`/herd` comments are no longer dispatched. Use `@%s <command>` instead.", login)
}
