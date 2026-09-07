# Rollback-readiness evidence

`verify-rollback-readiness` is a read-only release gate. It verifies a canonical
rollback-readiness record against independently supplied expectations and an
independently observed, canonical post-publication receipt. It does not publish,
install, execute a candidate, open or mutate a database, restore a backup,
delete a release, or choose rollback policy.

The record binds the current repository, version, commit, tag, immutable GitHub
release ID, publication-authorization digest, post-publication receipt digest,
durable-state schema, rehearsal evidence, incident owner and HTTPS status
channel, and a time-bounded approval. The receipt is parsed as typed public
identity; an opaque digest or publisher success journal is not sufficient. The
receipt observer, rehearsal verifier, backup verifier, and approver identities
are independently supplied, and the approver cannot author those evidence
items.

## Explicit release-history policy

Operators must pass exactly one mode; the verifier never infers it:

- `first_release` records `approved_no_previous_public_release`, has no prior
  binary and no pre-upgrade backup, and requires a passed
  `first_release_install_recovery` rehearsal from schema 0. This is an explicit
  operator approval, not proof that no release exists. DarwinRouter currently
  does not invent a previous public release.
- `upgrade` records `not_applicable`, the exact prior supported binary identity
  (repository, version, commit, tag, target, artifact and binary digests,
  publication and verification receipt digests, and schema), an actual
  pre-upgrade backup digest/schema, and a passed
  `published_release_upgrade_rollback` rehearsal.

Canonical records use schema `1`, project `DarwinRouter`, and scope
`darwinrouter-rollback-readiness`. JSON must be the exact two-space-indented
encoding with one trailing newline. Generate it through the Go type and verify
its independently communicated SHA-256; hand-edited or reordered JSON is
rejected.

## Verification

For a first public release, supply every value from an independent trusted
channel (values below are placeholders):

```sh
go run ./cmd/verify-rollback-readiness \
  --record rollback-readiness.json --record-sha256 sha256:RECORD \
  --publication-receipt post-publication.json \
  --publication-receipt-sha256 sha256:RECEIPT \
  --publication-authorization-sha256 sha256:AUTHORIZATION \
  --receipt-verifier-id idp:release-observer \
  --repository ArronJablonowski/DarwinRouter --version 1.0.0 \
  --commit FULL40HEXCOMMIT --tag v1.0.0 --release-id 123 --current-schema 1 \
  --mode first_release \
  --rehearsal-evidence rehearsal.log --rehearsal-sha256 sha256:REHEARSAL \
  --rehearsal-verifier-id idp:rehearsal-reviewer \
  --incident-owner team:release-incident \
  --status-url https://status.example.invalid/darwinrouter \
  --approver-id idp:release-approver \
  --policy-url https://policy.example.invalid/rollback
```

For `upgrade`, additionally pass `--backup`, `--backup-sha256`,
`--backup-schema`, `--backup-verifier-id`, and all `--prior-*` identity flags.
Those flags are forbidden in `first_release` mode. Missing, mismatched, stale,
noncanonical, symlinked, self-authored, or changing evidence fails closed. An
approval is stale after its exact `valid_until`; approval must follow receipt
observation, rehearsal, and backup capture.

Successful output is only a compact verification summary. It is evidence that
the supplied readiness record matched the observed inputs at verification time,
not a promise that rollback will succeed later and not authority to execute one.
