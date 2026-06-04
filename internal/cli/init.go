package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/herd-os/herd/internal/cli/runner"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	ghplatform "github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var skipLabels, skipWorkflows, checkOnly, dryRun bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up a repo for HerdOS",
		Long:  "Initialize a repository for HerdOS. Creates config, labels, workflow files, and guides secrets setup.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkOnly || dryRun {
				return runInitCheck()
			}
			return runInit(skipLabels, skipWorkflows)
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVar(&skipLabels, "skip-labels", false, "Don't create labels")
	cmd.Flags().BoolVar(&skipWorkflows, "skip-workflows", false, "Don't install workflow files")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Compare what `herd init` would write against on-disk files; exit 1 if any drift is detected. Writes nothing.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Alias for --check")

	return cmd
}

func runInit(skipLabels, skipWorkflows bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	// 1. Check prerequisites
	if err := checkPrerequisites(dir); err != nil {
		return err
	}

	// Detect owner/repo from git remote
	owner, repo, err := detectOwnerRepo(dir)
	if err != nil {
		return fmt.Errorf("could not detect repository: %w — make sure a GitHub remote is configured", err)
	}

	// 2. Create config
	configPath := filepath.Join(dir, config.ConfigFile)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		cfg := config.Default()
		cfg.Platform.Owner = owner
		cfg.Platform.Repo = repo
		if err := config.Save(dir, cfg); err != nil {
			return fmt.Errorf("creating config: %w", err)
		}
		fmt.Println(display.Success("Created " + config.ConfigFile))
	} else {
		fmt.Println(display.Success(config.ConfigFile + " already exists"))
	}

	// Load the (possibly user-edited) config so workflow rendering picks up
	// fields like workers.extra_env.
	cfg, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// 3. Create .herd/ directory with role instruction files
	herdDir := filepath.Join(dir, ".herd")
	if err := os.MkdirAll(herdDir, 0755); err != nil {
		return fmt.Errorf("creating .herd/: %w", err)
	}
	if err := ensureGitignore(dir, ".herd/state/"); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}
	if err := ensureGitignore(dir, ".env"); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}
	if err := createRoleInstructionFiles(herdDir); err != nil {
		return err
	}
	fmt.Println(display.Success("Created .herd/ directory with role instruction files"))

	// 4. Create labels
	if !skipLabels {
		if err := createLabels(owner, repo); err != nil {
			return err
		}
	} else {
		fmt.Println(display.Warning("Skipped label creation"))
	}

	// 5. Install workflow files
	if !skipWorkflows {
		if err := installWorkflows(dir, cfg); err != nil {
			return err
		}
	} else {
		fmt.Println(display.Warning("Skipped workflow installation"))
	}

	// 6. Create runner files
	if err := createRunnerFiles(dir, owner, repo, cfg.Agent.CodexReplicas); err != nil {
		return err
	}

	// 7. Stage and commit init files
	if err := commitInitFiles(dir, owner, repo); err != nil {
		fmt.Println(display.Warning(fmt.Sprintf("Could not commit init files: %s", err)))
		fmt.Println("  You should manually commit and push these files before running herd plan.")
	}

	// 8. Print next steps
	printNextSteps(owner, repo)

	return nil
}

func checkPrerequisites(dir string) error {
	// Check git repo
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("not a git repository (no .git directory)")
	}

	// Check GitHub remote
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("no git remote 'origin' configured")
	}

	remote := strings.TrimSpace(string(out))
	if !strings.Contains(remote, "github.com") {
		return fmt.Errorf("remote 'origin' does not appear to be GitHub: %s", remote)
	}

	return nil
}

func detectOwnerRepo(dir string) (string, string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}

	remote := strings.TrimSpace(string(out))

	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remote, "git@github.com:") {
		path := strings.TrimPrefix(remote, "git@github.com:")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}

	// HTTPS: https://github.com/owner/repo.git
	if strings.Contains(remote, "github.com/") {
		idx := strings.Index(remote, "github.com/")
		path := remote[idx+len("github.com/"):]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}

	return "", "", fmt.Errorf("cannot parse owner/repo from remote: %s", remote)
}

func ensureGitignore(dir, entry string) error {
	gitignorePath := filepath.Join(dir, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if entry already exists
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}

	// Append entry
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(entry + "\n")
	return err
}

