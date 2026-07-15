package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/herd-os/herd/internal/cli/runner"
	"github.com/herd-os/herd/internal/config"
	cpclient "github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	ghplatform "github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

const defaultAppLogin = "herd-os"

type initOptions struct {
	SkipLabels      bool
	SkipWorkflows   bool
	ControlPlaneURL string
	AppLogin        string
}

type repositoryRegistrar interface {
	RegisterRepository(ctx context.Context, req cpclient.RegisterRepositoryRequest) (cpclient.RegisterRepositoryResponse, error)
}

type setupAuthorizer interface {
	SetupToken(ctx context.Context) (string, error)
}

var (
	newSetupAuthorizer = func() setupAuthorizer {
		return newGHAuthorizer(nil)
	}
	newRepositoryRegistrar = func(controlPlaneURL string) (repositoryRegistrar, error) {
		return cpclient.New(controlPlaneURL, nil)
	}
)

func newInitCmd() *cobra.Command {
	var skipLabels, skipWorkflows, checkOnly, dryRun bool
	var controlPlaneURL, appLogin string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up a repo for HerdOS",
		Long:  "Initialize a repository for HerdOS. Creates config, labels, workflow files, and guides secrets setup.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkOnly || dryRun {
				return runInitCheck()
			}
			return runInitWithOptions(initOptions{
				SkipLabels:      skipLabels,
				SkipWorkflows:   skipWorkflows,
				ControlPlaneURL: controlPlaneURL,
				AppLogin:        appLogin,
			})
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVar(&skipLabels, "skip-labels", false, "Don't create labels")
	cmd.Flags().BoolVar(&skipWorkflows, "skip-workflows", false, "Don't install workflow files")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Compare what `herd init` would write against on-disk files; exit 1 if any drift is detected. Writes nothing.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Alias for --check")
	cmd.Flags().StringVar(&controlPlaneURL, "control-plane-url", "", "Herd control-plane URL for self-hosted installs (default: hosted HerdOS API)")
	cmd.Flags().StringVar(&appLogin, "app-login", "", "GitHub App login override for tests or self-hosted installs")

	return cmd
}

func runInit(skipLabels, skipWorkflows bool) error {
	return runInitWithOptions(initOptions{SkipLabels: skipLabels, SkipWorkflows: skipWorkflows})
}

func runInitWithOptions(opts initOptions) error {
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
	opts.ControlPlaneURL, err = validatedEffectiveControlPlaneURL(opts.ControlPlaneURL)
	if err != nil {
		return err
	}
	opts.AppLogin = effectiveAppLogin(opts.AppLogin)

	registration, err := registerRepositoryForInit(context.Background(), owner, repo, opts)
	if err != nil {
		return err
	}

	// 2. Create config
	configPath := filepath.Join(dir, config.ConfigFile)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		cfg := config.Default()
		cfg.Platform.Owner = owner
		cfg.Platform.Repo = repo
		if isSelfHostedControlPlane(opts.ControlPlaneURL) {
			cfg.ControlPlaneURL = opts.ControlPlaneURL
		}
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
	if isSelfHostedControlPlane(opts.ControlPlaneURL) {
		cfg.ControlPlaneURL = opts.ControlPlaneURL
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
	if !opts.SkipLabels {
		if err := createLabels(owner, repo); err != nil {
			return err
		}
	} else {
		fmt.Println(display.Warning("Skipped label creation"))
	}

	// 5. Install workflow files
	if !opts.SkipWorkflows {
		if err := installWorkflows(dir, cfg); err != nil {
			return err
		}
	} else {
		fmt.Println(display.Warning("Skipped workflow installation"))
	}

	// 6. Create runner files
	responseControlPlaneURL, err := validatedEffectiveResponseControlPlaneURL(opts.ControlPlaneURL, registration.ControlPlaneURL)
	if err != nil {
		return err
	}
	if err := createRunnerFilesWithBootstrap(dir, owner, repo, registration.RunnerBootstrapToken, responseControlPlaneURL); err != nil {
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

func effectiveControlPlaneURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return config.DefaultControlPlaneURL
	}
	return value
}

func validatedEffectiveControlPlaneURL(value string) (string, error) {
	effective := effectiveControlPlaneURL(value)
	if strings.ContainsFunc(effective, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}) {
		return "", fmt.Errorf("control-plane URL must not contain whitespace or control characters")
	}
	parsed, err := url.Parse(effective)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("control-plane URL must be an absolute http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("control-plane URL must use http or https")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("control-plane URL must not contain userinfo")
	}
	if parsed.RawQuery != "" {
		return "", fmt.Errorf("control-plane URL must not contain a query string")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("control-plane URL must not contain a fragment")
	}
	if strings.Contains(effective, `"`) {
		return "", fmt.Errorf("control-plane URL must not contain double quotes")
	}
	return strings.TrimRight(effective, "/"), nil
}

func effectiveAppLogin(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
		return defaultAppLogin
	}
	return value
}

