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

## v2 -- Multi-Agent

Additional agent implementations (Codex, Cursor, Gemini CLI, OpenCode). Users can choose their preferred agent.

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
