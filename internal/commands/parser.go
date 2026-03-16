package commands

import (
	"strings"
)

const prefix = "/herd "

// Parse extracts a /herd command from a comment body.
// Returns nil if no command is found.
// Format: /herd <command> [positional-args...] ["optional prompt"]
//
// Examples:
//
//	/herd fix-ci
//	/herd fix-ci "the Node version file is missing"
//	/herd retry 42
//	/herd review "focus on error handling"
//	/herd fix "add missing error check in auth.go"
func Parse(body string) *Command {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		return parseCommandLine(rest)
	}
	return nil
}

func parseCommandLine(line string) *Command {
	cmd := &Command{}

	// Extract quoted prompt if present
	if idx := strings.Index(line, "\""); idx >= 0 {
		rest := line[idx+1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			cmd.Prompt = rest[:end]
			line = strings.TrimSpace(line[:idx])
		}
	}

	// Split remaining into command name and args
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return nil
	}
	cmd.Name = parts[0]
	cmd.Args = parts[1:]
	return cmd
}
