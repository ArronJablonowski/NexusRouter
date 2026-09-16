# Installation, migration and rollback rehearsal

This rehearsal qualifies the mechanics of a DarwinRouter installation without
opening an operator configuration, database or model endpoint. It uses a native
release archive, private disposable directories, a providerless configuration
and one synthetic memory fact. It does not qualify live inference, production
signing, publication or another operating system or architecture.

## Automated local scaffold

Run the focused test from the repository root:

```sh
go test ./internal/releasepack \
  -run '^(TestNativeInstallMigrationRehearsal|TestExclusiveRehearsalCopyRejectsOverwrite)$' \
  -count=1 -v
```

The test builds the current native command with network module resolution
disabled, places it in a seven-entry release archive, and installs only the
contract-checked `darwin` member into a new versioned prefix. An actual candidate
must already have passed signature verification. The test checks the exact
version string, exclusive installation paths, private configuration and state
permissions, configuration validation, authenticated daemon status/stop, exact
owned-process exit, SQLite WAL mode, `quick_check`, and schema 52.

It then records a synthetic local-only memory fact, converts that owned fixture
to the real schema-29 boundary used by migration tests, and confirms its stored
body and existing task-timing epoch before proceeding. With the writer stopped
and the fixture proven to have
no running task, queued/running submission or unreleased resource lease, the
test checkpoints WAL and creates a mode-0600 backup using an exclusive file
create. It records the backup SHA-256, migrates the source database from schema
29 to 52 through normal daemon startup, and verifies the fact body, unchanged
schema-29 task-timing epoch, schema-30 empty usage-ledger metadata, schema-31
routing index, schema-32 durable submission-stream mapping integrity, and
schema-33 globally ordered committed-event ledger integrity. The
migration deliberately does not fabricate usage records for pre-ledger work.
Finally, the test copies the backup to a new rollback database, checks the digest,
schema, timing epoch, and original fact without migration, and proves the upgraded
database was not replaced. The schema-51 context-lineage and schema-52 delegated-
compaction authorities are also created empty for this legacy fixture and pass
their binding and semantic integrity checks.

Schema 33 admits only canonical runtime events that can fit individually within
the bounded committed-event SDK page, including worst-case cursor overhead. If
a legacy schema-32 database contains a larger event, migration fails and rolls
back completely rather than dropping or truncating history. Keep the source and
pre-upgrade backup unchanged, correct the source only through an explicitly
reviewed recovery procedure, and rerun migration; no automatic skip is provided.

Runtime subprocesses receive an explicit `DARWIN_PROCESS_OWNER_DIR`, home,
temporary directory and API token rooted under `testing.T.TempDir`. The build
subprocess uses the caller's Go cache and home but disables module network
resolution. No provider is configured. A loopback HTTP listener is used only
for the owned daemon's control API. The test log paths are temporary. During
candidate qualification, `native-release-evidence --install-rehearsal-out
NEW_EXTERNAL_FILE` passes the new destination through to this same rehearsal so
it can retain canonical evidence derived inline from the observations below. It
does not reconstruct a record later from an operator checklist.

The helper `rehearseNativeInstallAndMigration` accepts an already selected
native archive and its authenticated manifest metadata. The release
qualification test can call it after signature verification and before any
tampering test. Signature verification and public-key trust remain the caller's
responsibility.

## Candidate rehearsal record

The schema-1 record is bounded to 32 KiB and contains no filesystem paths,
credentials, configuration body, database content, host identity, user content,
or timestamps. It binds the exact semantic version, full source commit, native
OS/architecture, archive name and SHA-256. Fixed result fields record private
permissions, configuration validation, daemon start and exact owned-writer
exit, schema-29 quick-check and quiescence, immutable backup digest and
quick-check, schema-52 migration and synthetic-record preservation, unchanged
task-timing provenance, empty legacy usage ledger, and the schema-29 rollback
database digest plus the exact binary version/target used for its read-only
smoke check. The rollback digest must equal the backup digest.

The record is created mode 0600 with exclusive create through a pinned,
nonsymlink parent directory outside the source checkout. Existing destinations
are never replaced. Treat an absent record as a failed evidence-retention gate;
never synthesize one from a transcript.

Obtain its SHA-256 through the independent evidence channel, then verify exact
canonical bytes and every independently expected identity:

```sh
go run ./cmd/verify-install-rehearsal \
  --record /ABSOLUTE/EXTERNAL/EVIDENCE/install-TARGET.json \
  --record-sha256 sha256:EXPECTED_RECORD \
  --version 1.0.1 \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --target-os darwin \
  --target-arch arm64 \
  --artifact DarwinRouter_1.0.1_darwin_arm64.tar.gz \
  --artifact-sha256 sha256:EXPECTED_ARCHIVE \
  --source-schema 29 \
  --current-schema 52 \
  --backup-sha256 sha256:EXPECTED_BACKUP
```