func createLabels(owner, repo string) error {
	client, err := ghplatform.New(owner, repo)
	if err != nil {
		return fmt.Errorf("connecting to GitHub: %w", err)
	}

	ctx := context.Background()
	labelSvc := client.Labels()

	existingLabels, err := labelSvc.List(ctx)
	if err != nil {
		// List not implemented yet — try creating all and ignore conflicts
		existingLabels = nil
	}

	existingSet := make(map[string]bool)
	for _, l := range existingLabels {
		existingSet[l.Name] = true
	}

	created := 0
	skipped := 0
	for _, def := range issues.AllLabels() {
		if existingSet[def.Name] {
			skipped++
			continue
		}
		err := labelSvc.Create(ctx, def.Name, def.Color, def.Description)
		if err != nil {
			// If "not implemented", fall back to gh CLI
			if strings.Contains(err.Error(), "not implemented") {
				if err := createLabelViaCLI(owner, repo, def); err != nil {
					fmt.Println(display.Warning(fmt.Sprintf("Could not create label %s: %s", def.Name, err)))
					continue
				}
				created++
				continue
			}
			fmt.Println(display.Warning(fmt.Sprintf("Could not create label %s: %s", def.Name, err)))
			continue
		}
		created++
	}

	total := created + skipped
	if skipped > 0 {
		fmt.Println(display.Success(fmt.Sprintf("Created %d labels (%d already existed, %d total)", created, skipped, total)))
	} else {
		fmt.Println(display.Success(fmt.Sprintf("Created %d labels", created)))
	}

	return nil
}

func createLabelViaCLI(owner, repo string, def issues.LabelDef) error {
	cmd := exec.Command("gh", "label", "create", def.Name,
		"--color", def.Color,
		"--description", def.Description,
		"--repo", owner+"/"+repo,
		"--force")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// managedFile represents a herd-managed file with its target path (relative to repo root)
// and its rendered content.
type managedFile struct {
	Path    string // relative path, e.g. ".github/workflows/herd-worker.yml"
	Content []byte
	Mode    os.FileMode // 0644 for most files
}

// renderRunnerFiles returns the runner-infrastructure files
// (.env.herd.example, docker-compose.herd.yml) in stable order.
// The compose result reflects docker-compose.herd.override.yml if present in dir.
// Dockerfile.herd_runner is intentionally NOT included — it is user-owned.
func renderRunnerFiles(dir, owner, repo string, replicas int) ([]managedFile, error) {
	envExample, err := runner.FS.ReadFile(".env.herd.example")
	if err != nil {
		return nil, fmt.Errorf("reading embedded .env.herd.example: %w", err)
	}

	composeContent, _, mergeErr := renderMergedCompose(dir, owner, repo, replicas)
	if composeContent == nil {
		return nil, fmt.Errorf("rendering docker-compose.herd.yml: %w", mergeErr)
	}
	if mergeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to merge docker-compose.herd.override.yml: %v (using base only)\n", mergeErr)
	}

	return []managedFile{
		{Path: ".env.herd.example", Content: envExample, Mode: 0644},
		{Path: "docker-compose.herd.yml", Content: composeContent, Mode: 0644},
	}, nil
}

// renderWorkflowFiles renders every workflow in workflowFiles() against cfg and
// returns the resulting managedFile slice in workflowFiles() order.
func renderWorkflowFiles(cfg *config.Config) ([]managedFile, error) {
	wfs := workflowFiles()
	out := make([]managedFile, 0, len(wfs))
	for _, wf := range wfs {
		data, err := RenderWorkflow(wf, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, managedFile{
			Path:    filepath.Join(".github", "workflows", wf.DestName),
			Content: data,
			Mode:    0644,
		})
	}
	return out, nil
}

// renderManagedFiles returns the full set of files `herd init` would write for the
// given dir+cfg, EXCLUDING Dockerfile.herd_runner (which is user-owned and only
// created if missing — it is checked separately by existence only).
//
// Reads dir for docker-compose.herd.override.yml so the merged compose result is
// included if an override is present.
func renderManagedFiles(dir, owner, repo string, cfg *config.Config) ([]managedFile, error) {
	runners, err := renderRunnerFiles(dir, owner, repo, cfg.Agent.CodexReplicas)
	if err != nil {
		return nil, err
	}
	workflows, err := renderWorkflowFiles(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]managedFile, 0, len(runners)+len(workflows))
	out = append(out, runners...)
	out = append(out, workflows...)
	return out, nil
}

