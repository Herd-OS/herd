package commands

import (
	"errors"
	"fmt"
	"strings"
)

type CommandKind string

const (
	CommandPlan             CommandKind = "plan"
	CommandFix              CommandKind = "fix"
	CommandReview           CommandKind = "review"
	CommandFixCI            CommandKind = "fix-ci"
	CommandResolveConflicts CommandKind = "resolve-conflicts"
	CommandDispatch         CommandKind = "dispatch"
	CommandRetry            CommandKind = "retry"
	CommandIntegrate        CommandKind = "integrate"
)

var ErrUnknownCommand = errors.New("unknown herd-os command")

type ParsedCommand struct {
	Kind   CommandKind
	Args   []string
	Prompt string
	Raw    string
}

func ParseMentionCommand(appLogin, body string) (ParsedCommand, bool, error) {
	login := strings.TrimSpace(appLogin)
	if login == "" {
		return ParsedCommand{}, false, fmt.Errorf("app login is required")
	}

	line, remainingBody := firstNonEmptyLineWithRest(body)
	if line == "" || strings.HasPrefix(line, "/herd") {
		return ParsedCommand{}, false, nil
	}

	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ParsedCommand{}, false, nil
	}
	wantMention := "@" + strings.ToLower(login)
	if strings.ToLower(fields[0]) != wantMention {
		return ParsedCommand{}, false, nil
	}
	if len(fields) < 2 {
		return ParsedCommand{}, true, ErrUnknownCommand
	}

	kind := CommandKind(strings.ToLower(fields[1]))
	if !isSupportedCommand(kind) {
		return ParsedCommand{}, true, fmt.Errorf("%w: %s", ErrUnknownCommand, fields[1])
	}

	afterMention := strings.TrimSpace(line[len(fields[0]):])
	afterCommand := strings.TrimSpace(afterMention[len(fields[1]):])
	if strings.HasPrefix(afterCommand, "\"") {
		rest := afterCommand[1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			quotedPrompt := rest[:end]
			trailingPrompt := strings.TrimSpace(rest[end+1:])
			if trailingPrompt != "" {
				quotedPrompt = strings.TrimSpace(quotedPrompt + " " + trailingPrompt)
			}
			return ParsedCommand{
				Kind:   kind,
				Prompt: commandPrompt(quotedPrompt, remainingBody),
				Raw:    line,
			}, true, nil
		}
		return ParsedCommand{}, true, fmt.Errorf("unterminated quote in command")
	}

	args := []string(nil)
	if afterCommand != "" {
		args = strings.Fields(afterCommand)
	}
	prompt := commandPrompt(afterCommand, remainingBody)
	return ParsedCommand{
		Kind:   kind,
		Args:   args,
		Prompt: prompt,
		Raw:    line,
	}, true, nil
}

func commandPrompt(afterCommand, remainingBody string) string {
	var promptParts []string
	if afterCommand != "" {
		promptParts = append(promptParts, afterCommand)
	}
	if trimmed := strings.TrimRightFunc(remainingBody, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}); trimmed != "" {
		promptParts = append(promptParts, trimmed)
	}
	return strings.Join(promptParts, "\n")
}

func isSupportedCommand(kind CommandKind) bool {
	switch kind {
	case CommandFix, CommandReview, CommandFixCI, CommandResolveConflicts, CommandDispatch, CommandRetry, CommandIntegrate:
		return true
	default:
		return false
	}
}

func firstNonEmptyLine(body string) string {
	line, _ := firstNonEmptyLineWithRest(body)
	return line
}

func firstNonEmptyLineWithRest(body string) (string, string) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			if i+1 < len(lines) {
				return line, strings.Join(lines[i+1:], "\n")
			}
			return line, ""
		}
	}
	return "", ""
}
