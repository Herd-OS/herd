package github

import (
	"context"
	"errors"
	"fmt"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

type workflowService struct{ c *Client }

func (s *workflowService) GetWorkflow(ctx context.Context, filename string) (int64, error) {
	workflow, _, err := s.c.gh.Actions.GetWorkflowByFileName(ctx, s.c.owner, s.c.repo, filename)
	if err != nil {
		return 0, fmt.Errorf("getting workflow %s: %w", filename, err)
	}
	return workflow.GetID(), nil
}

func (s *workflowService) Dispatch(ctx context.Context, workflowFile, ref string, inputs map[string]string) (*platform.Run, error) {
	// Convert map[string]string to map[string]interface{} for go-github
	ghInputs := make(map[string]interface{}, len(inputs))
	for k, v := range inputs {
		ghInputs[k] = v
	}

	event := gh.CreateWorkflowDispatchEventRequest{
		Ref:    ref,
		Inputs: ghInputs,
	}

	_, err := s.c.gh.Actions.CreateWorkflowDispatchEventByFileName(ctx, s.c.owner, s.c.repo, workflowFile, event)
	if err != nil {
		return nil, fmt.Errorf("dispatching workflow %s: %w", workflowFile, err)
	}

	// workflow_dispatch is fire-and-forget — GitHub does not return a run ID.
	// The caller must look up the run via ListRuns after dispatch.
	return nil, nil
}

func (s *workflowService) GetRun(ctx context.Context, runID int64) (*platform.Run, error) {
	run, _, err := s.c.gh.Actions.GetWorkflowRunByID(ctx, s.c.owner, s.c.repo, runID)
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", runID, err)
	}
	return mapRun(run), nil
}

func (s *workflowService) ListRuns(ctx context.Context, filters platform.RunFilters) ([]*platform.Run, error) {
	opts := &gh.ListWorkflowRunsOptions{
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}
	if filters.Status != "" {
		opts.Status = filters.Status
	}
	if filters.Branch != "" {
		opts.Branch = filters.Branch
	}

	var result []*platform.Run

	if filters.WorkflowID != 0 {
		runs, _, err := s.c.gh.Actions.ListWorkflowRunsByID(ctx, s.c.owner, s.c.repo, filters.WorkflowID, opts)
		if err != nil {
			return nil, fmt.Errorf("listing workflow runs: %w", err)
		}
		for _, r := range runs.WorkflowRuns {
			result = append(result, mapRun(r))
		}
	} else {
		runs, _, err := s.c.gh.Actions.ListRepositoryWorkflowRuns(ctx, s.c.owner, s.c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing workflow runs: %w", err)
		}
		for _, r := range runs.WorkflowRuns {
			result = append(result, mapRun(r))
		}
	}

	return result, nil
}

func (s *workflowService) CancelRun(ctx context.Context, runID int64) error {
	_, err := s.c.gh.Actions.CancelWorkflowRunByID(ctx, s.c.owner, s.c.repo, runID)
	if err != nil {
		// GitHub returns 202 Accepted, which go-github wraps as AcceptedError.
		// This is not an error — it means the cancellation was accepted.
		var acceptedErr *gh.AcceptedError
		if errors.As(err, &acceptedErr) {
			return nil
		}
		return fmt.Errorf("cancelling run %d: %w", runID, err)
	}
	return nil
}

func mapRun(r *gh.WorkflowRun) *platform.Run {
	run := &platform.Run{
		ID:         r.GetID(),
		Status:     r.GetStatus(),
		Conclusion: r.GetConclusion(),
		URL:        r.GetHTMLURL(),
		CreatedAt:  r.GetCreatedAt().Time,
	}
	// Parse inputs from run name (format: "worker #42" set by run-name in workflow)
	// This is needed because GitHub's REST API doesn't return dispatch inputs on the run object.
	if name := r.GetName(); name != "" {
		run.Inputs = parseRunNameInputs(name)
	}
	return run
}

// parseRunNameInputs extracts inputs from the run name.
// Format: "Herd Worker #<issue_number>" → {"issue_number": "<issue_number>"}
func parseRunNameInputs(name string) map[string]string {
	// Match "Herd Worker #<number>"
	const prefix = "Herd Worker #"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		num := name[len(prefix):]
		// Verify it's a number
		for _, c := range num {
			if c < '0' || c > '9' {
				return nil
			}
		}
		return map[string]string{"issue_number": num}
	}
	return nil
}
