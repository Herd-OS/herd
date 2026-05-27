package prompt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTempPlan(t *testing.T, plan agent.Plan) string {
	t.Helper()
	data, err := json.Marshal(plan)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestReadPlanFile_Valid(t *testing.T) {
	plan := agent.Plan{
		BatchName: "Add authentication",
		Tasks: []agent.PlannedTask{
			{
				Title:                   "Create User model",
				Description:             "Create user model with password hashing",
				ImplementationDetails:   "Use bcrypt with 12 salt rounds",
				AcceptanceCriteria:      []string{"Model exists", "Tests pass"},
				Scope:                   []string{"src/models/user.ts"},
				Conventions:             []string{"Follow existing model pattern"},
				ContextFromDependencies: []string{"bcrypt is available as import"},
				Complexity:              "medium",
				Type:                    "feature",
			},
			{
				Title:              "Add login route",
				AcceptanceCriteria: []string{"Returns 200 on valid creds"},
				Complexity:         "low",
				Type:               "feature",
				DependsOn:          []int{0},
			},
		},
	}

	path := writeTempPlan(t, plan)
	got, err := ReadPlanFile(path)
	require.NoError(t, err)
	assert.Equal(t, "Add authentication", got.BatchName)
	assert.Len(t, got.Tasks, 2)
	assert.Equal(t, "Create User model", got.Tasks[0].Title)
	assert.Equal(t, "Use bcrypt with 12 salt rounds", got.Tasks[0].ImplementationDetails)
	assert.Equal(t, []string{"Follow existing model pattern"}, got.Tasks[0].Conventions)
	assert.Equal(t, []string{"bcrypt is available as import"}, got.Tasks[0].ContextFromDependencies)
	assert.Equal(t, []int{0}, got.Tasks[1].DependsOn)
}

func TestReadPlanFile_NotExist(t *testing.T) {
	_, err := ReadPlanFile("/nonexistent/path/plan.json")
	assert.ErrorContains(t, err, "agent did not produce a plan file")
}

func TestReadPlanFile_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(path, []byte("not json{"), 0o644))

	_, err := ReadPlanFile(path)
	assert.ErrorContains(t, err, "parsing plan JSON")
}

func TestReadPlanFile_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	_, err := ReadPlanFile(path)
	assert.ErrorContains(t, err, "parsing plan JSON")
}

func TestValidatePlan(t *testing.T) {
	tests := []struct {
		name       string
		plan       *agent.Plan
		wantErr    bool
		errSubstr  string
	}{
		{
			name:    "accepts valid plan",
			plan:    &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a", AcceptanceCriteria: []string{"y"}, Complexity: "medium", Type: "feature"},
				{Title: "b", AcceptanceCriteria: []string{"y"}, Complexity: "low", Type: "bugfix", DependsOn: []int{0}},
			}},
			wantErr: false,
		},
		{
			name:      "empty batch_name",
			plan:      &agent.Plan{BatchName: "", Tasks: []agent.PlannedTask{{Title: "x", AcceptanceCriteria: []string{"y"}}}},
			wantErr:   true,
			errSubstr: "batch_name",
		},
		{
			name:      "no tasks",
			plan:      &agent.Plan{BatchName: "b", Tasks: nil},
			wantErr:   true,
			errSubstr: "tasks",
		},
		{
			name: "task missing title",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "", AcceptanceCriteria: []string{"y"}},
			}},
			wantErr:   true,
			errSubstr: "title",
		},
		{
			name: "task missing acceptance criteria",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a"},
			}},
			wantErr:   true,
			errSubstr: "acceptance_criteria",
		},
		{
			name: "depends_on out of range",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a", AcceptanceCriteria: []string{"y"}},
				{Title: "b", AcceptanceCriteria: []string{"y"}, DependsOn: []int{5}},
			}},
			wantErr:   true,
			errSubstr: "out of range",
		},
		{
			name: "self-referential depends_on",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a", AcceptanceCriteria: []string{"y"}, DependsOn: []int{0}},
			}},
			wantErr:   true,
			errSubstr: "references itself",
		},
		{
			name: "invalid complexity",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a", AcceptanceCriteria: []string{"y"}, Complexity: "huge"},
			}},
			wantErr:   true,
			errSubstr: "complexity",
		},
		{
			name: "invalid type",
			plan: &agent.Plan{BatchName: "b", Tasks: []agent.PlannedTask{
				{Title: "a", AcceptanceCriteria: []string{"y"}, Type: "wibble"},
			}},
			wantErr:   true,
			errSubstr: "type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlan(tt.plan)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRenderPlanningPrompt_Basic(t *testing.T) {
	opts := agent.PlanOptions{
		RepoRoot:   "/home/user/project",
		OutputPath: "/home/user/project/.herd/plans/abc.json",
		Context:    map[string]string{},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)
	assert.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "Working directory: /home/user/project")
	assert.Contains(t, prompt, ".herd/plans/abc.json")
	assert.Contains(t, prompt, "Cannot produce merge conflicts with parallel tasks")
	assert.Contains(t, prompt, "You do the thinking, the Worker does the typing")
	assert.Contains(t, prompt, "Exact file paths")
	assert.Contains(t, prompt, "context_from_dependencies")
	assert.NotContains(t, prompt, "Project-Specific Instructions")
	assert.NotContains(t, prompt, "Repository Structure")
	assert.NotContains(t, prompt, "Project Overview")
	assert.NotContains(t, prompt, "Recent Changes")
	assert.NotContains(t, prompt, "Active Batches")
}

