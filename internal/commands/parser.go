package commands

import (
	"strings"
	"unicode"
)

// Command represents a parsed /herd command from a comment.
type Command struct {
	Name   string // "fix-ci", "retry", "review", "fix"
	Args   string // e.g., issue number for retry
	Prompt string // optional quoted prompt
}

// Parse extracts a /herd command from a comment body.
// Format: /herd <command> [<args>] ["optional prompt"]
// Returns nil if the comment doesn't contain a /herd command.
func Parse(body string) *Command {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "/herd ") && line != "/herd" {
			continue
		}
		rest := strings.TrimPrefix(line, "/herd")
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			continue
		}

		cmd := &Command{}
		cmd.Name, rest = nextToken(rest)
		if cmd.Name == "" {
			continue
		}

		// Parse remaining tokens: args (unquoted) and optional quoted prompt
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		for rest != "" {
			if rest[0] == '"' || rest[0] == '\'' {
				// Quoted prompt
				quote := rune(rest[0])
				end := strings.IndexRune(rest[1:], quote)
				if end < 0 {
					// Unclosed quote — treat the rest (excluding opening quote) as prompt
					cmd.Prompt = rest[1:]
					rest = ""
				} else {
					cmd.Prompt = rest[1 : end+1]
					rest = strings.TrimLeftFunc(rest[end+2:], unicode.IsSpace)
				}
			} else {
				// Unquoted arg
				var tok string
				tok, rest = nextToken(rest)
				if tok != "" {
					if cmd.Args == "" {
						cmd.Args = tok
					} else {
						cmd.Args += " " + tok
					}
				}
				rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
			}
		}

		return cmd
	}
	return nil
}

// nextToken returns the first whitespace-delimited token and the remainder.
func nextToken(s string) (token, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if s == "" {
		return "", ""
	}
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx:]
}
