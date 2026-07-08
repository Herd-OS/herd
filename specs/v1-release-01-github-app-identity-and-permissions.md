# GitHub App Identity and Permissions

## Purpose

Make HerdOS operate as a GitHub-native bot identity instead of the human user's GitHub account.

## Why This Matters Before v1

Without a GitHub App, Herd-authored work is attributed to the user's PAT identity. That prevents normal GitHub review flows: the user cannot review or approve PRs authored by their own account, and Herd cannot cleanly submit blocking reviews under an independent identity.

## Initial Scope

- Official HerdOS GitHub App installation flow.
- Installation-token based API authentication.
- App-authored comments, reactions, reviews, and branch operations.
- `@herd-os` mention command entry point.
- Compatibility fallback for existing `/herd` commands.
- Clear App permission documentation.

## Open Questions

- What exact App slug/user name will be used publicly?
- Which permissions are required for the minimum v1 flow?
- Which PAT-based paths remain as compatibility fallback?
- How should self-hosted or enterprise users configure their own App?