func isSelfHostedControlPlane(value string) bool {
	return effectiveControlPlaneURL(value) != config.DefaultControlPlaneURL
}

func effectiveResponseControlPlaneURL(requested string, returned string) string {
	if strings.TrimSpace(returned) != "" {
		return effectiveControlPlaneURL(returned)
	}
	return effectiveControlPlaneURL(requested)
}

func validatedEffectiveResponseControlPlaneURL(requested string, returned string) (string, error) {
	effective, err := validatedEffectiveControlPlaneURL(effectiveResponseControlPlaneURL(requested, returned))
	if err != nil {
		return "", err
	}
	return effective, nil
}

func registerRepositoryForInit(ctx context.Context, owner, repo string, opts initOptions) (cpclient.RegisterRepositoryResponse, error) {
	authorizer := newSetupAuthorizer()
	setupToken, err := authorizer.SetupToken(ctx)
	if err != nil {
		return cpclient.RegisterRepositoryResponse{}, err
	}
	registrar, err := newRepositoryRegistrar(opts.ControlPlaneURL)
	if err != nil {
		return cpclient.RegisterRepositoryResponse{}, err
	}
	resp, err := registrar.RegisterRepository(ctx, cpclient.RegisterRepositoryRequest{
		Repository: owner + "/" + repo,
		Owner:      owner,
		Name:       repo,
		SetupToken: setupToken,
		AppLogin:   opts.AppLogin,
	})
	if err != nil {
		safeErr := redactSetupTokenError(err, setupToken)
		if registrationFailureLooksLikeAppAccess(err) {
			return cpclient.RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: %w. Ensure the Herd GitHub App is installed for %s/%s and retry `herd init`", safeErr, owner, repo)
		}
		return cpclient.RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: %w. The Herd control plane is unavailable or rate limited; retry `herd init` later", safeErr)
	}
	if strings.TrimSpace(resp.RunnerBootstrapToken) == "" {
		return cpclient.RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: response is missing runner bootstrap token; retry `herd init` later or contact the control-plane operator")
	}
	if err := validateRunnerBootstrapToken(resp.RunnerBootstrapToken); err != nil {
		return cpclient.RegisterRepositoryResponse{}, fmt.Errorf("register repository with Herd control plane: invalid runner bootstrap token in response: %w", err)
	}
	return resp, nil
}

func redactSetupTokenError(err error, setupToken string) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(err.Error())
	token := strings.TrimSpace(setupToken)
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[REDACTED]")
	}
	for _, prefix := range []string{"ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_"} {
		for {
			idx := strings.Index(msg, prefix)
			if idx < 0 {
				break
			}
			end := idx + len(prefix)
			for end < len(msg) {
				c := msg[end]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
					end++
					continue
				}
				break
			}
			msg = msg[:idx] + "[REDACTED]" + msg[end:]
		}
	}
	if msg == "" {
		msg = "registration request failed"
	}
	return errors.New(msg)
}

