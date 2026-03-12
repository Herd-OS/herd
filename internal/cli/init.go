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

	"github.com/herd-os/herd/internal/cli/runner"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	ghplatform "github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var skipLabels, skipWorkflows bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up a repo for HerdOS",
		Long:  "Initialize a repository for HerdOS. Creates config, labels, workflow files, and guides secrets setup.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(skipLabels, skipWorkflows)
		},
	}

	cmd.Flags().BoolVar(&skipLabels, "skip-labels", false, "Don't create labels")
	cmd.Flags().BoolVar(&skipWorkflows, "skip-workflows", false, "Don't install workflow files")

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
		if err := installWorkflows(dir); err != nil {
			return err
		}
	} else {
		fmt.Println(display.Warning("Skipped workflow installation"))
	}

	// 6. Create runner files
	if err := createRunnerFiles(dir, owner, repo); err != nil {
		return err
	}

	// 7. Print next steps
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
	defer f.Close()

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

func installWorkflows(dir string) error {
	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("creating .github/workflows/: %w", err)
	}

	for _, name := range WorkflowFiles() {
		data, err := workflowFS.ReadFile("workflows/" + name)
		if err != nil {
			return fmt.Errorf("reading embedded workflow %s: %w", name, err)
		}

		dest := filepath.Join(workflowDir, name)
		if err := os.WriteFile(dest, data, 0644); err != nil {
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
	// Dockerfile.runner (static)
	dockerfilePath := filepath.Join(dir, "Dockerfile.runner")
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		data, err := runner.FS.ReadFile("Dockerfile.runner")
		if err != nil {
			return fmt.Errorf("reading embedded Dockerfile.runner: %w", err)
		}
		if err := os.WriteFile(dockerfilePath, data, 0644); err != nil {
			return fmt.Errorf("creating Dockerfile.runner: %w", err)
		}
		fmt.Println(display.Success("Created Dockerfile.runner"))
	} else {
		fmt.Println(display.Success("Dockerfile.runner already exists"))
	}

	// entrypoint.sh (static, executable)
	entrypointPath := filepath.Join(dir, "entrypoint.sh")
	if _, err := os.Stat(entrypointPath); os.IsNotExist(err) {
		data, err := runner.FS.ReadFile("entrypoint.sh")
		if err != nil {
			return fmt.Errorf("reading embedded entrypoint.sh: %w", err)
		}
		if err := os.WriteFile(entrypointPath, data, 0755); err != nil {
			return fmt.Errorf("creating entrypoint.sh: %w", err)
		}
		fmt.Println(display.Success("Created entrypoint.sh"))
	} else {
		fmt.Println(display.Success("entrypoint.sh already exists"))
	}

	// docker-compose.herd.yml (templated with owner/repo)
	composePath := filepath.Join(dir, "docker-compose.herd.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		rendered, err := renderDockerCompose(owner, repo)
		if err != nil {
			return fmt.Errorf("rendering docker-compose.herd.yml: %w", err)
		}
		if err := os.WriteFile(composePath, []byte(rendered), 0644); err != nil {
			return fmt.Errorf("creating docker-compose.herd.yml: %w", err)
		}
		fmt.Println(display.Success("Created docker-compose.herd.yml"))
	} else {
		fmt.Println(display.Success("docker-compose.herd.yml already exists"))
	}

	// .env.example (static)
	envExamplePath := filepath.Join(dir, ".env.example")
	if _, err := os.Stat(envExamplePath); os.IsNotExist(err) {
		data, err := runner.FS.ReadFile(".env.example")
		if err != nil {
			return fmt.Errorf("reading embedded .env.example: %w", err)
		}
		if err := os.WriteFile(envExamplePath, data, 0644); err != nil {
			return fmt.Errorf("creating .env.example: %w", err)
		}
		fmt.Println(display.Success("Created .env.example"))
	} else {
		fmt.Println(display.Success(".env.example already exists"))
	}

	return nil
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

func printNextSteps(owner, repo string) {
	fmt.Println()
	fmt.Println("Set up runners:")
	fmt.Println("  1. cp .env.example .env")
	fmt.Println("  2. Add your GITHUB_TOKEN to .env")
	fmt.Println("  3. Run: claude setup-token (uses your subscription, no API cost)")
	fmt.Println("     Add the token as CLAUDE_CODE_OAUTH_TOKEN in .env")
	fmt.Println("  4. docker compose -f docker-compose.herd.yml up -d")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  herd plan          Start a planning session")
	fmt.Println("  herd plan \"...\"    Start a planning session with an initial prompt")
	fmt.Println("  herd status        Check system status")
}
