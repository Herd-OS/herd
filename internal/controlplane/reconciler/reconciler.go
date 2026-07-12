package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
)

const (
	defaultJobTimeout       = 2 * time.Hour
	defaultCommandTimeout   = 10 * time.Minute
	defaultReviewStaleAfter = 24 * time.Hour
	defaultCallbackTimeout  = 30 * time.Minute
	defaultInterval         = 5 * time.Minute
	defaultLimit            = 100
)

type Store interface {
	ListReconcileJobs(ctx context.Context, updatedBefore time.Time, limit int) ([]store.ReconcileJob, error)
	UpdateJobStatus(ctx context.Context, jobID string, status string, metadata json.RawMessage, updatedAt time.Time) error
	ListReconcileCommands(ctx context.Context, createdBefore time.Time, limit int) ([]store.ReconcileCommand, error)
	UpdateCommandStatus(ctx context.Context, repoID int64, commentID int64, commandKey string, status string, metadata json.RawMessage) error
	ListReconcileReviewStates(ctx context.Context, updatedBefore time.Time, limit int) ([]store.ReconcileReviewState, error)
	SetReviewState(ctx context.Context, state store.ReviewState) error
	ListStartedIdempotencyKeys(ctx context.Context, scope string, createdBefore time.Time, limit int) ([]store.IdempotencyKey, error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	ListStartedGitHubMutationAttempts(ctx context.Context, createdBefore time.Time, limit int) ([]store.GitHubMutationAttempt, error)
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type CurrentState interface {
	GetPullRequest(ctx context.Context, repo store.Repository, prNumber int) (platform.PullRequest, error)
	GetHerdReviewStatus(ctx context.Context, repo store.Repository, headSHA string) (platform.CommitStatus, bool, error)
	EnsureHerdReviewStatus(ctx context.Context, repo store.Repository, prNumber int, headSHA string, status platform.CommitStatus) error
}

type CommandRequeuer interface {
	RequeueCommand(ctx context.Context, item store.ReconcileCommand) error
}

type Config struct {
	JobTimeout       time.Duration
	CommandTimeout   time.Duration
	ReviewStaleAfter time.Duration
	CallbackTimeout  time.Duration
	Interval         time.Duration
	Limit            int
}

type Reconciler struct {
	Store      Store
	State      CurrentState
	Commands   CommandRequeuer
	Logger     *log.Logger
	Now        func() time.Time
	Config     Config
	lastReport Report
}

func (r *Reconciler) RunOnce(ctx context.Context) (Report, error) {
	if r.Store == nil {
		return Report{}, fmt.Errorf("reconciler store is required")
	}
	cfg := r.config()
	now := r.now()
	report := Report{StartedAt: now}
	var errs []error

	r.runJobs(ctx, cfg, now, &report, &errs)
	r.runCommands(ctx, cfg, now, &report, &errs)
	r.runReviews(ctx, cfg, now, &report, &errs)
	r.runIdempotency(ctx, cfg, now, &report, &errs)
	r.runMutationAttempts(ctx, cfg, now, &report, &errs)

	report.CompletedAt = r.now()
	r.lastReport = report
	return report, errors.Join(errs...)
}

func (r *Reconciler) Run(ctx context.Context) error {
	cfg := r.config()
	if _, err := r.RunOnce(ctx); err != nil {
		r.logf("reconciler run failed: %v", err)
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				r.logf("reconciler run failed: %v", err)
			}
		}
	}
}

func (r *Reconciler) LastReport() Report {
	return r.lastReport
}

func (r *Reconciler) runJobs(ctx context.Context, cfg Config, now time.Time, report *Report, errs *[]error) {
	items, err := r.Store.ListReconcileJobs(ctx, now.Add(-cfg.JobTimeout), cfg.Limit)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("list reconcile jobs: %w", err))
		return
	}
	for _, item := range items {
		if item.ResultCount > 0 {
			r.add(report, "job", item.Job.JobID, ClassificationComplete, "observed_callback", "job has callback result")
			continue
		}
		d := r.add(report, "job", item.Job.JobID, ClassificationFailedSurfaced, "mark_failed", "job timed out before callback")
		if err := r.Store.UpdateJobStatus(ctx, item.Job.JobID, "failed", diagnosticMetadata(d), now); err != nil {
			*errs = append(*errs, fmt.Errorf("mark timed-out job %s failed: %w", item.Job.JobID, err))
			continue
		}
		r.logf("reconciler surfaced timed-out job job_id=%s repo_id=%d pr=%d", item.Job.JobID, item.Job.RepositoryID, item.Job.PRNumber)
	}
}

