package commands

import (
	"errors"
	"fmt"
	"strings"
)

type CommandKind string

const (
	CommandPlan      CommandKind = "plan"
	CommandFix       CommandKind = "fix"
	CommandReview    CommandKind = "review"
	CommandFixCI     CommandKind = "fix-ci"
	CommandRetry     CommandKind = "retry"
	CommandIntegrate CommandKind = "integrate"
)

var ErrUnknownCommand = errors.New("unknown herd-os command")

type ParsedCommand struct {
	Kind CommandKind
	Args []string
	Raw  string
}

func ParseMentionCommand(appLogin, body string) (ParsedCommand, bool, error) {
	login := strings.TrimSpace(appLogin)
	if login == "" {
		return ParsedCommand{}, false, fmt.Errorf("app login is required")
	}

	line := firstNonEmptyLine(body)
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

	args := []string(nil)
	if len(fields) > 2 {
		args = append(args, fields[2:]...)
	}
	return ParsedCommand{
		Kind: kind,
		Args: args,
		Raw:  line,
	}, true, nil
}

func isSupportedCommand(kind CommandKind) bool {
	switch kind {
	case CommandPlan, CommandFix, CommandReview, CommandFixCI, CommandRetry, CommandIntegrate:
		return true
	default:
		return false
	}
}

func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
