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

## v2 -- Multi-Agent + GitHub App

Additional agent implementations (Codex, Cursor, Gemini CLI, OpenCode). Users can choose their preferred agent.

### GitHub App (@herd-os)

A GitHub App replaces the `/herd` comment prefix with `@herd-os` mentions:

- **Dedicated bot identity** -- `@herd-os[bot]` instead of `github-actions[bot]` for all HerdOS comments, reviews, and reactions
- **Agent-based command interpretation** -- instead of parsing rigid `/herd <command>` syntax, an agent interprets natural language: `@herd-os the deploy is failing because we're missing the Node version file` → agent calls the `fix-ci` tool with context
- **Tool call architecture** -- the same command functions (fix-ci, retry, review, fix) become tools available to the agent, identical to how LLM tool calls work. The agent decides which tool to call based on the user's message
- **Webhook-based handling** -- comment events arrive via GitHub App webhooks instead of `issue_comment` workflow triggers. This is faster (no runner startup) and doesn't consume Actions minutes for simple commands
- **Autocomplete** -- GitHub doesn't support autocomplete for bot mentions, but the agent interpretation makes exact syntax unnecessary
- **Installation flow** -- `herd init` detects whether the GitHub App is installed and configures accordingly. If not installed, it prints an installation link and falls back to `/herd` command syntax

The transition is seamless: the `internal/commands/` package and tool functions are shared between the `/herd` parser and the `@herd-os` agent. Users can use both simultaneously during migration.

### Issue-Driven Planning

Planning moves from the local CLI to GitHub Issues:

- **Create a planning issue** with label `herd/type:plan` describing the feature
- A workflow picks it up, launches the planner agent
- The agent comments on the issue asking clarifying questions
- The user replies with answers — each reply triggers the handler, which feeds the full comment thread to the agent for multi-turn conversation
- When the user approves (e.g., `/herd approve` or 👍 reaction), the agent creates batch issues and dispatches Tier 0
- The entire planning conversation lives on the issue — fully visible, searchable, linkable
- With the GitHub App, this becomes `@herd-os plan: Add user authentication` — the agent interprets the intent and starts planning

The `HandlerContext.IssueBody` field and `ListComments` access (already in the comment handler infrastructure) provide everything needed for multi-turn conversation without infrastructure changes.

## v3 -- Multi-Platform

GitLab and Gitea/Forgejo implementations of the Platform interface. Platform auto-detection from Git remote URL. Same `herd` CLI works against GitHub, GitLab, and Gitea repositories.

## v4 -- Multi-Repo and Formulas

- Cross-repo batches (track work across multiple repos)
- Formula system (reusable work decomposition templates)
- Worker templates (customizable worker behavior per task type)
- Performance metrics (worker success rate, average completion time)

## Future

- **Federation** -- coordinating HerdOS instances across organizations
- **Marketplace** -- shareable formulas and worker templates
- **Model A/B Testing** -- run different AI models on similar tasks and compare outcomes
