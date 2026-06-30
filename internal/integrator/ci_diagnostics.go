package integrator

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/herd-os/herd/internal/platform"
)

func isConfiguredCIWorkflow(name, workflowPath string, workflowID int64, configured []string) bool {
	for _, workflow := range configured {
		if workflow == "" {
			continue
		}
		if name == workflow {
			return true
		}
		if workflowID != 0 && strconv.FormatInt(workflowID, 10) == workflow {
			return true
		}
		if workflowPath == "" {
			continue
		}
		if workflowPath == workflow || path.Base(workflowPath) == workflow {
			return true
		}
		if !strings.Contains(workflow, "/") && workflowPath == ".github/workflows/"+workflow {
			return true
		}
	}
	return false
}

func parseBatchNumberFromBranch(branch string) (int, bool) {
	const prefix = "herd/batch/"
	if !strings.HasPrefix(branch, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(branch, prefix)
	if rest == "" {
		return 0, false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	if end < len(rest) && rest[end] != '-' {
		return 0, false
	}
	number, err := strconv.Atoi(rest[:end])
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func isFailedCIConclusion(conclusion string) bool {
	switch conclusion {
	case "failure", "cancelled", "timed_out", "action_required":
		return true
	default:
		return false
	}
}

func classifyCIFailure(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return "unknown"
	}

	text := strings.ToLower(strings.Join(append([]string{}, diag.Annotations...), "\n") + "\n" + diag.LogExcerpt)
	if containsCodeFailurePattern(text) {
		return "code"
	}
	if containsInfrastructureFailurePattern(text) {
		return "infrastructure"
	}
	if diag.LogStatus == "unavailable" {
		return "infrastructure"
	}
	return "unknown"
}

func renderCIFailureContext(ctx *CIFailureContext) string {
	if ctx == nil {
		return ""
	}

	diag := ctx.Diagnostics
	var b strings.Builder
	b.WriteString("## CI Failure\n\n")
	writeField := func(name, value string) {
		if value != "" {
			b.WriteString(fmt.Sprintf("- %s: %s\n", name, value))
		}
	}
	writeField("Workflow", firstNonEmpty(ctx.Workflow, diagnosticsWorkflow(diag)))
	writeField("Run URL", firstNonEmpty(ctx.URL, diagnosticsURL(diag)))
	writeField("Conclusion", firstNonEmpty(ctx.Conclusion, diagnosticsConclusion(diag)))
	writeField("Head branch", firstNonEmpty(ctx.HeadBranch, diagnosticsHeadBranch(diag)))
	writeField("Head SHA", firstNonEmpty(ctx.HeadSHA, diagnosticsHeadSHA(diag)))

	if diag == nil {
		return strings.TrimSpace(b.String())
	}

	failedJobs := failedWorkflowJobs(diag.Jobs)
	if len(failedJobs) > 0 {
		b.WriteString("\n### Failed Jobs\n\n")
		for _, job := range failedJobs {
			line := fmt.Sprintf("- %s", job.Name)
			if job.Conclusion != "" {
				line += fmt.Sprintf(" (%s)", job.Conclusion)
			}
			if job.URL != "" {
				line += fmt.Sprintf(": %s", job.URL)
			}
			b.WriteString(line + "\n")
		}
	}

	switch diag.LogStatus {
	case "available":
		if diag.LogExcerpt != "" {
			b.WriteString("\n### Log Excerpt\n\n```text\n")
			b.WriteString(boundLogExcerpt(diag.LogExcerpt))
			b.WriteString("\n```\n")
		}
	case "unavailable":
		note := strings.TrimSpace(diag.LogExcerpt)
		if note == "" {
			note = "workflow logs were unavailable"
		}
		b.WriteString("\n### Log Excerpt\n\nUnavailable: ")
		b.WriteString(note)
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

func containsInfrastructureFailurePattern(text string) bool {
	patterns := []string{
		"runner lost communication",
		"lost communication with the server",
		"logs unavailable",
		"log unavailable",
		"workflow logs unavailable",
		"cancelled before tests started",
		"canceled before tests started",
		"runner shutdown",
		"runner shut down",
		"out of disk space",
		"no space left on device",
		"disk full",
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func containsCodeFailurePattern(text string) bool {
	patterns := []string{
		"rspec failed",
		"failures:",
		"expected:",
		"got:",
		"assertion failed",
		"assertionerror",
		"panic:",
		"stack trace",
		"traceback",
		"--- fail:",
		"fail\t",
		" failed)",
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return containsGoTestFailLine(text)
}

func containsGoTestFailLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "fail\t") || strings.HasPrefix(line, "fail ") {
			return true
		}
		if strings.HasPrefix(line, "FAIL:") || strings.HasPrefix(line, "FAIL ") {
			return true
		}
	}
	return false
}

func failedWorkflowJobs(jobs []platform.WorkflowJobDiagnostic) []platform.WorkflowJobDiagnostic {
	var failed []platform.WorkflowJobDiagnostic
	for _, job := range jobs {
		switch job.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required":
			failed = append(failed, job)
		}
	}
	return failed
}

func boundLogExcerpt(excerpt string) string {
	const maxChars = 12000
	const maxLines = 160
	lines := strings.Split(excerpt, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		excerpt = strings.Join(lines, "\n") + "\n[truncated]"
	}
	if len(excerpt) <= maxChars {
		return strings.TrimRightFunc(excerpt, unicode.IsSpace)
	}
	return strings.TrimRightFunc(excerpt[:maxChars], unicode.IsSpace) + "\n[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func diagnosticsWorkflow(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return ""
	}
	return diag.Workflow
}

func diagnosticsURL(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return ""
	}
	return diag.URL
}

func diagnosticsConclusion(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return ""
	}
	return diag.Conclusion
}

func diagnosticsHeadBranch(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return ""
	}
	return diag.HeadBranch
}

func diagnosticsHeadSHA(diag *platform.WorkflowRunDiagnostics) string {
	if diag == nil {
		return ""
	}
	return diag.HeadSHA
}
