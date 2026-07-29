package dispatch

import (
	"encoding/json"
	"fmt"
	"strconv"
)

const maxWorkflowDispatchInputs = 10

// WorkflowInputs builds the stable workflow_dispatch input set for req.
// GitHub workflow_dispatch accepts at most 10 top-level inputs. Keep this
// boundary limited to fields that hosted workflows actually route on; richer
// callback and repair context stays durable in the job/idempotency metadata and
// mutation request records created before the GitHub-visible dispatch call.
func WorkflowInputs(req DispatchRequest, jobID string) (map[string]string, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job ID is required")
	}
	if req.Owner == "" {
		return nil, fmt.Errorf("repository owner is required")
	}
	if req.Repo == "" {
		return nil, fmt.Errorf("repository name is required")
	}
	if req.BatchNumber <= 0 {
		return nil, fmt.Errorf("batch number is required")
	}

	inputs := map[string]string{
		"repository":        req.Owner + "/" + req.Repo,
		"job_id":            jobID,
		"batch_number":      strconv.Itoa(req.BatchNumber),
		"batch_branch":      req.BatchBranch,
		"expected_head_sha": req.ExpectedHeadSHA,
	}
	if req.Kind != JobKindReview && req.IssueNumber > 0 {
		inputs["issue_number"] = strconv.Itoa(req.IssueNumber)
	}
	if req.Kind == JobKindReview && req.PRNumber > 0 {
		inputs["pr_number"] = strconv.Itoa(req.PRNumber)
	}
	if req.Kind != JobKindReview && req.Mode != "" {
		inputs["mode"] = req.Mode
	}
	if req.RunnerLabel != "" {
		inputs["runner_label"] = req.RunnerLabel
	}
	if req.TimeoutMinutes > 0 {
		inputs["timeout_minutes"] = strconv.Itoa(req.TimeoutMinutes)
	}
	if req.Kind == JobKindReview && req.ReviewPrompt != "" {
		inputs["review_prompt"] = req.ReviewPrompt
	}
	if req.Kind == JobKindReview && req.ManualReview {
		inputs["manual_review"] = "true"
	}
	if len(inputs) > maxWorkflowDispatchInputs {
		fields, err := json.Marshal(inputs)
		if err != nil {
			return nil, fmt.Errorf("workflow dispatch input count %d exceeds GitHub limit %d", len(inputs), maxWorkflowDispatchInputs)
		}
		return nil, fmt.Errorf("workflow dispatch input count %d exceeds GitHub limit %d: %s", len(inputs), maxWorkflowDispatchInputs, string(fields))
	}
	return inputs, nil
}