// renderMergedCompose returns the rendered+merged docker-compose content. If the
// override file is missing or unreadable, returns the base render. If the override
// exists but fails to merge, returns the base content and the merge error so the
// caller can decide whether to surface a warning.
func renderMergedCompose(dir, owner, repo string, replicas int) ([]byte, bool, error) {
	rendered, err := renderDockerCompose(owner, repo, replicas)
	if err != nil {
		return nil, false, fmt.Errorf("rendering docker-compose.herd.yml: %w", err)
	}
	base := []byte(rendered)
	overridePath := filepath.Join(dir, "docker-compose.herd.override.yml")
	overrideData, readErr := os.ReadFile(overridePath)
	if readErr != nil {
		return base, false, nil
	}
	merged, mergeErr := mergeComposeOverride(base, overrideData)
	if mergeErr != nil {
		return base, false, mergeErr
	}
	return merged, true, nil
}

// installManagedFilesOnly writes the herd-managed files (excluding Dockerfile.herd_runner)
// produced by renderManagedFiles to disk. It performs no console output and creates
// any missing parent directories. Used internally by tests and by runInit's file-writing
// portion.
func installManagedFilesOnly(dir, owner, repo string, cfg *config.Config) error {
	files, err := renderManagedFiles(dir, owner, repo, cfg)
	if err != nil {
		return err
	}
	for _, mf := range files {
		full := filepath.Join(dir, mf.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", mf.Path, err)
		}
		if err := os.WriteFile(full, mf.Content, mf.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", mf.Path, err)
		}
	}
	return nil
}

func installWorkflows(dir string, cfg *config.Config) error {
	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("creating .github/workflows/: %w", err)
	}

	files, err := renderWorkflowFiles(cfg)
	if err != nil {
		return err
	}
	for _, mf := range files {
		dest := filepath.Join(dir, mf.Path)
		name := filepath.Base(mf.Path)
		if err := os.WriteFile(dest, mf.Content, mf.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
		fmt.Println(display.Success("Installed " + name))
	}

	return nil
}

// RoleInstructionFiles returns the list of role instruction filenames created by init.
func RoleInstructionFiles() []string {
	return []string{"planner.md", "worker.md", "integrator.md"}
}

func createRoleInstructionFiles(herdDir string) error {
	for _, name := range RoleInstructionFiles() {
		p := filepath.Join(herdDir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte{}, 0644); err != nil {
				return fmt.Errorf("creating .herd/%s: %w", name, err)
			}
		}
	}
	return nil
}

type runnerTemplateData struct {
	Owner    string
	Repo     string
	Replicas int // number of runner replicas to generate; >= 1
}

// runnerBaseImage returns the fully-qualified, version-pinned GHCR reference for
// the minimal herd-runner-base image.
func runnerBaseImage() string {
	return fmt.Sprintf("ghcr.io/herd-os/herd-runner-base:%s", runnerImageTag(version))
}

type herdRunnerTemplateData struct {
	BaseImage string
}

// migrateRunnerDockerfileFrom rewrites a legacy `FROM herd-runner-base[:tag]`
// line in a user-owned Dockerfile.herd_runner to the version-pinned GHCR
// reference. Only the matched FROM line is changed; all other bytes are
// preserved. Lines that already reference ghcr.io, or use a custom base, are
// left untouched. Any trailing tokens on the FROM line (e.g. a multi-stage
// `AS <stage>` alias or a trailing comment) are preserved after the new image
// reference. Returns the (possibly unchanged) content and whether a rewrite
// occurred.
func migrateRunnerDockerfileFrom(content []byte, baseImage string) (newContent []byte, changed bool) {
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		image := fields[1]
		if strings.Contains(image, "ghcr.io") {
			continue
		}
		repo := image
		if idx := strings.IndexByte(image, ':'); idx >= 0 {
			repo = image[:idx]
		}
		if repo != "herd-runner-base" {
			continue
		}
		rewritten := "FROM " + baseImage
		if len(fields) > 2 {
			rewritten += " " + strings.Join(fields[2:], " ")
		}
		lines[i] = rewritten
		return []byte(strings.Join(lines, "\n")), true
	}
	return content, false
}

