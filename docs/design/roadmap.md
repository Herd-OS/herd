# Roadmap

## v1.0 -- Full Release Target

A planned complete, self-healing, GitHub-native orchestration system for a single repository.

Before tagging v1.0, HerdOS is focusing on a release-readiness pass across the areas that make the system feel native, reliable, and supportable:

- GitHub App identity and permissions.
- Line-level review findings.
- Pi provider support.
- Provider parity for Claude Code, OpenCode, Codex, and Pi.
- A broad `herd doctor` command.
- Clear recovery UX for non-self-healing failures.
- Batch pause, resume, and cancel controls.
- Cost, token, and time visibility.
- Concurrency model audit.
- Upgrade path hardening.
- Security posture documentation.

### Target Success Criteria

A user can:
1. Run `herd init` in a repo and start Docker runners
2. Install the HerdOS GitHub App so Herd acts as its own bot identity rather than the user's GitHub account
3. Mention Herd in GitHub comments (for example, `@herd-os review this PR`) and have the App route the request to the same command handlers as CLI/slash-command flows
4. Run `herd plan "Add feature X"`, get issues created, and have Tier 0 workers dispatch automatically
5. Watch workers execute tasks tier by tier (Integrator advances tiers automatically)
6. See the batch PR with agent review under the HerdOS App identity, including line-level review comments when findings map to specific code
7. Review the complete feature and merge it without Herd's work being attributed to the human user's account
8. If a worker fails, the Monitor re-dispatches it automatically

### Limitations

- Single-repo only (no cross-repo batches)
- GitHub only (Platform interface exists but only GitHub is implemented)
- Claude Code, OpenCode, Codex, and Pi supported by the v1 target

### GitHub App

- **@herd-os mentions** -- users mention @herd-os in comments with natural language requests (e.g., "@herd-os the CI is broken, I think the Node version file is missing"). An agent interprets the request and calls the same command functions from `internal/commands/` as tool calls.
- **Webhook-based handling** -- replace the `issue_comment` workflow trigger with a webhook endpoint on the GitHub App. Eliminates runner spin-up latency for command handling.
- **Dedicated bot identity** -- all HerdOS comments, reactions, and reviews come from the `herd-os[bot]` account instead of the GITHUB_TOKEN identity. This also fixes the PR review blocking issue: currently the PAT that creates batch PRs cannot submit "Changes Requested" reviews on them (GitHub prevents users from reviewing their own PRs). The GitHub App has a separate identity and can both create and review PRs, enabling the merge button to be blocked during active fix cycles.
- **Line-level review comments** -- agent review can submit GitHub review comments on specific lines when findings map cleanly to a diff hunk, instead of requiring users to translate every finding into a `/herd fix` comment.
- **Issue-driven planning** -- users create an issue describing a feature, mention @herd-os, and the agent decomposes and dispatches without the CLI. Multi-turn conversation in the issue thread refines the plan.

Slash commands (`/herd`) remain as a compatibility path, but the GitHub-native interaction model is App identity plus `@herd-os` mentions.

## v2 -- Multi-Agent Expansion

### Multi-Agent

Claude Code, OpenCode, Codex, and Pi are the v1 target provider set. Configure implemented providers with `agent.provider` in `.herdos.yml` (see [configuration.md](../configuration.md#agent-providers)).

Future provider candidates should only be added to this roadmap after they have a documented headless interface that fits HerdOS's provider contract.

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
