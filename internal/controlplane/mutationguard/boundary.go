package mutationguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
)

// Store is the durable boundary for GitHub-visible mutations. Callers must
// acquire an idempotency key before using this boundary, then every outbound
// GitHub API mutation must pass through RecordGitHubMutationAttempt and
// TryStartGitHubMutationAttempt immediately before the API call is made.
//
// The shared state machine is:
//   - intent_recorded: durable intent exists, no GitHub-visible call has begun.
//   - call_started: the GitHub-visible call may have happened; this is the
//     post-call-unknown boundary if the process crashes or persistence fails.
//   - completed: the visible effect is known and exact redelivery can replay it.
//   - failed_pre_call: setup failed before a visible call and may be retried.
//   - repair_required: a non-pre-call failure needs operation-specific lookup.
//
// Only intent_recorded and failed_pre_call may be retried automatically; once a
// call is marked call_started, retries must converge by replaying a completed
// mutation record or by an operation-specific repair lookup.
type Store interface {
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
	TryStartGitHubMutationAttempt(ctx context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error)
}

type RunRequest struct {
	Key          string
	RepositoryID int64
	MutationType string
	Request      json.RawMessage
	ResultRef    func(json.RawMessage) string
	Response     func(resultRef string) json.RawMessage
	Accepted     func(resultRef string, response json.RawMessage) string
	Preflight    func() error
	Mutate       func() (string, error)
	Repair       func() (string, bool, error)
	Now          func() time.Time
}

type RunResult struct {
	ResultRef string
	Replayed  bool
}

func Run(ctx context.Context, st Store, req RunRequest) (RunResult, error) {
	if st == nil {
		return RunResult{}, fmt.Errorf("mutation store is required")
	}
	if strings.TrimSpace(req.Key) == "" {
		return RunResult{}, fmt.Errorf("mutation idempotency key is required")
	}
	if strings.TrimSpace(req.MutationType) == "" {
		return RunResult{}, fmt.Errorf("mutation type is required")
	}
	if req.Mutate == nil {
		return RunResult{}, fmt.Errorf("mutation function is required")
	}
	replay, existing, err := recordIntent(ctx, st, req)
	if err != nil {
		return RunResult{}, err
	}
	if existing {
		if result, handled, err := convergeExisting(ctx, st, req, replay); handled || err != nil {
			return result, err
		}
	}
	if req.Preflight != nil {
		if err := req.Preflight(); err != nil {
			_ = st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseFailedPreCall, nil, err.Error(), now(req.Now))
			_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseFailedPreCall+":"+err.Error())
			return RunResult{}, err
		}
	}
	start, err := st.TryStartGitHubMutationAttempt(ctx, req.Key, []string{mutations.PhaseIntentRecorded, mutations.PhaseFailedPreCall}, now(req.Now))
	if err != nil {
		_ = st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseFailedPreCall, nil, err.Error(), now(req.Now))
		_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseFailedPreCall+":"+err.Error())
		return RunResult{}, fmt.Errorf("mark mutation call started: %w", err)
	}
	if !start.Started {
		if mutations.IsCompleted(start.Attempt.Status) {
			resultRef := resultRef(req, start.Attempt.Response)
			if strings.TrimSpace(resultRef) != "" {
				if err := st.CompleteIdempotencyKey(ctx, req.Key, resultRef); err != nil {
					return RunResult{}, fmt.Errorf("repair idempotency key: %w", err)
				}
				return RunResult{ResultRef: resultRef, Replayed: true}, nil
			}
		}
		if result, repaired, err := repairUnknown(ctx, st, req); repaired || err != nil {
			return result, err
		}
		return RunResult{}, fmt.Errorf("mutation attempt %q is %s; repair required before retry", req.Key, mutations.Normalize(start.Attempt.Status))
	}
	resultRef, err := req.Mutate()
	if err != nil {
		var preCallErr mutations.PreCallError
		if errors.As(err, &preCallErr) {
			_ = st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseFailedPreCall, nil, err.Error(), now(req.Now))
			_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseFailedPreCall+":"+err.Error())
			return RunResult{}, err
		}
		_ = st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseRepairRequired, nil, err.Error(), now(req.Now))
		_ = st.FailIdempotencyKey(ctx, req.Key, err.Error())
		return RunResult{}, err
	}
	response := response(req, resultRef)
	if req.Accepted != nil {
		if acceptedRef := strings.TrimSpace(req.Accepted(resultRef, response)); acceptedRef != "" {
			_ = st.FailIdempotencyKey(ctx, req.Key, acceptedRef)
		}
	}
	if err := st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseCompleted, response, "", now(req.Now)); err != nil {
		if idemErr := st.CompleteIdempotencyKey(ctx, req.Key, resultRef); idemErr != nil {
			return RunResult{}, fmt.Errorf("complete mutation attempt: %w; complete idempotency key after mutation attempt failure: %v", err, idemErr)
		}
		return RunResult{}, fmt.Errorf("complete mutation attempt: %w", err)
	}
	if err := st.CompleteIdempotencyKey(ctx, req.Key, resultRef); err != nil {
		return RunResult{}, fmt.Errorf("complete idempotency key: %w", err)
	}
	return RunResult{ResultRef: resultRef}, nil
}

