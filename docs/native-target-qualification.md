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
  --version 1.0.0 \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter \
  --out /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json \
  > "$native_log"

shasum -a 256 "$native_log" \
  /ABSOLUTE/EXTERNAL/EVIDENCE/native-TARGET.json
```

The wrapper does not accept a target or gate result from the operator. It reads
`GOOS`, `GOARCH`, `GOHOSTOS`, `GOHOSTARCH`, and `GOVERSION` from the fixed local
Go environment and requires execution target and Go host to agree on one of the
four packaged target pairs. It checks clean commit binding, runs `make check`,
checks the source again, runs the version- and commit-bound
`make qualify-release`, and performs a final clean-source check before
exclusively creating the record.

Standard output is the complete gate transcript and standard error contains
only command diagnostics. Redirect stdout to the explicit operator-controlled
log as shown above; do not use the JSON record as a substitute for that log.
Each gate transcript is limited to 1 MiB. Output overflow, an output-write
failure, a failed gate, or a source change fails the command and leaves no JSON
record. A partial log may remain for diagnosis and must not be recorded as
successful evidence. Retain and independently record both file digests.

The canonical schema-1 JSON contains exactly one target and the five completed
gate names. Its scope is `single_native_target_only`; it has no field for
declaring another target qualified. `make qualify-release` supplies the native
version and disposable install/schema-migration/backup/rollback execution plus
cross-build inspection. Only the matching target execution is native evidence.
Cross-built archives and workflow runner labels are not evidence for another
OS or architecture.

The record is unsigned retained evidence, not a remote attestation or proof of
physical hardware. Preserve its SHA-256, command log, host/virtualization
provenance, UTC run time, reviewed source channel, and operator identity in the
release checklist. An operator must still decide the supported target matrix.
Do not combine several records into a broader claim unless every approved target
has its own reviewed native record and the checklist accepts its environment.

Hosted Ubuntu/macOS results may provide records only for the exact observed
`GOHOSTOS/GOHOSTARCH`; do not infer architecture from `ubuntu-latest` or
`macos-latest`. No workflow runner-label changes are part of this contract.
