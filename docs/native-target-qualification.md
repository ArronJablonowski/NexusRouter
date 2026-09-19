# Native target qualification evidence

Status: local evidence tooling for DAR-51. This process does not approve a
supported-platform matrix, sign artifacts, notarize binaries, upload evidence,
or publish a release.

Run the wrapper separately on each target an operator proposes to support, from
an independently obtained clean checkout at the exact candidate commit. Place
the new record outside the checkout:

```sh
native_log=/ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.log

go run ./cmd/native-release-evidence \
  --version 1.0.1 \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter \
  --out /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json \
  --install-rehearsal-out /ABSOLUTE/EXTERNAL/EVIDENCE/install-TARGET.json \
  > "$native_log"

shasum -a 256 "$native_log" \
  /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json \
  /ABSOLUTE/EXTERNAL/EVIDENCE/install-TARGET.json
```

The wrapper does not accept a target or gate result from the operator. It reads
`GOOS`, `GOARCH`, `GOHOSTOS`, `GOHOSTARCH`, and `GOVERSION` from the fixed local
Go environment and requires execution target and Go host to agree on one of the
four packaged target pairs. It checks clean commit binding, runs `make check`,
checks the source again, runs `make qualify-mvp`, checks the source again, runs
the version- and commit-bound `make qualify-release-test`, and performs a final
clean-source check before exclusively creating the record.

The wrapper gives the package-serialized `check` command a 120-minute ceiling
and runs `qualify-mvp`, `qualify-context-recovery`, and the private
`qualify-release-test` target with separate 60-minute ceilings. The
public `make qualify-release` target preserves the same operator-facing order
with explicit sequential recursive invocations of `qualify-mvp` and then
`qualify-release-test`, including when the outer make enables parallel work;
splitting the
wrapper invocations prevents the MVP phase from consuming the release test's
45-minute ceiling.

The hosted matrix keeps a separate 60-minute allowance beyond those four gate
ceilings for checkout and toolchain setup, candidate
derivation, two fresh license-evidence reconstructions, verification, artifact
upload, and failure-aware reporting. That six-hour job limit is an execution
ceiling, not evidence that a timed-out or skipped gate passed.

Standard output is the complete gate transcript and standard error contains
only command diagnostics. Redirect stdout to the explicit operator-controlled
log as shown above; do not use the JSON record as a substitute for that log.
Each gate transcript is limited to 1 MiB. Output overflow, an output-write
failure, a failed gate, or a source change fails the command and produces no
acknowledged successful JSON record. A late low-level write or sync failure may
leave primary-record residue at the new destination; quarantine and inspect it,
and never treat it as evidence without command success and the matching bounded
transcript. A partial log may likewise remain for diagnosis and must not be
recorded as successful evidence. Retain and independently record both file
digests.

After transferring both retained records, verify them together through the
offline cross-record verifier. Supply expectations from the reviewed release
checklist and evidence channel rather than copying them out of either record:

```sh
go run ./cmd/verify-native-release-evidence \
  --record /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json \
  --record-sha256 sha256:EXPECTED_NATIVE_RECORD \
  --install-rehearsal-record /ABSOLUTE/EXTERNAL/EVIDENCE/install-TARGET.json \
  --install-rehearsal-record-sha256 sha256:EXPECTED_INSTALL_RECORD \
  --version 1.0.1 \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --target-os darwin \
  --target-arch arm64 \
  --go-version go1.27.1 \
  --artifact DarwinRouter_1.0.1_darwin_arm64.tar.gz \
  --artifact-sha256 sha256:EXPECTED_ARCHIVE \
  --source-schema 29 \
  --current-schema 53 \
  --backup-sha256 sha256:EXPECTED_BACKUP \
  > /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET-verification.json
```

The command is offline and emits one canonical JSON result only after both
bounded records, their supplied digests, all public identity fields, and their
cross-record bindings succeed. Retain and independently hash that result. It
validates record bytes and identity relationships; it does not authenticate the
gate transcript, establish physical-hardware or virtualization provenance,
prove that the recorded operations occurred, or supply human platform/release
approval.

Without a companion rehearsal destination, the canonical schema-1 JSON contains
exactly one target and the five completed gate names. With
`--install-rehearsal-out`, schema 2 additionally binds the exact companion-record,
native archive, and immutable backup digests plus the source/current schema
pair. Its scope remains `single_native_target_only`; it has no field for
declaring another target qualified. `make qualify-release` supplies the native
version and disposable install/schema-migration/backup/rollback execution plus
cross-build inspection. Only the matching target execution is native evidence.
Cross-built archives and workflow runner labels are not evidence for another
OS or architecture.

When `--install-rehearsal-out` is supplied, the nested native release gate
creates that second record exclusively after the actual selected archive has
completed installation, daemon, schema migration, backup, and rollback smoke
checks. The destination must be new, outside the source checkout, and beneath
an existing nonsymlink parent. Omitting the option retains no durable rehearsal
record even though the disposable gate still runs; release operators should not
omit it. See the independent verification procedure in
[installation, migration and rollback rehearsal](install-migration-rehearsal.md).
Both output destinations are checked before either expensive gate runs. When a
companion is requested, missing, existing, unsafe, malformed, noncanonical, or
version/commit/native-target-mismatched companion evidence prevents creation of
the primary native record. The bounded transcript exposes the companion,
archive, and backup digests generated by the rehearsal for same-job checking;
those generated observations are not external approval inputs.

The record is unsigned retained evidence, not a remote attestation or proof of
physical hardware. Preserve its SHA-256, command log, host/virtualization
provenance, UTC run time, combined verification JSON and digest, reviewed source
channel, and operator identity in the release checklist. An operator must still
decide the supported target matrix.
Do not combine several records into a broader claim unless every approved target
has its own reviewed native record and the checklist accepts its environment.

Hosted Ubuntu/macOS results may provide records only for the exact observed
`GOHOSTOS/GOHOSTARCH`; do not infer architecture from `ubuntu-latest` or
`macos-latest`. No workflow runner-label changes are part of this contract.