func registrationFailureLooksLikeAppAccess(err error) bool {
	var statusErr cpclient.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case 401, 403, 404:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not installed") || strings.Contains(message, "installation") || strings.Contains(message, "admin access")
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
func renderRunnerFiles(dir, owner, repo string) ([]managedFile, error) {
	envExample, err := runner.FS.ReadFile(".env.herd.example")
	if err != nil {
		return nil, fmt.Errorf("reading embedded .env.herd.example: %w", err)
	}

	composeContent, _, mergeErr := renderMergedCompose(dir, owner, repo)
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
	return renderMergedComposeWithControlPlane(dir, owner, repo, "")
}

func renderMergedComposeWithControlPlane(dir, owner, repo string, controlPlaneURL string) ([]byte, bool, error) {
	rendered, err := renderDockerComposeWithControlPlane(owner, repo, controlPlaneURL)
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
	Owner           string
	Repo            string
	ControlPlaneURL string
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

func createRunnerFiles(dir, owner, repo string) error {
	return createRunnerFilesWithBootstrap(dir, owner, repo, "", "")
}

func createRunnerFilesWithBootstrap(dir, owner, repo string, bootstrapToken string, controlPlaneURL string) error {
	validControlPlaneURL, err := validatedEffectiveControlPlaneURL(controlPlaneURL)
	if err != nil {
		return err
	}
	controlPlaneURL = validControlPlaneURL
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
	composeControlPlaneURL := ""
	if isSelfHostedControlPlane(controlPlaneURL) {
		composeControlPlaneURL = effectiveControlPlaneURL(controlPlaneURL)
	}
	composeContent, mergedOK, mergeErr := renderMergedComposeWithControlPlane(dir, owner, repo, composeControlPlaneURL)
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

	if strings.TrimSpace(bootstrapToken) != "" {
		if err := writeRunnerEnv(dir, bootstrapToken, composeControlPlaneURL); err != nil {
			return err
		}
		fmt.Println(display.Success("Wrote runner bootstrap token to .env"))
	}

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
	return renderDockerComposeWithControlPlane(owner, repo, "")
}

func renderDockerComposeWithControlPlane(owner, repo string, controlPlaneURL string) (string, error) {
	data, err := runner.FS.ReadFile("docker-compose.herd.yml.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading embedded template: %w", err)
	}
	tmpl, err := template.New("compose").Parse(string(data))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, runnerTemplateData{Owner: owner, Repo: repo, ControlPlaneURL: controlPlaneURL}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeRunnerEnv(dir string, bootstrapToken string, controlPlaneURL string) error {
	if strings.TrimSpace(bootstrapToken) != "" {
		if err := validateRunnerBootstrapToken(bootstrapToken); err != nil {
			return err
		}
		if err := ensureGitignore(dir, ".env"); err != nil {
			return fmt.Errorf("updating .gitignore for .env: %w", err)
		}
	}
	path := filepath.Join(dir, ".env")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading .env: %w", err)
	}

	values := parseEnvLines(string(existing))
	values["HERD_RUNNER_BOOTSTRAP_TOKEN"] = bootstrapToken
	if strings.TrimSpace(controlPlaneURL) != "" {
		values["HERD_CONTROL_PLANE_URL"] = effectiveControlPlaneURL(controlPlaneURL)
	} else {
		delete(values, "HERD_CONTROL_PLANE_URL")
	}

	var out strings.Builder
	wroteBootstrap := false
	wroteControlPlane := false
	for _, line := range strings.Split(string(existing), "\n") {
		if line == "" {
			continue
		}
		key, ok := envLineKey(line)
		switch {
		case ok && key == "HERD_RUNNER_BOOTSTRAP_TOKEN":
			out.WriteString("HERD_RUNNER_BOOTSTRAP_TOKEN=" + values[key] + "\n")
			wroteBootstrap = true
		case ok && key == "HERD_CONTROL_PLANE_URL":
			if v, keep := values[key]; keep {
				out.WriteString("HERD_CONTROL_PLANE_URL=" + v + "\n")
				wroteControlPlane = true
			}
		default:
			out.WriteString(line + "\n")
		}
	}
	if !wroteControlPlane {
		if v, ok := values["HERD_CONTROL_PLANE_URL"]; ok {
			out.WriteString("HERD_CONTROL_PLANE_URL=" + v + "\n")
		}
	}
	if !wroteBootstrap {
		out.WriteString("HERD_RUNNER_BOOTSTRAP_TOKEN=" + values["HERD_RUNNER_BOOTSTRAP_TOKEN"] + "\n")
	}
	return os.WriteFile(path, []byte(out.String()), 0600)
}

func validateRunnerBootstrapToken(token string) error {
	if token == "" {
		return fmt.Errorf("runner bootstrap token is required")
	}
	if strings.TrimSpace(token) != token {
		return fmt.Errorf("runner bootstrap token must not contain leading or trailing whitespace")
	}
	if !strings.HasPrefix(token, "hrb_") {
		return fmt.Errorf("runner bootstrap token has unexpected format")
	}
	if strings.ContainsFunc(token, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}) {
		return fmt.Errorf("runner bootstrap token must be a single-line env-safe value")
	}
	if strings.Contains(token, "=") {
		return fmt.Errorf("runner bootstrap token must not contain env syntax")
	}
	return nil
}

func parseEnvLines(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		key, ok := envLineKey(line)
		if !ok {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[key] = parts[1]
		}
	}
	return values
}

func envLineKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "=") {
		return "", false
	}
	key := strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[0])
	if key == "" {
		return "", false
	}
	return key, true
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
	fmt.Println("  1. Review the .env created by herd init; add model/provider credentials there")
	fmt.Println("     If .env is missing, copy .env.herd.example to .env first")
	fmt.Println("  2. Confirm HERD_RUNNER_BOOTSTRAP_TOKEN is present in .env; do not overwrite it")
	fmt.Println("  3. Run: claude setup-token (uses your subscription, no API cost)")
	fmt.Println("     Add the token as CLAUDE_CODE_OAUTH_TOKEN in .env")
	fmt.Println("  4. docker compose -f docker-compose.herd.yml up -d")
	fmt.Println()
	fmt.Println("Enable workflows (after runners are ready):")
	fmt.Printf("  gh variable set HERD_ENABLED --body true --repo %s/%s\n", owner, repo)
	fmt.Println("  Workflows are installed but inactive until HERD_ENABLED is set.")
	fmt.Println()
	fmt.Printf("Production orchestration:\n")
	fmt.Println("  Herd dispatches jobs through the control plane and GitHub App installation auth.")
	fmt.Println("  Do not add HERD_GITHUB_TOKEN or human PAT secrets for production workflows.")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  herd plan          Start a planning session")
	fmt.Println("  herd plan \"...\"    Start a planning session with an initial prompt")
	fmt.Println("  herd status        Check system status")
}
