# Roadmap

## v1.0 -- Full Release

A complete, self-healing orchestration system for a single repository.

### Success Criteria

A user can:
1. Run `herd init` in a repo and start Docker runners
2. Run `herd plan "Add feature X"`, get issues created, and have Tier 0 workers dispatch automatically
3. Watch workers execute tasks tier by tier (Integrator advances tiers automatically)
4. See the batch PR with agent review, review the complete feature, and merge it
5. If a worker fails, the Monitor re-dispatches it automatically

### Limitations

- Single-repo only (no cross-repo batches)
- GitHub only (Platform interface exists but only GitHub is implemented)
- Claude Code only (Agent interface exists but only Claude Code is implemented)

## v2 -- GitHub App and Multi-Agent

### GitHub App

- **@herd-os mentions** -- users mention @herd-os in comments with natural language requests (e.g., "@herd-os the CI is broken, I think the Node version file is missing"). An agent interprets the request and calls the same command functions from `internal/commands/` as tool calls.
- **Webhook-based handling** -- replace the `issue_comment` workflow trigger with a webhook endpoint on the GitHub App. Eliminates runner spin-up latency for command handling.
- **Dedicated bot identity** -- all HerdOS comments, reactions, and reviews come from the `herd-os[bot]` account instead of the GITHUB_TOKEN identity. This also fixes the PR review blocking issue: currently the PAT that creates batch PRs cannot submit "Changes Requested" reviews on them (GitHub prevents users from reviewing their own PRs). The GitHub App has a separate identity and can both create and review PRs, enabling the merge button to be blocked during active fix cycles.
- **Issue-driven planning** -- users create an issue describing a feature, mention @herd-os, and the agent decomposes and dispatches without the CLI. Multi-turn conversation in the issue thread refines the plan.

### Multi-Agent

Additional agent implementations (Codex, Cursor, Gemini CLI, OpenCode). Users can choose their preferred agent.

## v3 -- Multi-Platform

GitLab and Gitea/Forgejo implementations of the Platform interface. Platform auto-detection from Git remote URL. Same `herd` CLI works against GitHub, GitLab, and Gitea repositories.

## v4 -- Multi-Repo and Formulas

- Cross-repo batches (track work across multiple repos)
- Formula system (reusable work decomposition templates)
- Worker templates (customizable worker behavior per task type)
- Performance metrics (worker success rate, average completion time)

## Future

- **Daily Codebase Review** -- a scheduled workflow that runs the agent reviewer against the full codebase (or recent changes on main) on a daily cadence. Catches cross-cutting bugs, accumulated tech debt, security issues, and stale patterns that slip through individual PR reviews. Creates fix issues automatically, giving the team a daily health report.
- **Federation** -- coordinating HerdOS instances across organizations
- **Marketplace** -- shareable formulas and worker templates
- **Model A/B Testing** -- run different AI models on similar tasks and compare outcomes
