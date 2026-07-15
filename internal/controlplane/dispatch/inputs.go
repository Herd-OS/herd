package dispatch

import (
	"fmt"
	"strconv"
)

// WorkflowInputs builds the stable workflow_dispatch input set for req.
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
		"repository_owner":  req.Owner,
		"repository_name":   req.Repo,
		"repository":        req.Owner + "/" + req.Repo,
		"job_id":            jobID,
		"batch_number":      strconv.Itoa(req.BatchNumber),
		"batch_branch":      req.BatchBranch,
		"base_sha":          dispatchBaseSHA(req),
		"head_sha":          req.HeadSHA,
		"expected_head_sha": req.ExpectedHeadSHA,
	}
	if req.IssueNumber > 0 {
		inputs["issue_number"] = strconv.Itoa(req.IssueNumber)
	}
	if req.PRNumber > 0 {
		inputs["pr_number"] = strconv.Itoa(req.PRNumber)
	}
	if req.RunnerLabel != "" {
		inputs["runner_label"] = req.RunnerLabel
	}
	if req.TimeoutMinutes > 0 {
		inputs["timeout_minutes"] = strconv.Itoa(req.TimeoutMinutes)
	}
	if req.ControlPlaneURL != "" {
		inputs["control_plane_url"] = req.ControlPlaneURL
	}
	if req.Reason != "" {
		inputs["reason"] = req.Reason
	}
	return inputs, nil
}
