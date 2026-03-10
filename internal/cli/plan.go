package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/dag"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/planner"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newPlanCmd() *cobra.Command {
	var noDispatch, dryRun bool
	var batchName string

	cmd := &cobra.Command{
		Use:   "plan [description]",
		Short: "Plan and decompose work into issues",
		Long:  "Launch an interactive planning session with an AI agent to decompose work into discrete, executable tasks.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var initialPrompt string
			if len(args) > 0 {
				initialPrompt = args[0]
			}
			return runPlan(cmd.Context(), initialPrompt, batchName, noDispatch, dryRun)
		},
	}

	cmd.Flags().BoolVar(&noDispatch, "no-dispatch", false, "Don't dispatch Tier 0 after creating issues")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be created without creating anything")
	cmd.Flags().StringVar(&batchName, "batch", "", "Override batch name")

	return cmd
}

func runPlan(ctx context.Context, initialPrompt, batchNameOverride string, noDispatch, dryRun bool) error {
	cfg, err := loadConfigOrExit()
	if err != nil {
		return err
	}

	// Generate plan ID and output path
	planID := fmt.Sprintf("%d", time.Now().UnixNano())
	stateDir := filepath.Join(".", ".herd", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	outputPath := filepath.Join(stateDir, planID+".json")
	defer os.Remove(outputPath)

	// Read role instructions
	roleInstructions := ""
	if data, err := os.ReadFile(".herd/planner.md"); err == nil {
		roleInstructions = string(data)
	}

	// Get working directory
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	opts := agent.PlanOptions{
		RepoRoot:   dir,
		OutputPath: outputPath,
		Context:    map[string]string{},
	}
	if roleInstructions != "" {
		opts.Context["role_instructions"] = roleInstructions
	}

	// Create agent and launch planning session
	agentInstance := claude.New(cfg.Agent.Binary, cfg.Agent.Model)
	plan, err := agentInstance.Plan(ctx, initialPrompt, opts)
	if err != nil {
		return err
	}

	// Apply batch name override
	if batchNameOverride != "" {
		plan.BatchName = batchNameOverride
	}

	// Compute tiers for display
	tiers, err := computeTiers(plan)
	if err != nil {
		return err
	}

	// Present plan and get confirmation
	plan, err = confirmPlan(plan, tiers)
	if err != nil {
		return err
	}

	if dryRun {
		printDryRun(plan, tiers)
		return nil
	}

	// Create platform client
	client, err := newClientOrExit(cfg.Platform.Owner, cfg.Platform.Repo)
	if err != nil {
		return err
	}

	// Create issues, milestone, and batch branch
	result, err := planner.CreateFromPlan(ctx, client, plan)
	if err != nil {
		return err
	}

	// Print summary
	fmt.Println(display.Success(fmt.Sprintf("Created milestone #%d: %s", result.MilestoneNumber, plan.BatchName)))
	fmt.Println(display.Success(fmt.Sprintf("Created %d issues (#%d-#%d)",
		len(result.IssueNumbers), result.IssueNumbers[0], result.IssueNumbers[len(result.IssueNumbers)-1])))
	fmt.Println(display.Success(fmt.Sprintf("Created batch branch: %s", result.BatchBranch)))

	// Dispatch Tier 0
	if !noDispatch && len(result.Tiers) > 0 {
		fmt.Printf("\nDispatching Tier 0 (%d issues):\n", len(result.Tiers[0]))
		for _, issueNum := range result.Tiers[0] {
			err := dispatchIssue(ctx, client, cfg, issueNum, result.BatchBranch)
			if err != nil {
				fmt.Printf("  #%d %s\n", issueNum, display.Error(fmt.Sprintf("failed: %v", err)))
			} else {
				fmt.Printf("  #%d %s\n", issueNum, display.Success("triggered"))
			}
		}
		blocked := 0
		for i := 1; i < len(result.Tiers); i++ {
			blocked += len(result.Tiers[i])
		}
		if blocked > 0 {
			fmt.Printf("  (%d issues blocked, will dispatch when dependencies complete)\n", blocked)
		}
	}

	return nil
}

func computeTiers(plan *agent.Plan) ([][]int, error) {
	d := dag.New()
	for i := range plan.Tasks {
		d.AddNode(i)
	}
	for i, task := range plan.Tasks {
		for _, dep := range task.DependsOn {
			d.AddEdge(i, dep)
		}
	}
	return d.Tiers()
}

func confirmPlan(plan *agent.Plan, tiers [][]int) (*agent.Plan, error) {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Printf("\nPlan: %q (%d tasks)\n", plan.BatchName, len(plan.Tasks))
		for t, tier := range tiers {
			for _, idx := range tier {
				task := plan.Tasks[idx]
				depStr := ""
				if len(task.DependsOn) > 0 {
					deps := make([]string, len(task.DependsOn))
					for j, d := range task.DependsOn {
						deps[j] = fmt.Sprintf("#%d", d)
					}
					depStr = fmt.Sprintf(" (depends on %s)", strings.Join(deps, ", "))
				}
				fmt.Printf("  %d. %-40s %-8s Tier %d%s\n", idx+1, task.Title, task.Complexity, t, depStr)
			}
		}

		fmt.Print("\nCreate batch with issues? [y/n/edit] ")
		if !scanner.Scan() {
			return nil, fmt.Errorf("cancelled")
		}

		switch strings.TrimSpace(strings.ToLower(scanner.Text())) {
		case "y", "yes":
			return plan, nil
		case "n", "no":
			return nil, fmt.Errorf("plan rejected by user")
		case "edit":
			edited, err := editPlan(plan)
			if err != nil {
				fmt.Printf("Edit error: %v\n", err)
				continue
			}
			plan = edited
			tiers, err = computeTiers(plan)
			if err != nil {
				fmt.Printf("DAG error: %v\n", err)
				continue
			}
		default:
			fmt.Println("Please enter y, n, or edit")
		}
	}
}

func editPlan(plan *agent.Plan) (*agent.Plan, error) {
	data, err := yaml.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshaling plan: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "herd-plan-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("writing plan: %w", err)
	}
	tmpFile.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, tmpFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("editor exited with error: %w", err)
	}

	edited, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("reading edited plan: %w", err)
	}

	var newPlan agent.Plan
	if err := yaml.Unmarshal(edited, &newPlan); err != nil {
		return nil, fmt.Errorf("parsing edited plan: %w", err)
	}

	if newPlan.BatchName == "" {
		return nil, fmt.Errorf("batch_name cannot be empty")
	}
	if len(newPlan.Tasks) == 0 {
		return nil, fmt.Errorf("plan must have at least one task")
	}

	return &newPlan, nil
}

func printDryRun(plan *agent.Plan, tiers [][]int) {
	fmt.Println("\n--- DRY RUN ---")
	fmt.Printf("Would create milestone: %s\n", plan.BatchName)
	fmt.Printf("Would create batch branch: herd/batch/<number>-%s\n", planner.Slugify(plan.BatchName))
	fmt.Printf("Would create %d issues:\n", len(plan.Tasks))
	for t, tier := range tiers {
		for _, idx := range tier {
			task := plan.Tasks[idx]
			status := "ready"
			if t > 0 {
				status = "blocked"
			}
			fmt.Printf("  [Tier %d] %s (%s, %s)\n", t, task.Title, task.Complexity, status)
		}
	}
	fmt.Printf("Would dispatch Tier 0 (%d issues)\n", len(tiers[0]))
}
