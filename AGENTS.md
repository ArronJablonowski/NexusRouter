# Project workflow

Use NexusRouter_PRD.md and the NexusRouter Linear project as the product requirements. Track partial implementation and verification gaps in docs/progress.md; do not equate passing unit tests with a complete MVP.

The user requires regular GitHub backups to git@github.com:ArronJablonowski/NexusRouter.git. Commit and push at meaningful verified development checkpoints, including before ending a development turn with material changes. Run the change-scoped validation below before pushing and record any verification limitations honestly. Never force-push or overwrite remote changes. Inspect remote state and reconcile safely if it diverges.

SSH is configured locally for this repository. Do not commit private keys, tokens, .env files, task databases, generated binaries, or sensitive session content. Review staged files before each commit.

## Change-scoped validation

A full `make check` is not required for every commit or GitHub push. Choose checks based on the changed behavior and its dependencies:

- Documentation-only changes: review the diff and run `git diff --check`; no application test suite is required.
- Code changes: run source formatting/static checks, build affected packages, and run tests for affected functionality and dependent integration boundaries. Use race-enabled tests for concurrency-sensitive changes.
- Web UI changes: run relevant browser, shell, accessibility, and interaction checks; inspect affected layouts and refresh behavior. Verify embedded assets and their reviewed manifest when assets change.
- Changes to authorization, permissions, storage, migrations, recovery, or task dispatch: run broader regression tests covering failure paths and related boundaries. Use the full suite when the impact crosses many subsystems or cannot be confidently bounded.
- Run the full `make check` for major releases, broad refactors, uncertain cross-system impact, or an explicit user request for full validation.

Reuse successful results when the relevant source, dependencies, test configuration, and environment remain unchanged. Do not rerun unrelated passing suites simply because another test failed. Investigate failures before retrying; fix the cause rather than weakening assertions or retrying until green.

Record the tested commit or source state, commands, results, and any untested scope in the work report or validation evidence. Targeted checks permit a normal GitHub push; never describe them as a full-suite pass. A running validation remains bound to its source checkout: do not edit it or start a duplicate gate. Deployment still requires a successful build, applicable targeted checks, backups, and an idle boundary that preserves active work and existing services.
