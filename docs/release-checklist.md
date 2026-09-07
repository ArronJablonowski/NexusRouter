# Release operator checklist

Copy this template for one candidate release and retain it with the release
evidence. Do not mark an item complete without a durable evidence reference.
Completion records operator decisions; it does not make tests or signatures
prove more than their documented scope. Follow the [packaging and verification
procedure](release-packaging.md) and keep private keys, credentials, databases
and sensitive logs out of this record and the repository.

## Candidate identity

- Release version:
- Full 40-character reviewed source commit:
- Candidate branch or tag inspected:
- Release owner/operator:
- Checklist opened at (UTC):
- Proposed publication channel and audience:
- [ ] The version is approved and has no leading `v` in package-tool inputs.
- [ ] HEAD equals the recorded commit and the worktree is clean.
- [ ] Release notes identify this exact version, commit, date, supported targets
  and known limitations.

## License and notices

- DarwinRouter licensing policy: MIT, selected by the project owner to match
  Hermes Agent; see the repository-root `LICENSE`.
- Mechanical dependency inventory:
  [distribution dependency license inventory](dependency-license-inventory.md)
- Approved repository license and evidence:
- Dependency-license review evidence:
- Approved dependency notices location:
- Reviewer and decision time (UTC):
- [ ] The repository license is present and approved for distribution.
- [ ] Dependency licenses and required notices were reviewed for the shipped
  dependency graph and included through the approved publication channel.

## Supported-platform decision

Record `unsupported` explicitly rather than leaving a target ambiguous.
Cross-build or file-format inspection is not native execution evidence.
The current schema always emits and verifies all four targets, so any target
that is not approved blocks this release rather than permitting its archive to
be omitted.

| Target | Supported? | Native host evidence | Installer/config smoke evidence | Limitations |
| --- | --- | --- | --- | --- |
| darwin/amd64 |  |  |  |  |
| darwin/arm64 |  |  |  |  |
| linux/amd64 |  |  |  |  |
| linux/arm64 |  |  |  |  |

- Apple signing/notarization decision and evidence:
- Other distribution-channel requirements:
- Platform approver and decision time (UTC):
- [ ] The published target set contains only targets explicitly approved above.

## Qualification evidence

- Local `make check` host, time and result:
- Local version/commit-bound `make qualify-release` host, time and result:
- GitHub workflow run URL:
- Workflow-dispatched version:
- Workflow-dispatched commit:
- Ubuntu job native OS/architecture and outcomes:
- macOS job native OS/architecture and outcomes:
- [ ] Hosted and local evidence refer to the recorded candidate version and
  commit.
- [ ] Failed or skipped gates are resolved; none are counted as qualification.
- [ ] Remaining non-hermetic build and runner/action trust assumptions are
  accepted by the release approver.

## Installation and migration rehearsal

- Fresh-install host/result:
- Absolute configuration path used:
- Absolute database path used:
- Source database schema and binary version:
- Pre-upgrade backup location and digest:
- Backup restore test evidence:
- Old-writer shutdown evidence:
- Upgrade result and observed schema:
- Rollback binary prefix and matching data-backup reference:
- [ ] Configuration and state directories have reviewed private permissions.
- [ ] The fail-closed sample fields were replaced with reviewed model ID,
  `context_tokens`, `estimated_cost` and `ram_bytes` values.
- [ ] Upgrade and rollback were rehearsed without concurrent old/new writers.
- [ ] Known migration, guard-retention and downgrade limits appear in the final
  release notes.

## Production signing approval

- Signing key identifier (never the private seed):
- Trusted public-key fingerprint:
- Independent public-key publication URL/channel:
- Key custodian/authorized signer:
- Rotation and revocation procedure:
- Release approval policy reference:
- Signing host and time (UTC):
- [ ] The production key is dedicated to releases and is not a Git/SSH key.
- [ ] The private seed stayed outside the repository, artifacts and logs.
- [ ] A second operator obtained the public key through the independent trusted
  channel and verified the candidate directory successfully.

## Signed artifacts

- Artifact directory/evidence location:
- `manifest.json` SHA-256:
- `SHA256SUMS` SHA-256:
- `SHA256SUMS.sig` SHA-256:
- Independent verification operator, host, time and result:
- [ ] Manifest version and commit equal the recorded candidate identity.
- [ ] Exactly four approved archives, the manifest, checksums and signature are
  present; no unexpected files are included.
- [ ] Native staged binary reports the approved release version.

## Publication authorization

- Final release-notes review:
- Final artifact-set review:
- Publication approver:
- Explicit approval statement and time (UTC):
- Authorized publisher and destination:
- Planned tag and release identifier:
- [ ] Approval occurred after all preceding gates were completed.
- [ ] The tag points exactly to the recorded commit.
- [ ] Publication uses no implicit overwrite of an existing tag or artifact.

## Post-publication verification and rollback readiness

- Published release URL:
- Publication time (UTC):
- Re-downloaded artifact location:
- Independent re-download verification operator/time/result:
- Public-key retrieval channel used for re-verification:
- Installation smoke result from published bytes:
- Previous supported release/binary location:
- Matching pre-upgrade data backup location:
- Incident/rollback owner and procedure:
- [ ] Re-downloaded bytes passed signature, manifest and checksum verification.
- [ ] Published release notes, supported-target claims and public-key references
  match the approved records.
- [ ] Rollback materials remain retained and accessible to the operator.

## Final record

- Final state: `BLOCKED` / `PUBLISHED` / `WITHDRAWN`
- Blocking items or deviations:
- Final operator:
- Final time (UTC):
