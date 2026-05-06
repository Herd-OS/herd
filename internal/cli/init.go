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
	if err := createRunnerFiles(dir, owner, repo); err != nil {
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
	Mode    os.FileMode // 0644 for most, 0755 for entrypoint.herd.sh
}

// renderRunnerFiles returns the runner-infrastructure files (Dockerfile.herd_runner_base,
// entrypoint.herd.sh, .env.herd.example, docker-compose.herd.yml) in stable order.
// The compose result reflects docker-compose.herd.override.yml if present in dir.
// Dockerfile.herd_runner is intentionally NOT included — it is user-owned.
func renderRunnerFiles(dir, owner, repo string) ([]managedFile, error) {
	dockerfileBase, err := runner.FS.ReadFile("Dockerfile.herd_runner_base")
	if err != nil {
		return nil, fmt.Errorf("reading embedded Dockerfile.herd_runner_base: %w", err)
	}
	entrypoint, err := runner.FS.ReadFile("entrypoint.herd.sh")
	if err != nil {
		return nil, fmt.Errorf("reading embedded entrypoint.herd.sh: %w", err)
	}
	envExample, err := runner.FS.ReadFile(".env.herd.example")
	if err != nil {
		return nil, fmt.Errorf("reading embedded .env.herd.example: %w", err)
	}

	composeContent, _, _ := renderMergedCompose(dir, owner, repo)
	if composeContent == nil {
		return nil, fmt.Errorf("rendering docker-compose.herd.yml")
	}

	return []managedFile{
		{Path: "Dockerfile.herd_runner_base", Content: dockerfileBase, Mode: 0644},
		{Path: "entrypoint.herd.sh", Content: entrypoint, Mode: 0755},
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
	runners, err := renderRunnerFiles(dir, owner, repo)
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
func renderMergedCompose(dir, owner, repo string) ([]byte, bool, error) {
	rendered, err := renderDockerCompose(owner, repo)
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
	Owner string
	Repo  string
}

func createRunnerFiles(dir, owner, repo string) error {
	// Runner infrastructure files are herd-managed — always overwrite to keep
	// them in sync with the installed herd version.

	// Dockerfile.herd_runner_base (herd-managed, always overwritten)
	dockerfileBase, err := runner.FS.ReadFile("Dockerfile.herd_runner_base")
	if err != nil {
		return fmt.Errorf("reading embedded Dockerfile.herd_runner_base: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner_base"), dockerfileBase, 0644); err != nil {
		return fmt.Errorf("writing Dockerfile.herd_runner_base: %w", err)
	}
	fmt.Println(display.Success("Installed Dockerfile.herd_runner_base"))

	// Dockerfile.herd_runner (user-owned, only created if missing)
	herdRunnerPath := filepath.Join(dir, "Dockerfile.herd_runner")
	if _, err := os.Stat(herdRunnerPath); os.IsNotExist(err) {
		data, err := runner.FS.ReadFile("Dockerfile.herd_runner.tmpl")
		if err != nil {
			return fmt.Errorf("reading embedded Dockerfile.herd_runner.tmpl: %w", err)
		}
		if err := os.WriteFile(herdRunnerPath, data, 0644); err != nil {
			return fmt.Errorf("writing Dockerfile.herd_runner: %w", err)
		}
		fmt.Println(display.Success("Created Dockerfile.herd_runner"))
	} else {
		fmt.Println(display.Success("Dockerfile.herd_runner already exists (not overwritten)"))
	}

	// entrypoint.herd.sh (static, executable)
	entrypoint, err := runner.FS.ReadFile("entrypoint.herd.sh")
	if err != nil {
		return fmt.Errorf("reading embedded entrypoint.herd.sh: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entrypoint.herd.sh"), entrypoint, 0755); err != nil {
		return fmt.Errorf("writing entrypoint.herd.sh: %w", err)
	}
	fmt.Println(display.Success("Installed entrypoint.herd.sh"))

	// docker-compose.herd.yml (templated with owner/repo, merged with override if present)
	composeContent, mergedOK, mergeErr := renderMergedCompose(dir, owner, repo)
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

func renderDockerCompose(owner, repo string) (string, error) {
	data, err := runner.FS.ReadFile("docker-compose.herd.yml.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading embedded template: %w", err)
	}
	tmpl, err := template.New("compose").Parse(string(data))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, runnerTemplateData{Owner: owner, Repo: repo}); err != nil {
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
		"Dockerfile.herd_runner_base",
		"Dockerfile.herd_runner",
		"entrypoint.herd.sh",
		"docker-compose.herd.yml",
		".env.herd.example",
	}
	args := append([]string{"add", "--"}, filesToAdd...)
	cmd = exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %s", err, strings.TrimSpace(string(out)))
	}

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
