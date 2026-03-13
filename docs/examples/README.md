# Example Configurations

Example `.herdos.yml` files for common project setups. Copy one to your repo root and adjust the `platform` section to match your repository.

## Examples

### [solo-dev.yml](solo-dev.yml)

For solo developers running HerdOS on personal projects. Minimal runner pool, no agent review (you review everything yourself), shorter timeouts. Good starting point if you're trying HerdOS for the first time.

### [small-team.yml](small-team.yml)

For teams of 2–5 developers. Larger worker pool, agent review enabled to catch issues before human review, conflict resolution via dispatch. Notifications go to the team lead.

### [ci-heavy.yml](ci-heavy.yml)

For projects with extensive CI pipelines. Higher fix-cycle limits so the integrator can iterate on CI failures and review feedback. Longer timeouts to accommodate complex builds.

## Usage

```bash
cp docs/examples/solo-dev.yml .herdos.yml
# Edit platform.owner and platform.repo
herd plan "add user authentication"
```
