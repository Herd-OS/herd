# Line-Level Review Findings

## Purpose

Let HerdOS agent review submit GitHub review comments on specific changed lines when findings map cleanly to a PR diff.

## Why This Matters Before v1

PR-level review summaries are useful for broad findings, but concrete code issues should appear where developers expect them: on the changed line. Line-level comments also give fix workers better location context and reduce the need for users to manually translate findings into `/herd fix` prompts.

## Initial Scope

- Extend review output to include optional file, line, and diff-hunk metadata.
- Map agent findings to GitHub review comments when the target line is in the PR diff.
- Fall back to PR-level summary comments for broad or unmappable findings.
- Keep severity classification and fix-worker dispatch behavior intact.
- Add tests for mappable and unmappable findings.

## Open Questions

- Should line-level comments be used for all severities or only fix-triggering severities?
- Should the reviewer propose exact fix instructions in the line comment body?
- How should Herd handle stale line mappings after the PR head changes?
