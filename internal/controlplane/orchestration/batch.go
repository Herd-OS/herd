package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/config"
	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/dag"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

const (
	mutationStatusStarted   = "started"
	mutationStatusCompleted = "completed"
	mutationStatusFailed    = "failed"
)

// Store is the durable state required by service-owned orchestration.
type Store interface {
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
	RecordJobResult(ctx context.Context, r store.JobResult) (created bool, err error)
}

// Dispatcher dispatches hosted-service workflow jobs to self-hosted runners.
type Dispatcher interface {
	Dispatch(ctx context.Context, req cpdispatch.DispatchRequest) (cpdispatch.DispatchResult, error)
}

// Service owns GitHub-visible orchestration mutations for a registered repo.
type Service struct {
	Repo       store.Repository
	Platform   platform.Platform
	Store      Store
	Dispatcher Dispatcher
	Clock      func() time.Time
}

func (s Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

func (s Service) validate() error {
	if s.Repo.ID == 0 {
		return fmt.Errorf("repository ID is required")
	}
	if s.Platform == nil {
		return fmt.Errorf("platform client is required")
	}
	if s.Store == nil {
		return fmt.Errorf("orchestration store is required")
	}
	return nil
}

// AdvanceBatch dispatches ready work from the next tier or opens the batch PR
// when every tier is complete.
func (s Service) AdvanceBatch(ctx context.Context, batchNumber int, cfg *config.Config) (AdvanceBatchResult, error) {
	if err := s.validate(); err != nil {
		return AdvanceBatchResult{}, err
	}
	if batchNumber <= 0 {
		return AdvanceBatchResult{}, fmt.Errorf("batch number is required")
	}
	ms, err := s.Platform.Milestones().Get(ctx, batchNumber)
	if err != nil {
		return AdvanceBatchResult{}, fmt.Errorf("get milestone: %w", err)
	}
	allIssues, err := s.Platform.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &batchNumber})
	if err != nil {
		return AdvanceBatchResult{}, fmt.Errorf("list milestone issues: %w", err)
	}
	tiers, err := BuildTiers(allIssues)
	if err != nil {
		return AdvanceBatchResult{}, err
	}
	batchBranch := BatchBranchName(ms)
	for _, tier := range tiers {
		if !tierComplete(tier, allIssues) {
			count, err := s.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
				BatchNumber: batchNumber,
				BatchBranch: batchBranch,
				TierIssues:  tier,
				AllIssues:   allIssues,
				Config:      cfg,
			})
			return AdvanceBatchResult{DispatchedCount: count}, err
		}
	}
	pr, err := s.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: batchNumber,
		Title:       fmt.Sprintf("[herd] %s", ms.Title),
		Body:        BuildBatchPRBody(ms, allIssues, tiers),
		Head:        batchBranch,
		Base:        s.defaultBranch(ms),
	})
	if err != nil {
		return AdvanceBatchResult{}, err
	}
	return AdvanceBatchResult{AllComplete: true, BatchPRNumber: pr.Number}, nil
}

type AdvanceBatchResult struct {
	AllComplete     bool
	DispatchedCount int
	BatchPRNumber   int
}

