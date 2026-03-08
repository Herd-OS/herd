package issues

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type frontMatterWrapper struct {
	Herd FrontMatter `yaml:"herd"`
}

// ParseBody parses a herd issue body into structured data.
func ParseBody(raw string) (*IssueBody, error) {
	fm, markdown, err := splitFrontMatter(raw)
	if err != nil {
		return nil, err
	}

	body := &IssueBody{}

	// Parse front matter
	if fm != "" {
		var wrapper frontMatterWrapper
		if err := yaml.Unmarshal([]byte(fm), &wrapper); err != nil {
			return nil, fmt.Errorf("parsing front matter: %w", err)
		}
		body.FrontMatter = wrapper.Herd
	}

	// Parse markdown sections
	sections := parseSections(markdown)
	body.Task = sections["Task"]
	body.Context = sections["Context"]
	body.ImplementationDetails = sections["Implementation Details"]

	if conventions, ok := sections["Conventions"]; ok {
		body.Conventions = parseBulletList(conventions)
	}

	if ctxDeps, ok := sections["Context from Dependencies"]; ok {
		body.ContextFromDeps = parseBulletList(ctxDeps)
	}

	if criteria, ok := sections["Acceptance Criteria"]; ok {
		body.Criteria = parseChecklist(criteria)
	}

	if files, ok := sections["Files to Modify"]; ok {
		body.FilesToModify = parseFileList(files)
	}

	return body, nil
}

func splitFrontMatter(raw string) (frontMatter, markdown string, err error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "---") {
		return "", raw, nil
	}

	rest := raw[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", raw, nil
	}

	frontMatter = strings.TrimSpace(rest[:idx])
	markdown = strings.TrimSpace(rest[idx+4:])
	return frontMatter, markdown, nil
}

func parseSections(markdown string) map[string]string {
	sections := make(map[string]string)
	lines := strings.Split(markdown, "\n")

	var currentSection string
	var content []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if currentSection != "" {
				sections[currentSection] = strings.TrimSpace(strings.Join(content, "\n"))
			}
			currentSection = strings.TrimPrefix(line, "## ")
			content = nil
		} else {
			content = append(content, line)
		}
	}
	if currentSection != "" {
		sections[currentSection] = strings.TrimSpace(strings.Join(content, "\n"))
	}

	return sections
}

func parseChecklist(text string) []string {
	var items []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- [ ] ") {
			items = append(items, strings.TrimPrefix(line, "- [ ] "))
		} else if strings.HasPrefix(line, "- [x] ") {
			items = append(items, strings.TrimPrefix(line, "- [x] "))
		}
	}
	return items
}

func parseBulletList(text string) []string {
	var items []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			items = append(items, strings.TrimPrefix(line, "- "))
		}
	}
	return items
}

func parseFileList(text string) []string {
	var files []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- `") && strings.HasSuffix(line, "`") {
			file := strings.TrimPrefix(line, "- `")
			file = strings.TrimSuffix(file, "`")
			files = append(files, file)
		}
	}
	return files
}