func recordIntent(ctx context.Context, st Store, req RunRequest) (store.GitHubMutationAttempt, bool, error) {
	if err := st.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
		IdempotencyKey: req.Key,
		RepositoryID:   req.RepositoryID,
		MutationType:   req.MutationType,
		Status:         mutations.PhaseIntentRecorded,
		Request:        req.Request,
		CreatedAt:      now(req.Now),
	}); err != nil {
		if !errors.Is(err, store.ErrAlreadyExists) {
			_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseFailedPreCall+":"+err.Error())
			return store.GitHubMutationAttempt{}, false, fmt.Errorf("record mutation attempt: %w", err)
		}
		attempt, readErr := st.GetGitHubMutationAttempt(ctx, req.Key)
		if readErr != nil {
			_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseFailedPreCall+":"+readErr.Error())
			return store.GitHubMutationAttempt{}, false, fmt.Errorf("get existing mutation attempt: %w", readErr)
		}
		if !mutations.IsPreCallRetryable(attempt.Status) && !mutations.IsCompleted(attempt.Status) {
			return attempt, true, nil
		}
		return attempt, true, nil
	}
	return store.GitHubMutationAttempt{}, false, nil
}

func convergeExisting(ctx context.Context, st Store, req RunRequest, attempt store.GitHubMutationAttempt) (RunResult, bool, error) {
	if mutations.IsCompleted(attempt.Status) {
		resultRef := resultRef(req, attempt.Response)
		if strings.TrimSpace(resultRef) != "" {
			if err := st.CompleteIdempotencyKey(ctx, req.Key, resultRef); err != nil {
				return RunResult{}, true, fmt.Errorf("repair idempotency key: %w", err)
			}
			return RunResult{ResultRef: resultRef, Replayed: true}, true, nil
		}
	}
	if mutations.IsPreCallRetryable(attempt.Status) {
		return RunResult{}, false, nil
	}
	if result, repaired, err := repairUnknown(ctx, st, req); repaired || err != nil {
		return result, true, err
	}
	_ = st.FailIdempotencyKey(ctx, req.Key, mutations.PhaseRepairRequired)
	return RunResult{}, true, fmt.Errorf("mutation attempt %q is %s; repair required before retry", req.Key, mutations.Normalize(attempt.Status))
}

func repairUnknown(ctx context.Context, st Store, req RunRequest) (RunResult, bool, error) {
	if req.Repair == nil {
		return RunResult{}, false, nil
	}
	resultRef, repaired, err := req.Repair()
	if err != nil {
		return RunResult{}, true, err
	}
	if !repaired {
		return RunResult{}, false, nil
	}
	response := response(req, resultRef)
	if err := st.CompleteGitHubMutationAttempt(ctx, req.Key, mutations.PhaseCompleted, response, "", now(req.Now)); err != nil {
		return RunResult{}, true, fmt.Errorf("complete repaired mutation attempt: %w", err)
	}
	if err := st.CompleteIdempotencyKey(ctx, req.Key, resultRef); err != nil {
		return RunResult{}, true, fmt.Errorf("complete repaired idempotency key: %w", err)
	}
	return RunResult{ResultRef: resultRef, Replayed: true}, true, nil
}

func resultRef(req RunRequest, response json.RawMessage) string {
	if req.ResultRef != nil {
		return strings.TrimSpace(req.ResultRef(response))
	}
	var body struct {
		ResultRef string `json:"result_ref"`
	}
	if len(response) == 0 || json.Unmarshal(response, &body) != nil {
		return ""
	}
	return strings.TrimSpace(body.ResultRef)
}

func response(req RunRequest, resultRef string) json.RawMessage {
	if req.Response != nil {
		return req.Response(resultRef)
	}
	response, _ := json.Marshal(map[string]string{"result_ref": resultRef})
	return response
}

func now(fn func() time.Time) time.Time {
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}