func TestRenderPlanningPrompt_ThreeStepOutput(t *testing.T) {
	opts := agent.PlanOptions{
		RepoRoot:   "/home/user/project",
		OutputPath: "/home/user/project/.herd/plans/abc.json",
		Context:    map[string]string{},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)

	tests := []struct {
		name     string
		contains string
	}{
		{"step1 heading", "### Step 1: Present a high-level overview"},
		{"step1 table header", "| # | Title | Tier | Complexity | Depends On | Manual |"},
		{"step1 prompt details option", "Say **details** to see the full implementation plan"},
		{"step1 prompt approve option", "**approve** to write the plan file"},
		{"step2 heading", "### Step 2: Show full details (on request)"},
		{"step2 trigger", `"details"`},
		{"step2 task format", "**Task N: <title>** as a heading"},
		{"step2 approval prompt", "say **approve** and I will write the plan file"},
		{"step3 heading", "### Step 3: Write the plan file"},
		{"step3 either step", "**either** Step 1 or Step 2"},
		{"step3 output path", ".herd/plans/abc.json"},
		{"step3 no write until approve", "Do NOT write the JSON file until the user explicitly approves"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, prompt, tc.contains)
		})
	}
}

func TestRenderPlanningPrompt_WithRoleInstructions(t *testing.T) {
	opts := agent.PlanOptions{
		RepoRoot:   "/home/user/project",
		OutputPath: "/tmp/plan.json",
		Context: map[string]string{
			"role_instructions": "Always use table-driven tests.\nPrefer short functions.",
		},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Project-Specific Instructions")
	assert.Contains(t, prompt, "Always use table-driven tests.")
	assert.Contains(t, prompt, "Prefer short functions.")
}

func TestPlannedTaskJSONRoundTrip(t *testing.T) {
	task := agent.PlannedTask{
		Title:                   "Add auth middleware",
		Description:             "JWT validation middleware",
		ImplementationDetails:   "Use jsonwebtoken to verify Bearer tokens",
		AcceptanceCriteria:      []string{"401 on missing token", "403 on invalid token"},
		Scope:                   []string{"src/middleware/auth.ts"},
		Conventions:             []string{"Follow Express middleware pattern"},
		ContextFromDependencies: []string{"User model has verifyPassword method"},
		Complexity:              "medium",
		Type:                    "feature",
		RunnerLabel:             "herd-heavy",
		DependsOn:               []int{0, 1},
	}

	data, err := json.Marshal(task)
	require.NoError(t, err)

	var got agent.PlannedTask
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, task, got)
}

func TestPlanJSONRoundTrip(t *testing.T) {
	plan := agent.Plan{
		BatchName: "Feature X",
		Tasks: []agent.PlannedTask{
			{Title: "Task 1", Description: "First", Complexity: "low", DependsOn: []int{}},
			{Title: "Task 2", Description: "Second", Complexity: "medium", DependsOn: []int{0}},
		},
	}

	data, err := json.Marshal(plan)
	require.NoError(t, err)

	var got agent.Plan
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, plan, got)
}

func TestRenderPlanningPrompt_WithRepoContext(t *testing.T) {
	tmp := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmp, "README.md"), []byte("# My Project\nA test project"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/foo\n\ngo 1.22"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cmd", "app"), 0o755))

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "add", "."},
		{"git", "commit", "-m", "initial commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, out)
	}

	opts := agent.PlanOptions{
		RepoRoot:   tmp,
		OutputPath: filepath.Join(tmp, "plan.json"),
		Context:    map[string]string{},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)

	assert.Contains(t, prompt, "## Repository Structure")
	assert.Contains(t, prompt, "cmd/")
	assert.Contains(t, prompt, "## Project Overview")
	assert.Contains(t, prompt, "# My Project")
	assert.Contains(t, prompt, "## Tech Stack (go.mod)")
	assert.Contains(t, prompt, "module example.com/foo")
	assert.Contains(t, prompt, "## Recent Changes")
}

func TestRenderPlanningPrompt_WithMilestones(t *testing.T) {
	opts := agent.PlanOptions{
		RepoRoot:   "/home/user/project",
		OutputPath: "/tmp/plan.json",
		Context: map[string]string{
			"milestones": "- Batch #5: Add auth (3 open, 1 closed)",
		},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)

	assert.Contains(t, prompt, "## Active Batches")
	assert.Contains(t, prompt, "- Batch #5: Add auth (3 open, 1 closed)")
}

func TestRenderPlanningPrompt_SkipsHerdOSConfig(t *testing.T) {
	tmp := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmp, ".herdos.yml"), []byte("version: 1"), 0o644))

	opts := agent.PlanOptions{
		RepoRoot:   tmp,
		OutputPath: filepath.Join(tmp, "plan.json"),
		Context:    map[string]string{},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)

	assert.NotContains(t, prompt, "Tech Stack (.herdos.yml)")
	assert.NotContains(t, prompt, "version: 1")
}

func TestRenderPlanningPrompt_ContainsRepoRootAndOutputPath(t *testing.T) {
	opts := agent.PlanOptions{
		RepoRoot:   "/repo/root",
		OutputPath: "/out/path/plan.json",
		Context:    map[string]string{},
	}

	prompt, err := RenderPlanningPrompt(opts)
	require.NoError(t, err)
	assert.NotEmpty(t, prompt)
	assert.True(t, strings.Contains(prompt, "/repo/root"))
	assert.True(t, strings.Contains(prompt, "/out/path/plan.json"))
}