// renderHerdRunnerDockerfile renders the user-owned Dockerfile.herd_runner template
// with the given base image reference injected into the FROM line.
func renderHerdRunnerDockerfile(baseImage string) ([]byte, error) {
	data, err := runner.FS.ReadFile("Dockerfile.herd_runner.tmpl")
	if err != nil {
		return nil, fmt.Errorf("reading embedded Dockerfile.herd_runner.tmpl: %w", err)
	}
	tmpl, err := template.New("herd_runner").Parse(string(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, herdRunnerTemplateData{BaseImage: baseImage}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func createRunnerFiles(dir, owner, repo string, replicas int) error {
	// Runner infrastructure files are herd-managed — always overwrite to keep
	// them in sync with the installed herd version.

	// Dockerfile.herd_runner_base is no longer generated — base image is pulled
	// from ghcr.io/herd-os/herd-runner-base. Remove any leftover from older inits.
	basePath := filepath.Join(dir, "Dockerfile.herd_runner_base")
	if _, err := os.Stat(basePath); err == nil {
		if err := os.Remove(basePath); err != nil {
			return fmt.Errorf("removing obsolete Dockerfile.herd_runner_base: %w", err)
		}
		fmt.Println(display.Success("Removed obsolete Dockerfile.herd_runner_base (base image now pulled from GHCR)"))
	}

	// entrypoint.herd.sh is no longer generated — it is baked into the published base image.
	entrypointPath := filepath.Join(dir, "entrypoint.herd.sh")
	if _, err := os.Stat(entrypointPath); err == nil {
		if err := os.Remove(entrypointPath); err != nil {
			return fmt.Errorf("removing obsolete entrypoint.herd.sh: %w", err)
		}
		fmt.Println(display.Success("Removed obsolete entrypoint.herd.sh — now baked into the published base image"))
	}

	// Dockerfile.herd_runner (user-owned, only created if missing)
	herdRunnerPath := filepath.Join(dir, "Dockerfile.herd_runner")
	if _, err := os.Stat(herdRunnerPath); os.IsNotExist(err) {
		baseImage := runnerBaseImage()
		content, err := renderHerdRunnerDockerfile(baseImage)
		if err != nil {
			return err
		}
		if err := os.WriteFile(herdRunnerPath, content, 0644); err != nil {
			return fmt.Errorf("writing Dockerfile.herd_runner: %w", err)
		}
		if runnerImageTag(version) == "latest" {
			fmt.Println(display.Warning("herd dev build — pinning runner image to :latest. Re-run herd init from a released herd binary to pin a specific version."))
		}
		fmt.Println(display.Success("Created Dockerfile.herd_runner (base: " + baseImage + ")"))
	} else {
		baseImage := runnerBaseImage()
		existing, readErr := os.ReadFile(herdRunnerPath)
		if readErr != nil {
			return fmt.Errorf("reading existing Dockerfile.herd_runner: %w", readErr)
		}
		migrated, changed := migrateRunnerDockerfileFrom(existing, baseImage)
		if changed {
			// Preserve the existing file's permissions — this is a user-owned
			// file and the user may have set non-default modes on it.
			mode := os.FileMode(0644)
			if info, statErr := os.Stat(herdRunnerPath); statErr == nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(herdRunnerPath, migrated, mode); err != nil {
				return fmt.Errorf("writing migrated Dockerfile.herd_runner: %w", err)
			}
			fmt.Println(display.Success("Migrated Dockerfile.herd_runner FROM line to " + baseImage))
		} else {
			fmt.Println(display.Success("Dockerfile.herd_runner already exists (not overwritten)"))
		}
	}

	// docker-compose.herd.yml (templated with owner/repo, merged with override if present)
	composeContent, mergedOK, mergeErr := renderMergedCompose(dir, owner, repo, replicas)
	if composeContent == nil {
		return fmt.Errorf("rendering docker-compose.herd.yml: %w", mergeErr)
	}
	if mergeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to merge docker-compose.herd.override.yml: %v (using base only)\n", mergeErr)
	} else if mergedOK {
		fmt.Println(display.Success("Merged docker-compose.herd.override.yml"))
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), composeContent, 0644); err != nil {
		return fmt.Errorf("writing docker-compose.herd.yml: %w", err)
	}
	fmt.Println(display.Success("Installed docker-compose.herd.yml"))

	// .env.herd.example (static)
	envExample, err := runner.FS.ReadFile(".env.herd.example")
	if err != nil {
		return fmt.Errorf("reading embedded .env.herd.example: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.herd.example"), envExample, 0644); err != nil {
		return fmt.Errorf("writing .env.herd.example: %w", err)
	}
	fmt.Println(display.Success("Installed .env.herd.example"))

	return nil
}

// mergeComposeOverride deep-merges an override YAML into the base compose YAML.
// Maps are merged recursively; slices and scalars from the override replace the base.
func mergeComposeOverride(base, override []byte) ([]byte, error) {
	var baseMap, overrideMap map[string]any
	if err := yaml.Unmarshal(base, &baseMap); err != nil {
		return nil, fmt.Errorf("parsing base: %w", err)
	}
	if err := yaml.Unmarshal(override, &overrideMap); err != nil {
		return nil, fmt.Errorf("parsing override: %w", err)
	}
	deepMerge(baseMap, overrideMap)

	// Re-serialize with the original comment header
	merged, err := yaml.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("marshaling merged: %w", err)
	}

	// Preserve the comment header from the base
	header := extractYAMLHeader(string(base))
	return []byte(header + string(merged)), nil
}