// DispatchReadyWorkers moves ready/blocked issues to in-progress and dispatches
// worker workflows through the control-plane dispatcher.
func (s Service) DispatchReadyWorkers(ctx context.Context, req DispatchReadyWorkersRequest) (int, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	if s.Dispatcher == nil {
		return 0, fmt.Errorf("dispatcher is required")
	}
	cfg := req.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	remaining := cfg.Workers.MaxConcurrent
	if remaining <= 0 {
		remaining = len(req.TierIssues)
	}
	dispatched := 0
	for _, issueNumber := range req.TierIssues {
		if dispatched >= remaining {
			break
		}
		iss := findIssue(req.AllIssues, issueNumber)
		if iss == nil || issues.HasLabel(iss.Labels, issues.TypeManual) {
			continue
		}
		status := issues.StatusLabel(iss.Labels)
		if status != issues.StatusReady && status != issues.StatusBlocked {
			continue
		}
		if status == issues.StatusBlocked {
			if err := s.mutate(ctx, idempotencyKey("issue-label", s.Repo.ID, issueNumber, issues.StatusBlocked, "remove"), "issue_label_remove", func() (string, error) {
				return "", s.Platform.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusBlocked})
			}); err != nil {
				return dispatched, err
			}
		}
		if status == issues.StatusReady {
			if err := s.mutate(ctx, idempotencyKey("issue-label", s.Repo.ID, issueNumber, issues.StatusReady, "remove"), "issue_label_remove", func() (string, error) {
				return "", s.Platform.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusReady})
			}); err != nil {
				return dispatched, err
			}
		}
		if err := s.mutate(ctx, idempotencyKey("issue-label", s.Repo.ID, issueNumber, issues.StatusInProgress, "add"), "issue_label_add", func() (string, error) {
			return "", s.Platform.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusInProgress})
		}); err != nil {
			return dispatched, err
		}
		result, err := s.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
			RepoID:          s.Repo.ID,
			Owner:           s.Repo.Owner,
			Repo:            s.Repo.Name,
			InstallationID:  s.Repo.InstallationID,
			Kind:            cpdispatch.JobKindWorker,
			WorkflowFile:    "herd-worker.yml",
			Ref:             firstNonEmpty(s.Repo.DefaultBranch, req.Ref, "main"),
			BatchNumber:     req.BatchNumber,
			IssueNumber:     issueNumber,
			BatchBranch:     req.BatchBranch,
			RunnerLabel:     cfg.Workers.RunnerLabel,
			TimeoutMinutes:  cfg.Workers.TimeoutMinutes,
			ControlPlaneURL: cfg.EffectiveControlPlaneURL(),
			Reason:          req.Reason,
		})
		if err != nil {
			_ = s.Platform.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusInProgress})
			_ = s.Platform.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
			return dispatched, fmt.Errorf("dispatch worker for issue #%d: %w", issueNumber, err)
		}
		if result.Created {
			dispatched++
		}
	}
	return dispatched, nil
}

type DispatchReadyWorkersRequest struct {
	BatchNumber int
	BatchBranch string
	TierIssues  []int
	AllIssues   []*platform.Issue
	Ref         string
	Reason      string
	Config      *config.Config
}

// WorkerCallbackResult describes callback freshness classification.
type WorkerCallbackResult struct {
	Created        bool
	Stale          bool
	Classification string
}

// RecordWorkerCallback records worker results idempotently and classifies stale
// callbacks before hosted service handlers mutate GitHub state.
func (s Service) RecordWorkerCallback(ctx context.Context, req WorkerCallbackRequest) (WorkerCallbackResult, error) {
	if s.Store == nil {
		return WorkerCallbackResult{}, fmt.Errorf("orchestration store is required")
	}
	if req.JobID == "" {
		return WorkerCallbackResult{}, fmt.Errorf("job ID is required")
	}
	if req.IdempotencyKey == "" {
		return WorkerCallbackResult{}, fmt.Errorf("callback idempotency key is required")
	}
	classification := "current"
	stale := false
	if req.ExpectedHeadSHA != "" && req.ActualHeadSHA != "" && req.ExpectedHeadSHA != req.ActualHeadSHA {
		classification = "stale_head"
		stale = true
	}
	metadata, err := json.Marshal(map[string]any{
		"classification":    classification,
		"expected_head_sha": req.ExpectedHeadSHA,
		"actual_head_sha":   req.ActualHeadSHA,
		"issue_number":      req.IssueNumber,
	})
	if err != nil {
		return WorkerCallbackResult{}, err
	}
	created, err := s.Store.RecordJobResult(ctx, store.JobResult{
		JobID:          req.JobID,
		IdempotencyKey: req.IdempotencyKey,
		Status:         req.Status,
		ResultRef:      req.ResultRef,
		Metadata:       metadata,
		CreatedAt:      s.now(),
	})
	if err != nil {
		return WorkerCallbackResult{}, err
	}
	return WorkerCallbackResult{Created: created, Stale: stale, Classification: classification}, nil
}

