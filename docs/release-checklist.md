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
- External canonical candidate-record location:
- Candidate-record SHA-256:
- Independent expected candidate-record SHA-256 source:
- Candidate-record verification host/time/result:
- [ ] The version is approved and has no leading `v` in package-tool inputs.
- [ ] HEAD equals the recorded commit and the worktree is clean.
- [ ] `release-candidate verify` re-derived the external candidate record from
  this exact clean commit; all decisions in the generated record remain
  intentionally unapproved and are resolved by this checklist.
- [ ] Release notes identify this exact version, commit, date, supported targets
  and known limitations.

## License and notices

- DarwinRouter licensing policy: MIT, selected by the project owner to match
  Hermes Agent; see the repository-root `LICENSE`.
- Mechanical dependency inventory:
  [distribution dependency license inventory](dependency-license-inventory.md)
- External canonical license-evidence record location:
- License-evidence schema, exact source commit and Go version/directive:
- Independently supplied license-evidence SHA-256 and source:
- License-evidence verification host/time/result:
- Distribution scope reviewed (binary/source/channel):
- Per-target module counts and `THIRD_PARTY_NOTICES.txt` SHA-256 values:
- Per-target `SBOM.spdx.json` SHA-256 values and validation results:
- Pinned official SPDX 2.3 schema revision/digest validation result:
- First-party Web UI source inventory/hash review:
- Go toolchain `LICENSE`/`PATENTS` hashes:
- Approved repository license and evidence:
- Dependency-license review evidence:
- Approved dependency notices location:
- Exceptions or additional attribution obligations:
- Reviewer and decision time (UTC):
- [ ] The repository license is present and approved for distribution.
- [ ] `license-evidence verify` re-derived the exact external record from this
  clean candidate and its independently supplied digest.
- [ ] All four target closures, legal-file hashes and rendered-notice hashes
  were reviewed for the exact toolchain and distribution scope.
- [ ] All four canonical SPDX 2.3 SBOMs match their target binary hashes,
  candidate-bound module/toolchain closures, and exact first-party Web UI
  source hashes.
- [ ] SBOM `NOASSERTION` values were treated as unresolved review inputs, not
  license approval; vulnerability scanning, build provenance, and legal review
  were completed separately where the publication policy requires them.
- [ ] Dependency and toolchain licenses, patent grants and required notices
  were approved for the shipped graph and included through the approved
  publication channel.

## Supported-platform decision

Record `unsupported` explicitly rather than leaving a target ambiguous.
Cross-build or file-format inspection is not native execution evidence.
Use the one-target [native qualification contract](native-target-qualification.md)
without inferring architecture from a workflow runner label.
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
- Workflow-dispatched expected license-evidence SHA-256:
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
- Canonical install-rehearsal record location and SHA-256:
- Independent rehearsal verification result, operator and UTC time:
- [ ] Configuration and state directories have reviewed private permissions.
- [ ] The fail-closed sample fields were replaced with reviewed model ID,
  `context_tokens`, `estimated_cost` and `ram_bytes` values.
- [ ] Upgrade and rollback were rehearsed without concurrent old/new writers.
- [ ] Known migration, guard-retention and downgrade limits appear in the final
  release notes.

## Production signing approval

- Signing key identifier (never the private seed):
- Trusted public-key fingerprint:
- Independently retrieved canonical trust-record location:
- Expected exact trust-record SHA-256 source (separate from release artifacts):
- Expected key ID and fingerprint source (separate from release artifacts):
- Independent public-key publication URL/channel:
- Key custodian/authorized signer:
- Rotation and revocation procedure:
- Release approval policy reference:
- External canonical signing-authorization record location:
- Independently supplied signing-authorization record SHA-256 and source:
- Authorization approver identity, policy evidence and UTC approval time:
- Signing host and time (UTC):
- [ ] The production key is dedicated to releases and is not a Git/SSH key.
- [ ] The private seed stayed outside the repository, artifacts and logs.
- [ ] The exact active trust-record bytes, separately supplied record SHA-256,
  key ID and key fingerprint all agree.