// deepMerge recursively merges src into dst. Maps are merged; everything else is replaced.
func deepMerge(dst, src map[string]any) {
	for k, srcVal := range src {
		dstVal, exists := dst[k]
		if !exists {
			dst[k] = srcVal
			continue
		}
		dstMap, dstIsMap := dstVal.(map[string]any)
		srcMap, srcIsMap := srcVal.(map[string]any)
		if dstIsMap && srcIsMap {
			deepMerge(dstMap, srcMap)
		} else {
			dst[k] = srcVal
		}
	}
}

// extractYAMLHeader returns the leading comment lines from a YAML string.
func extractYAMLHeader(s string) string {
	var header string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			header += line + "\n"
		} else {
			break
		}
	}
	return header
}

func renderDockerCompose(owner, repo string, replicas int) (string, error) {
	// Defensively clamp to at least one replica so a zero-value config still
	// renders a valid single-replica compose. Config validation already
	// guarantees codex_replicas >= 1, but callers may pass an unvalidated value.
	if replicas < 1 {
		replicas = 1
	}
	data, err := runner.FS.ReadFile("docker-compose.herd.yml.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading embedded template: %w", err)
	}
	tmpl, err := template.New("compose").Funcs(template.FuncMap{
		"seq": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i + 1
			}
			return s
		},
	}).Parse(string(data))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, runnerTemplateData{Owner: owner, Repo: repo, Replicas: replicas}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// versionStateFile is the path (relative to repo root) where herd init records
// the version it last installed. It lives under .herd/state/ which is already
// gitignored.
const versionStateFile = ".herd/state/version"

// readPreviousInitVersion returns the version recorded by the most recent
// successful `herd init` run on this repo, or "" if no state file exists yet.
// Any read error other than not-existing is also reported as "" since the file
// is purely informational — callers should treat unreadable as "fresh install".
func readPreviousInitVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, versionStateFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeInitVersion records the currently-installed version to the state file.
// It creates the parent directory if needed. Errors are returned so the caller
// can decide whether to surface them — but this is non-fatal for the init flow.
func writeInitVersion(dir, v string) error {
	path := filepath.Join(dir, versionStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(v+"\n"), 0o644)
}

// initMessages holds the human-facing strings for the herd init commit and PR.
type initMessages struct {
	Title string
	Body  string
}

// selectInitMessages returns the commit message / PR title and PR body based
// on the relationship between the previously-installed version ("" if none)
// and the currently-running binary version.
func selectInitMessages(previous, current string) initMessages {
	switch {
	case previous == "":
		return initMessages{
			Title: fmt.Sprintf("Install HerdOS %s", current),
			Body:  fmt.Sprintf("Installs HerdOS workflows and runner infrastructure at %s.\n\nCreated by `herd init`.", current),
		}
	case previous == current:
		return initMessages{
			Title: "Sync HerdOS files",
			Body:  "Regenerates HerdOS workflows and runner infrastructure from current .herdos.yml.\n\nCreated by `herd init`.",
		}
	default:
		return initMessages{
			Title: fmt.Sprintf("Update HerdOS to %s", current),
			Body:  fmt.Sprintf("Updates HerdOS workflows and runner infrastructure from %s to %s.\n\nCreated by `herd init`.", previous, current),
		}
	}
}

