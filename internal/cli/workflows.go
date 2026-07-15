package cli

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/herd-os/herd/internal/config"
)

//go:embed workflows/*.tmpl
var workflowFS embed.FS

var plainYAMLScalarPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type workflowFile struct {
	SrcName  string // name in the embed FS
	DestName string // name written to .github/workflows/
	Template bool
}

type buildSecretTemplateData struct {
	Name string
	ID   string
}

func workflowFiles() []workflowFile {
	return []workflowFile{
		{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true},
		{SrcName: "herd-review.yml.tmpl", DestName: "herd-review.yml", Template: true},
		{SrcName: "herd-publish-runner.yml.tmpl", DestName: "herd-publish-runner.yml", Template: true},
		{SrcName: "herd-monitor.yml.tmpl", DestName: "herd-monitor.yml", Template: true},
		{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true},
	}
}

// WorkflowFiles returns the list of workflow filenames installed into .github/workflows/.
func WorkflowFiles() []string {
	files := workflowFiles()
	names := make([]string, 0, len(files))
	for _, wf := range files {
		names = append(names, wf.DestName)
	}
	return names
}

// RenderWorkflow returns the content to write to disk for the given workflow source.
// Templated workflows are executed against cfg; static workflows are returned as-is.
func RenderWorkflow(wf workflowFile, cfg *config.Config) ([]byte, error) {
	raw, err := workflowFS.ReadFile("workflows/" + wf.SrcName)
	if err != nil {
		return nil, err
	}
	if !wf.Template {
		return raw, nil
	}
	tmpl, err := template.New(wf.SrcName).Funcs(template.FuncMap{
		"buildSecretID":              config.BuildSecretID,
		"buildSecretIDs":             config.BuildSecretIDs,
		"buildSecretTemplateDataFor": buildSecretTemplateDataFor,
		"joinPlatforms":              joinPlatforms,
		"yamlQuote":                  strconv.Quote,
		"yamlScalar":                 yamlScalar,
	}).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing workflow template %s: %w", wf.SrcName, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("executing workflow template %s: %w", wf.SrcName, err)
	}
	return buf.Bytes(), nil
}

func yamlScalar(value string) string {
	if plainYAMLScalarPattern.MatchString(value) {
		return value
	}
	return strconv.Quote(value)
}

func joinPlatforms(platforms []string) string {
	return strings.Join(platforms, ",")
}

func buildSecretTemplateDataFor(names []string) ([]buildSecretTemplateData, error) {
	ids, err := config.BuildSecretIDs(names)
	if err != nil {
		return nil, err
	}

	secrets := make([]buildSecretTemplateData, 0, len(names))
	for i, name := range names {
		secrets = append(secrets, buildSecretTemplateData{
			Name: name,
			ID:   ids[i],
		})
	}
	return secrets, nil
}