- [ ] The canonical schema-2 signing authorization binds this exact candidate,
  license-evidence digest, checksum set, trust record, key identity, supported
  targets, project-license approval and notice approval; its publication gate
  remains unapproved.
- [ ] The signing authorization and independently trusted record name the exact
  same reviewed release-policy URL.
- [ ] A second operator independently confirmed the trust and authorization
  inputs before the production seed was unlocked.

## Signed artifacts

- Artifact directory/evidence location:
- `build-approved-release` JSON result and command transcript location:
- Retained-build host and time (UTC):
- Independently confirmed candidate-record digest used by the build:
- Independently confirmed license-evidence digest used by signing:
- Independently approved exact `SHA256SUMS` SHA-256 source:
- `manifest.json` SHA-256:
- Per-target `SBOM.spdx.json` SHA-256 values:
- `SHA256SUMS` SHA-256:
- `SHA256SUMS.sig` SHA-256:
- `verify-approved-release` JSON result location and digest:
- Independent verification operator, host, time and result:
- [ ] Manifest version and commit equal the recorded candidate identity.
- [ ] The candidate record is canonical schema 2, the release manifest is
  canonical schema 3, and each archive has the exact seven-member contract.
- [ ] `build-approved-release` performed two isolated builds and the retained
  unsigned directory is one of those byte-compared outputs, not a later rebuild.
- [ ] The retained checksum digest equals the exact independently authorized
  `SHA256SUMS` digest supplied to production signing.
- [ ] Exactly four approved archives, the manifest, checksums and signature are
  present; no unexpected files are included.
- [ ] Native staged binary reports the approved release version.
- [ ] A second operator retrieved the trust record through the independent
  channel and successfully ran the approval-bound verifier with independently
  supplied candidate, license-evidence, checksum, authorization, trust and key
  identities.

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
- Canonical post-publication receipt location and SHA-256:
- Post-publication receipt verifier identity, host and UTC verification time:
- Public-key retrieval channel used for re-verification:
- Installation smoke result from published bytes:
- Canonical published-native install evidence location and SHA-256:
- Published native target, archive SHA-256, installed binary SHA-256 and version output:
- Disposable verification host identity/provenance and low-privilege account:
- Network-denial and credential-absence evidence:
- Published-install command transcript location:
- Incomplete output/install quarantine location and execution-state classification (if any):
- Rollback-readiness mode: `first_release` / `upgrade`
- Canonical rollback-readiness record location and SHA-256:
- Canonical rollback-readiness verification receipt location and SHA-256,
  verifier identity and UTC completion time:
- Rollback-readiness approval validity window:
- Previous supported release/binary location:
- Matching pre-upgrade data backup location:
- Incident/rollback owner and procedure:
- [ ] Re-downloaded bytes passed signature, manifest and checksum verification.
- [ ] Published release notes, supported-target claims and public-key references
  match the approved records.
- [ ] The canonical post-publication receipt binds the approved release, exact
  downloaded bytes and independent verification inputs.
- [ ] The host-matching archive was installed and executed from a fresh
  directory containing the exact receipt-bound downloaded byte set; its
  canonical evidence binds the completion time and observed binary
  digest and version output to both the receipt and full migration rehearsal.
- [ ] Native execution used a disposable low-privilege, credential-free,
  network-denied host; the retained transcript and independently delivered
  digest are reviewed, and any incomplete reservation is quarantined.
- [ ] Exactly one rollback policy is recorded: `first_release` explicitly
  approves that no previous public DarwinRouter release or pre-upgrade backup is
  claimed, or `upgrade` binds the authenticated prior binary and its matching
  pre-upgrade backup digest and schema.
- [ ] The canonical rollback-readiness record is within its approval validity
  window and passed read-only verification against the independently supplied
  post-publication, rehearsal and, when applicable, backup evidence.
- [ ] Rollback materials remain retained and accessible to the operator.

## Final record

- Final state: `BLOCKED` / `PUBLISHED` / `WITHDRAWN`
- Blocking items or deviations:
- Final operator:
- Final time (UTC):