type WorkerCallbackRequest struct {
	JobID           string
	IdempotencyKey  string
	IssueNumber     int
	Status          string
	ResultRef       string
	ExpectedHeadSHA string
	ActualHeadSHA   string
}

func (s Service) mutate(ctx context.Context, key string, mutationType string, fn func() (string, error)) error {
	_, err := s.withIdempotency(ctx, key, mutationType, func() (string, error) {
		resultRef, err := fn()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(resultRef) == "" {
			return "void:" + mutationType, nil
		}
		return resultRef, nil
	})
	return err
}

func (s Service) withIdempotency(ctx context.Context, key string, mutationType string, fn func() (string, error)) (string, error) {
	created, err := s.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     mutationType,
		Status:    mutationStatusStarted,
		CreatedAt: s.now(),
	})
	if err != nil {
		return "", fmt.Errorf("acquire idempotency key: %w", err)
	}
	if !created {
		record, err := s.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return "", fmt.Errorf("get idempotency key: %w", err)
		}
		if record.Status == mutationStatusCompleted && strings.TrimSpace(record.ResultRef) != "" {
			if err := s.repairCompletedMutationAttempt(ctx, key, record.ResultRef); err != nil {
				return "", err
			}
			return record.ResultRef, nil
		}
		if record.Status == mutationStatusCompleted && isVoidMutationType(mutationType) {
			return "", nil
		}
		if resultRef, repaired, err := s.repairIdempotencyFromCompletedMutation(ctx, key, mutationType); repaired || err != nil {
			return resultRef, err
		}
		status := strings.TrimSpace(record.Status)
		if status == mutationStatusFailed {
			if _, err := s.Store.GetGitHubMutationAttempt(ctx, key); errors.Is(err, store.ErrNotFound) {
				return s.withAcquiredIdempotency(ctx, key, mutationType, fn)
			} else if err != nil {
				return "", fmt.Errorf("get mutation attempt: %w", err)
			}
		}
		if status == "" {
			status = "unknown"
		}
		return "", fmt.Errorf("idempotency key %q for %s is %s without a completed result; retry after reconciliation", key, mutationType, status)
	}
	return s.withAcquiredIdempotency(ctx, key, mutationType, fn)
}

func (s Service) withAcquiredIdempotency(ctx context.Context, key string, mutationType string, fn func() (string, error)) (string, error) {
	if err := s.Store.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
		IdempotencyKey: key,
		RepositoryID:   s.Repo.ID,
		MutationType:   mutationType,
		Status:         mutationStatusStarted,
		CreatedAt:      s.now(),
	}); err != nil {
		_ = s.Store.FailIdempotencyKey(ctx, key, err.Error())
		return "", fmt.Errorf("record mutation attempt: %w", err)
	}
	resultRef, err := fn()
	if err != nil {
		_ = s.Store.CompleteGitHubMutationAttempt(ctx, key, mutationStatusFailed, nil, err.Error(), s.now())
		_ = s.Store.FailIdempotencyKey(ctx, key, err.Error())
		return "", err
	}
	response, _ := json.Marshal(map[string]string{"result_ref": resultRef})
	if err := s.Store.CompleteGitHubMutationAttempt(ctx, key, mutationStatusCompleted, response, "", s.now()); err != nil {
		if idemErr := s.Store.CompleteIdempotencyKey(ctx, key, resultRef); idemErr != nil {
			return "", fmt.Errorf("complete mutation attempt: %w; complete idempotency key after mutation attempt failure: %v", err, idemErr)
		}
		return "", fmt.Errorf("complete mutation attempt: %w", err)
	}
	if err := s.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
		return "", fmt.Errorf("complete idempotency key: %w", err)
	}
	return resultRef, nil
}