func commitInitFiles(dir, owner, repo string) error {
	previousVersion := readPreviousInitVersion(dir)

	// Use version-based branch name to avoid collisions on re-runs
	branch := "herd/init-" + version

	// Delete stale local branch if it exists
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = dir
	_, _ = cmd.CombinedOutput() // ignore error if branch doesn't exist

	cmd = exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b: %s: %s", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		switchBack(dir)
		cleanupBranch(dir, branch)
	}()

	msgs := selectInitMessages(previousVersion, version)

	// Stage the files herd init creates
	filesToAdd := []string{
		config.ConfigFile,
		".gitignore",
		".herd/",
		".github/workflows/",
		"Dockerfile.herd_runner",
		"docker-compose.herd.yml",
		".env.herd.example",
	}
	args := append([]string{"add", "--"}, filesToAdd...)
	cmd = exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %s", err, strings.TrimSpace(string(out)))
	}

	// Stage removal of the obsolete Dockerfile.herd_runner_base when migrating an
	// existing repo that still tracks it. Ignore errors (file may never have been
	// tracked).
	cmd = exec.Command("git", "rm", "--cached", "--ignore-unmatch", "Dockerfile.herd_runner_base")
	cmd.Dir = dir
	_, _ = cmd.CombinedOutput()

	cmd = exec.Command("git", "rm", "--cached", "--ignore-unmatch", "entrypoint.herd.sh")
	cmd.Dir = dir
	_, _ = cmd.CombinedOutput()

	// Check if there's anything staged
	cmd = exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		fmt.Println(display.Success("All init files up to date"))
		if err := writeInitVersion(dir, version); err != nil {
			fmt.Println(display.Warning(fmt.Sprintf("Could not record installed version: %v", err)))
		}
		return nil
	}

	commitMsg := msgs.Title
	cmd = exec.Command("git", "commit", "-m", commitMsg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println(display.Success("Committed init files on branch " + branch))

	// Push (force in case a stale remote branch exists from a previous failed run)
	cmd = exec.Command("git", "push", "-u", "--force", "origin", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %s: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println(display.Success("Pushed to remote"))

	if err := writeInitVersion(dir, version); err != nil {
		fmt.Println(display.Warning(fmt.Sprintf("Could not record installed version: %v", err)))
	}

	// Open PR
	cmd = exec.Command("gh", "pr", "create",
		"--title", msgs.Title,
		"--body", msgs.Body,
		"--repo", owner+"/"+repo,
	)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(display.Warning(fmt.Sprintf("Could not create PR: %s", strings.TrimSpace(string(out)))))
		fmt.Println("  You can create it manually: gh pr create")
	} else {
		fmt.Println(display.Success("Created PR: " + strings.TrimSpace(string(out))))
	}

	return nil
}

func switchBack(dir string) {
	cmd := exec.Command("git", "checkout", "-")
	cmd.Dir = dir
	_ = cmd.Run()
}

func cleanupBranch(dir, branch string) {
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = dir
	_ = cmd.Run()
}

func printNextSteps(owner, repo string) {
	fmt.Println()
	fmt.Println("Set up runners:")
	fmt.Println("  1. cp .env.herd.example .env")
	fmt.Println("  2. Add your GITHUB_TOKEN to .env")
	fmt.Println("  3. Run: claude setup-token (uses your subscription, no API cost)")
	fmt.Println("     Add the token as CLAUDE_CODE_OAUTH_TOKEN in .env")
	fmt.Println("  4. docker compose -f docker-compose.herd.yml up -d")
	fmt.Println()
	fmt.Println("Enable workflows (after runners are ready):")
	fmt.Printf("  gh variable set HERD_ENABLED --body true --repo %s/%s\n", owner, repo)
	fmt.Println("  Workflows are installed but inactive until HERD_ENABLED is set.")
	fmt.Println()
	fmt.Printf("For full automation (cross-workflow dispatch):\n")
	fmt.Printf("  Add a fine-grained PAT as HERD_GITHUB_TOKEN in repo secrets:\n")
	fmt.Printf("  → https://github.com/%s/%s/settings/secrets/actions\n", owner, repo)
	fmt.Println("  Required PAT permissions:")
	fmt.Println("    Actions:         Read and write")
	fmt.Println("    Administration:  Read and write")
	fmt.Println("    Commit statuses: Read and write")
	fmt.Println("    Contents:        Read and write")
	fmt.Println("    Issues:          Read and write")
	fmt.Println("    Metadata:        Read-only (automatic)")
	fmt.Println("    Pull requests:   Read and write")
	fmt.Println("    Workflows:       Read and write")
	fmt.Println("  Without this, GITHUB_TOKEN's anti-recursion protection prevents")
	fmt.Println("  Monitor→Worker and Integrator→Worker dispatch.")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  herd plan          Start a planning session")
	fmt.Println("  herd plan \"...\"    Start a planning session with an initial prompt")
	fmt.Println("  herd status        Check system status")
}
