# Project workflow

Use NexusRouter_PRD.md and the NexusRouter Linear project as the product requirements. Track partial implementation and verification gaps in docs/progress.md; do not equate passing unit tests with a complete MVP.

The user requires regular GitHub backups to git@github.com:ArronJablonowski/NexusRouter.git. Commit and push at meaningful verified development checkpoints, including before ending a development turn with material changes. Run make check before pushing and record any verification limitations honestly. Never force-push or overwrite remote changes. Inspect remote state and reconcile safely if it diverges.

SSH is configured locally for this repository. Do not commit private keys, tokens, .env files, task databases, generated binaries, or sensitive session content. Review staged files before each commit.