func (r *Reconciler) runCommands(ctx context.Context, cfg Config, now time.Time, report *Report, errs *[]error) {
	items, err := r.Store.ListReconcileCommands(ctx, now.Add(-cfg.CommandTimeout), cfg.Limit)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("list reconcile commands: %w", err))
		return
	}
	for _, item := range items {
		if item.IdempotencySeen && item.Idempotency.Status == "completed" {
			r.add(report, "command", item.IdempotencyKey, ClassificationComplete, "none", "command dispatch completed")
			continue
		}
		if item.Command.Status == "retry_needed" {
			r.add(report, "command", item.IdempotencyKey, ClassificationStillNeeded, "none", "command already marked for retry")
			continue
		}
		d := r.add(report, "command", item.IdempotencyKey, ClassificationSafeToRetry, "requeue", "acknowledged command did not complete dispatch")
		if r.Commands != nil {
			if err := r.Commands.RequeueCommand(ctx, item); err != nil {
				*errs = append(*errs, fmt.Errorf("requeue command %s: %w", item.IdempotencyKey, err))
				continue
			}
		}
		if err := r.Store.UpdateCommandStatus(ctx, item.Command.RepositoryID, item.Command.CommentID, item.Command.CommandKey, "retry_needed", diagnosticMetadata(d)); err != nil {
			*errs = append(*errs, fmt.Errorf("mark command %s retry_needed: %w", item.IdempotencyKey, err))
		}
	}
}

func (r *Reconciler) runReviews(ctx context.Context, cfg Config, now time.Time, report *Report, errs *[]error) {
	items, err := r.Store.ListReconcileReviewStates(ctx, now.Add(-cfg.ReviewStaleAfter), cfg.Limit)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("list reconcile review states: %w", err))
		return
	}
	for _, item := range items {
		if r.State == nil {
			r.add(report, "review", reviewID(item.State), ClassificationStillNeeded, "inspect_skipped", "current GitHub state client is not configured")
			continue
		}
		pr, err := r.State.GetPullRequest(ctx, item.Repository, item.State.PRNumber)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("get PR for review %s: %w", reviewID(item.State), err))
			continue
		}
		if pr.State != "open" || pr.HeadSHA != item.State.HeadSHA {
			d := r.add(report, "review", reviewID(item.State), ClassificationStaleAbandoned, "abandon", "review state no longer matches an open PR head")
			item.State.Status = "abandoned"
			item.State.Metadata = diagnosticMetadata(d)
			item.State.UpdatedAt = now
			if err := r.Store.SetReviewState(ctx, item.State); err != nil {
				*errs = append(*errs, fmt.Errorf("abandon stale review %s: %w", reviewID(item.State), err))
			}
			continue
		}
		current, ok, err := r.State.GetHerdReviewStatus(ctx, item.Repository, item.State.HeadSHA)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("get Herd Review status %s: %w", reviewID(item.State), err))
			continue
		}
		if ok && current.State == item.State.Status {
			r.add(report, "review", reviewID(item.State), ClassificationComplete, "none", "Herd Review status is current")
			continue
		}
		status := platform.CommitStatus{
			State:       normalizeReviewStatus(item.State.Status),
			Context:     review.HerdReviewContext,
			Description: "Herd Review status repaired by reconciler",
			TargetURL:   firstMetadataString(item.State.Metadata, "target_url", "workflow_run_url", "run_url"),
		}
		d := r.add(report, "review", reviewID(item.State), ClassificationSafeToRetry, "repair_status", "Herd Review status missing or stale")
		if err := r.State.EnsureHerdReviewStatus(ctx, item.Repository, item.State.PRNumber, item.State.HeadSHA, status); err != nil {
			*errs = append(*errs, fmt.Errorf("repair Herd Review status %s: %w", reviewID(item.State), err))
			continue
		}
		item.State.Metadata = diagnosticMetadata(d)
		item.State.UpdatedAt = now
		if err := r.Store.SetReviewState(ctx, item.State); err != nil {
			*errs = append(*errs, fmt.Errorf("record repaired review %s: %w", reviewID(item.State), err))
		}
	}
}

