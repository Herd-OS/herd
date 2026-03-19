package commands

import (
	"errors"
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
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		var remainingLines string
		if i+1 < len(lines) {
			remainingLines = strings.Join(lines[i+1:], "\n")
		}
		return parseCommandLine(rest, remainingLines)
	}
	return nil
}

func parseCommandLine(line string, remainingBody string) *Command {
	cmd := &Command{}

	// If the line itself starts with a quote, there is no command name.
	if strings.HasPrefix(line, "\"") {
		if strings.Count(line, "\"") >= 2 {
			// Matched quotes but no command name before them.
			return nil
		}
		return &Command{ParseErr: errors.New("unterminated quote in command")}
	}

	parts := strings.Fields(line)
	if len(parts) == 0 {
		return nil
	}
	cmd.Name = parts[0]

	afterName := strings.TrimSpace(line[len(parts[0]):])

	// Extract quoted prompt if the text after the command name starts
	// with a quote (backward compatibility with `/herd fix "prompt"`).
	if strings.HasPrefix(afterName, "\"") {
		rest := afterName[1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			cmd.Prompt = rest[:end]
		} else {
			return &Command{ParseErr: errors.New("unterminated quote in command")}
		}
		return cmd
	}

	// No leading quote — treat remaining text as unquoted prompt
	cmd.Args = parts[1:]

	// Build prompt from text after command name + any subsequent lines
	var promptParts []string
	if afterName != "" {
		promptParts = append(promptParts, afterName)
	}
	if trimmed := strings.TrimRight(remainingBody, "\n "); trimmed != "" {
		promptParts = append(promptParts, trimmed)
	}
	if len(promptParts) > 0 {
		cmd.Prompt = strings.Join(promptParts, "\n")
	}

	return cmd
}
