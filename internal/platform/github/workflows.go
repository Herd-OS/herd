package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
)

const (
	workflowLogStatusAvailable   = "available"
	workflowLogStatusUnavailable = "unavailable"
	workflowLogStatusNotFetched  = "not_fetched"

	maxWorkflowLogExcerptLines = 120
	maxWorkflowLogExcerptChars = 12000
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

func (s *workflowService) GetRunDiagnostics(ctx context.Context, runID int64) (*platform.WorkflowRunDiagnostics, error) {
	run, _, err := s.c.gh.Actions.GetWorkflowRunByID(ctx, s.c.owner, s.c.repo, runID)
	if err != nil {
		return nil, fmt.Errorf("getting run diagnostics for run %d: %w", runID, err)
	}

	diagnostics := &platform.WorkflowRunDiagnostics{
		RunID:      run.GetID(),
		Workflow:   run.GetName(),
		URL:        run.GetHTMLURL(),
		Conclusion: run.GetConclusion(),
		HeadBranch: run.GetHeadBranch(),
		HeadSHA:    run.GetHeadSHA(),
		LogStatus:  workflowLogStatusNotFetched,
	}

	jobs, jobsErr := s.listWorkflowJobs(ctx, runID)
	if jobsErr == nil {
		diagnostics.Jobs = mapWorkflowJobDiagnostics(jobs)
		diagnostics.Annotations = s.collectWorkflowAnnotations(ctx, jobs)
	}

	logExcerpt, logErr := s.fetchWorkflowLogExcerpt(ctx, run, jobs)
	switch {
	case logErr != nil:
		diagnostics.LogStatus = workflowLogStatusUnavailable
		diagnostics.LogExcerpt = fmt.Sprintf("workflow logs unavailable: %v", logErr)
	case logExcerpt != "":
		diagnostics.LogStatus = workflowLogStatusAvailable
		diagnostics.LogExcerpt = logExcerpt
	case jobsErr != nil && runNeedsLogs(run):
		diagnostics.LogStatus = workflowLogStatusUnavailable
		diagnostics.LogExcerpt = fmt.Sprintf("workflow jobs unavailable: %v", jobsErr)
	default:
		diagnostics.LogStatus = workflowLogStatusNotFetched
	}

	return diagnostics, nil
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

	if filters.WorkflowFileName != "" {
		runs, _, err := s.c.gh.Actions.ListWorkflowRunsByFileName(ctx, s.c.owner, s.c.repo, filters.WorkflowFileName, opts)
		if err != nil {
			return nil, fmt.Errorf("listing workflow runs by file: %w", err)
		}
		for _, r := range runs.WorkflowRuns {
			result = append(result, mapRun(r))
		}
	} else if filters.WorkflowID != 0 {
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
		ID:           r.GetID(),
		WorkflowName: r.GetName(),
		HeadBranch:   r.GetHeadBranch(),
		HeadSHA:      r.GetHeadSHA(),
		Status:       r.GetStatus(),
		Conclusion:   r.GetConclusion(),
		URL:          r.GetHTMLURL(),
		CreatedAt:    r.GetCreatedAt().Time,
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

func (s *workflowService) listWorkflowJobs(ctx context.Context, runID int64) ([]*gh.WorkflowJob, error) {
	opts := &gh.ListWorkflowJobsOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	var jobs []*gh.WorkflowJob
	for {
		page, resp, err := s.c.gh.Actions.ListWorkflowJobs(ctx, s.c.owner, s.c.repo, runID, opts)
		if err != nil {
			return nil, err
		}
		if page != nil {
			jobs = append(jobs, page.Jobs...)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return jobs, nil
}

func mapWorkflowJobDiagnostics(jobs []*gh.WorkflowJob) []platform.WorkflowJobDiagnostic {
	diagnostics := make([]platform.WorkflowJobDiagnostic, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}
		diagnostics = append(diagnostics, platform.WorkflowJobDiagnostic{
			ID:         job.GetID(),
			Name:       job.GetName(),
			URL:        job.GetHTMLURL(),
			Conclusion: job.GetConclusion(),
			Status:     job.GetStatus(),
		})
	}
	return diagnostics
}

func (s *workflowService) collectWorkflowAnnotations(ctx context.Context, jobs []*gh.WorkflowJob) []string {
	var annotations []string
	for _, job := range jobs {
		if job == nil {
			continue
		}
		checkRunID, ok := checkRunIDFromURL(job.GetCheckRunURL())
		if !ok {
			continue
		}
		opts := &gh.ListOptions{PerPage: 100}
		for {
			page, resp, err := s.c.gh.Checks.ListCheckRunAnnotations(ctx, s.c.owner, s.c.repo, checkRunID, opts)
			if err != nil {
				break
			}
			for _, annotation := range page {
				message := strings.TrimSpace(annotation.GetMessage())
				if message == "" {
					continue
				}
				annotations = append(annotations, fmt.Sprintf("%s: %s", job.GetName(), trimDiagnosticString(message, 500)))
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}
	return annotations
}

func checkRunIDFromURL(rawURL string) (int64, bool) {
	if rawURL == "" {
		return 0, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(segments[len(segments)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (s *workflowService) fetchWorkflowLogExcerpt(ctx context.Context, run *gh.WorkflowRun, jobs []*gh.WorkflowJob) (string, error) {
	for _, job := range jobs {
		if !jobNeedsLogs(job) {
			continue
		}
		logURL, _, err := s.c.gh.Actions.GetWorkflowJobLogs(ctx, s.c.owner, s.c.repo, job.GetID(), 0)
		if err != nil {
			return "", err
		}
		return s.downloadLogExcerpt(ctx, logURL)
	}
	if !runNeedsLogs(run) {
		return "", nil
	}
	logURL, _, err := s.c.gh.Actions.GetWorkflowRunLogs(ctx, s.c.owner, s.c.repo, run.GetID(), 0)
	if err != nil {
		return "", err
	}
	return s.downloadLogExcerpt(ctx, logURL)
}

func jobNeedsLogs(job *gh.WorkflowJob) bool {
	if job == nil {
		return false
	}
	switch job.GetConclusion() {
	case "failure", "timed_out", "startup_failure", "action_required":
		return true
	default:
		return false
	}
}

func runNeedsLogs(run *gh.WorkflowRun) bool {
	if run == nil {
		return false
	}
	switch run.GetConclusion() {
	case "failure", "timed_out", "startup_failure", "action_required":
		return true
	default:
		return false
	}
}

func (s *workflowService) downloadLogExcerpt(ctx context.Context, logURL *url.URL) (string, error) {
	if logURL == nil || logURL.String() == "" {
		return "", fmt.Errorf("empty log download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := s.c.gh.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("downloading logs returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkflowLogExcerptChars+1))
	if err != nil {
		return "", err
	}
	return boundLogExcerpt(string(body)), nil
}

func boundLogExcerpt(logs string) string {
	if len(logs) > maxWorkflowLogExcerptChars {
		logs = logs[:maxWorkflowLogExcerptChars]
	}
	lines := strings.Split(logs, "\n")
	if len(lines) > maxWorkflowLogExcerptLines {
		lines = lines[:maxWorkflowLogExcerptLines]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func trimDiagnosticString(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return strings.TrimSpace(value[:maxLen])
}