func (r *Reconciler) runIdempotency(ctx context.Context, cfg Config, now time.Time, report *Report, errs *[]error) {
	keys, err := r.Store.ListStartedIdempotencyKeys(ctx, "issue_comment_command", now.Add(-cfg.CommandTimeout), cfg.Limit)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("list started command idempotency keys: %w", err))
		return
	}
	for _, key := range keys {
		r.add(report, "idempotency_key", key.Key, ClassificationSafeToRetry, "surface_started", "command idempotency key is still started")
	}
}

func (r *Reconciler) runMutationAttempts(ctx context.Context, cfg Config, now time.Time, report *Report, errs *[]error) {
	attempts, err := r.Store.ListStartedGitHubMutationAttempts(ctx, now.Add(-cfg.CallbackTimeout), cfg.Limit)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("list started mutation attempts: %w", err))
		return
	}
	for _, attempt := range attempts {
		idem, err := r.Store.GetIdempotencyKey(ctx, attempt.IdempotencyKey)
		if err == nil && idem.Status == "completed" {
			d := r.add(report, "mutation_attempt", attempt.IdempotencyKey, ClassificationComplete, "suppress_duplicate", "idempotency key is already completed")
			response, _ := json.Marshal(map[string]string{"result_ref": idem.ResultRef})
			if err := r.Store.CompleteGitHubMutationAttempt(ctx, attempt.IdempotencyKey, "completed", response, "", now); err != nil {
				*errs = append(*errs, fmt.Errorf("repair completed stuck mutation attempt %s: %w", attempt.IdempotencyKey, err))
			}
			_ = d
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			*errs = append(*errs, fmt.Errorf("get stuck mutation idempotency key %s: %w", attempt.IdempotencyKey, err))
			continue
		}
		r.add(report, "mutation_attempt", attempt.IdempotencyKey, ClassificationStillNeeded, "inspect_required", "mutation outcome is unknown; current GitHub state must be inspected before retry")
	}
}

func (r *Reconciler) add(report *Report, kind, id string, classification Classification, action, message string) Diagnostic {
	d := Diagnostic{Kind: kind, ID: id, Classification: classification, Action: action, Message: message, RecordedAt: r.now()}
	report.Diagnostics = append(report.Diagnostics, d)
	return d
}

func (r *Reconciler) config() Config {
	cfg := r.Config
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = defaultJobTimeout
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = defaultCommandTimeout
	}
	if cfg.ReviewStaleAfter <= 0 {
		cfg.ReviewStaleAfter = defaultReviewStaleAfter
	}
	if cfg.CallbackTimeout <= 0 {
		cfg.CallbackTimeout = defaultCallbackTimeout
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	return cfg
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) logf(format string, args ...any) {
	logger := r.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "herd-reconciler: ", log.LstdFlags)
	}
	logger.Printf(format, args...)
}

func reviewID(state store.ReviewState) string {
	return fmt.Sprintf("%d/%d/%s", state.RepositoryID, state.PRNumber, state.HeadSHA)
}

func normalizeReviewStatus(status string) string {
	switch status {
	case string(review.ReviewStatusSuccess):
		return string(review.ReviewStatusSuccess)
	case string(review.ReviewStatusFailure), "changes_requested", "timed_out", "unparseable", "max_cycles_hit", "failed":
		return string(review.ReviewStatusFailure)
	default:
		return string(review.ReviewStatusPending)
	}
}

func firstMetadataString(raw json.RawMessage, keys ...string) string {
	var metadata map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &metadata) != nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
