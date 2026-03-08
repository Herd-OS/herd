package cli

import "embed"

//go:embed workflows/*.yml
var workflowFS embed.FS

// WorkflowFiles returns the list of workflow filenames to install.
func WorkflowFiles() []string {
	return []string{
		"herd-worker.yml",
		"herd-monitor.yml",
		"herd-integrator.yml",
	}
}