func (s Service) repairCompletedMutationAttempt(ctx context.Context, key string, resultRef string) error {
	attempt, err := s.Store.GetGitHubMutationAttempt(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get mutation attempt: %w", err)
	}
	if attempt.Status == mutationStatusCompleted {
		return nil
	}
	response, _ := json.Marshal(map[string]string{"result_ref": resultRef})
	if err := s.Store.CompleteGitHubMutationAttempt(ctx, key, mutationStatusCompleted, response, "", s.now()); err != nil {
		return fmt.Errorf("repair mutation attempt: %w", err)
	}
	return nil
}

func (s Service) repairIdempotencyFromCompletedMutation(ctx context.Context, key string, mutationType string) (string, bool, error) {
	attempt, err := s.Store.GetGitHubMutationAttempt(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get mutation attempt: %w", err)
	}
	if attempt.Status != mutationStatusCompleted {
		return "", false, nil
	}
	resultRef := mutationResultRef(attempt.Response)
	if strings.TrimSpace(resultRef) == "" {
		if !isVoidMutationType(mutationType) {
			return "", false, fmt.Errorf("completed mutation attempt %q is missing result_ref; retry after reconciliation", key)
		}
		resultRef = "void:" + mutationType
	}
	if err := s.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
		return "", true, fmt.Errorf("repair idempotency key: %w", err)
	}
	return resultRef, true, nil
}

func isVoidMutationType(mutationType string) bool {
	switch mutationType {
	case "issue_label_add", "issue_label_remove":
		return true
	default:
		return false
	}
}

func mutationResultRef(response json.RawMessage) string {
	var body struct {
		ResultRef string `json:"result_ref"`
	}
	if len(response) == 0 || json.Unmarshal(response, &body) != nil {
		return ""
	}
	return strings.TrimSpace(body.ResultRef)
}

func BuildTiers(allIssues []*platform.Issue) ([][]int, error) {
	d := dag.New()
	for _, iss := range allIssues {
		d.AddNode(iss.Number)
	}
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		for _, dep := range parsed.FrontMatter.DependsOn {
			d.AddEdge(iss.Number, dep)
		}
	}
	return d.Tiers()
}

func BuildBatchPRBody(ms *platform.Milestone, allIssues []*platform.Issue, tiers [][]int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Batch %d: %s\n\n", ms.Number, ms.Title))
	b.WriteString("This PR consolidates the completed HerdOS batch.\n\n")
	for i, tier := range tiers {
		b.WriteString(fmt.Sprintf("### Tier %d\n\n", i+1))
		for _, issueNumber := range tier {
			iss := findIssue(allIssues, issueNumber)
			if iss == nil {
				continue
			}
			b.WriteString(fmt.Sprintf("- #%d %s\n", iss.Number, iss.Title))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func BatchBranchName(ms *platform.Milestone) string {
	return fmt.Sprintf("herd/batch/%d-%s", ms.Number, slugify(ms.Title))
}

func (s Service) defaultBranch(_ *platform.Milestone) string {
	if s.Repo.DefaultBranch != "" {
		return s.Repo.DefaultBranch
	}
	return "main"
}

func tierComplete(tier []int, allIssues []*platform.Issue) bool {
	for _, issueNumber := range tier {
		iss := findIssue(allIssues, issueNumber)
		if iss == nil {
			continue
		}
		if iss.State != "closed" && !issues.HasLabel(iss.Labels, issues.StatusDone) {
			return false
		}
	}
	return true
}

func findIssue(allIssues []*platform.Issue, number int) *platform.Issue {
	for _, iss := range allIssues {
		if iss != nil && iss.Number == number {
			return iss
		}
	}
	return nil
}

func idempotencyKey(parts ...any) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(':')
		}
		switch v := part.(type) {
		case string:
			b.WriteString(v)
		case int:
			b.WriteString(strconv.Itoa(v))
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		default:
			b.WriteString(fmt.Sprint(v))
		}
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var out []rune
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			out = append(out, r)
			lastDash = false
			continue
		}
		if !lastDash {
			out = append(out, '-')
			lastDash = true
		}
	}
	return strings.Trim(string(out), "-")
}