Verification opens only a stable regular record file, rejects symlinks,
oversize data, duplicate/unknown/missing fields, noncanonical JSON, tampering,
and any release, commit, target, artifact, schema, or backup expectation
mismatch. Its JSON output repeats public identities only; it does not authorize
support, signing, installation, migration, rollback, or publication.
The record is unsigned mechanical evidence, not independent attestation that a
host ran the named operations; retain its bounded transcript and operator/host
provenance separately.

When the rehearsal record accompanies a schema-2 native-target record, verify
the pair offline as one identity-bound evidence set as well:

```sh
go run ./cmd/verify-native-release-evidence \
  --record /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json \
  --record-sha256 sha256:EXPECTED_NATIVE_RECORD \
  --install-rehearsal-record /ABSOLUTE/EXTERNAL/EVIDENCE/install-TARGET.json \
  --install-rehearsal-record-sha256 sha256:EXPECTED_INSTALL_RECORD \
  --version 1.0.1 \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --target-os darwin --target-arch arm64 --go-version go1.27.1 \
  --artifact DarwinRouter_1.0.1_darwin_arm64.tar.gz \
  --artifact-sha256 sha256:EXPECTED_ARCHIVE \
  --source-schema 29 --current-schema 52 \
  --backup-sha256 sha256:EXPECTED_BACKUP \
  > /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET-verification.json
```

Obtain both record digests and the expected public identity through the
operator-controlled evidence channel. Successful output proves that the exact
canonical bytes agree with those expectations and with each other. It does not
validate hardware provenance, virtualization status, transcript authenticity,
actual execution, or human approval. Retain and hash the canonical verification
JSON separately.

For an actual candidate, retain at least:

- Candidate version and full source commit.
- Independently verified manifest, checksums and signature references.
- Native operating system, architecture and binary version output.
- Absolute installation prefix, configuration path and database path.
- Configuration/state permissions and validation result.
- Source schema and previous binary version.
- Evidence that the exact old writer exited and no active work remained.
- Pre-upgrade backup path, SHA-256 and `quick_check` result.
- Migrated schema, preserved-record digest and daemon lifecycle result.
- Rollback binary prefix, restored database path/digest and smoke result.
- Canonical rehearsal-record location/digest and independent verification
  output, paired native/install verification output and digest, operator
  identity, host provenance, and UTC verification time.

Do not place credentials, private signing material, user prompts, model output
or the database itself in repository or CI logs.

## Real previous-binary rehearsal

The automated scaffold intentionally does not fetch or execute historical code.
Before approving a release that upgrades an existing installation, obtain the
previously distributed binary through its authenticated release channel and
verify its version and signature. Install it in a different versioned prefix.
Use it to create or open a disposable database matching the source schema and
write only synthetic evidence. Stop that exact process and wait for exit before
making the backup.

Start the candidate against the disposable source database and verify the
migration. Then show that the previous binary rejects the newer schema rather
than modifying it. Copy the immutable pre-upgrade backup to a new database path,
point the previous binary at that new path, and repeat its configuration,
startup, inspection and shutdown smoke checks. Never run an old binary against
the restored path until its digest and schema have been checked.

There is no previous public DarwinRouter release at the time of this document.
The automated schema-29 fixture proves the candidate migration code against an
owned boundary; it is not a historical binary or publication-channel test and
cannot substitute for a prior published release. Hosted CI must not fetch or
execute an ancestor implicitly.

## Safety and rollback limits

- Any database schema newer than 29, including current schema 52, is
  intentionally unsupported by the schema-29 binary. There is no supported
  in-place downgrade. Rollback means selecting the older binary and a restored
  matching backup as one pair.
- Restore to a new path. Do not overwrite, rename or edit the migrated database;
  retain it for inspection until the rollback decision is closed.
- Rollback discards all work committed after the backup. Record and approve that
  recovery point before an upgrade.
- Operational backup requires all old writers to be stopped. A control response
  saying `stopping` is insufficient; wait for the owned process to exit.
- Do not back up a database with running tasks, queued/running submissions or
  unreleased leases for this v1 procedure. Databases containing lease history
  can depend on retained process-owner guard paths for conservative recovery.
- Memory export is not a database backup. A main SQLite file copied without a
  quiescent writer and successful WAL checkpoint is not a qualified backup.
- The automated providerless configuration qualifies storage and daemon control
  only. A separate supervised smoke test must qualify Hermes/Ollama or other
  model execution.
- A local-built macOS binary lacks downloaded-file quarantine and does not prove
  Developer ID signing, notarization or Gatekeeper behavior.
- Native execution on Darwin/arm64 says nothing about Linux or Darwin/amd64.
  Each supported target needs its own native installation evidence.

The current storage opener creates new databases privately when the operator
has first created private directories. General protection against permissive
pre-existing paths or database symlinks is outside this rehearsal and must not
be inferred from its controlled fixture.
