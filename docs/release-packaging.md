# Release packaging and verification

Status: release preparation, not a published or fully qualified v1.0.0. The
tooling below creates local artifacts only. It does not create Git tags, upload
files, change a GitHub release, install binaries, or use the Git SSH key.

## Build the retained reviewed candidate

Use a trusted Go toolchain matching `go.mod`, Git, and a clean committed checkout.
Run `make check` first. Freeze and independently review the canonical candidate
record as described in [release candidate identity](release-candidate.md). The
record supplies the semantic version and full source commit; its exact SHA-256
must come from the independent review channel, not from the path handed to the
build operator.

From the repository root, replace the uppercase placeholders:

```sh
go run ./cmd/build-approved-release \
  --source /ABSOLUTE/PATH/TO/DarwinRouter \
  --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
  --candidate-record-sha256 sha256:EXPECTED_EXACT_CANDIDATE_RECORD_SHA256 \
  --out /ABSOLUTE/EXISTING/PARENT/new-release-directory
```

The output directory must not exist; its parent must exist and be under your
control and outside the source checkout. The command re-derives the candidate
record from the exact clean commit, performs two complete four-target builds in
separate private directories, validates both unsigned release sets, and directly
compares every byte of all four archives, the manifest and checksums. It then
uses an atomic no-replace rename to retain the first compared directory. It does
not perform an unverified third build or copy artifacts into a new release set.
The one-line JSON result reports the independently supplied candidate digest and
the exact retained `SHA256SUMS` digest; record it, but have the signing approver
obtain the expected checksum digest through the approved evidence channel.

A sibling approval-build lock prevents cooperating builders from targeting the
same destination. A failure before retention does not publish a partial set or
overwrite an old one. An I/O or source-consistency failure after the destination
appears leaves that directory for inspection and must not be treated as
permission to rerun or overwrite it. An abrupt process kill can leave private
staging directories and locks; inspect their ownership and confirm the original
process is gone before manual cleanup. Do not delete an active builder's lock.
Power-loss durability and hostile mutation of the operator-controlled parent
directory remain outside this local build guarantee.

Each underlying packaging pass checks that HEAD matches the candidate commit and that tracked and
untracked work is clean before and after the build. It materializes one private
snapshot directly from the commit's Git blobs, then builds all targets from
that snapshot. Ignored files, working-tree changes, Git export attributes and
checkout filters cannot add source to the build. Git replacement objects are
disabled. Submodules, symbolic links and filesystem `replace` directives in
`go.mod` are rejected rather than silently using unbound inputs. Snapshot bounds
are 10,000 regular files, 1 MiB per file and 64 MiB total.

Artifacts are produced for macOS (`darwin`) and Linux, both `amd64` and `arm64`:

- Four `DarwinRouter_VERSION_OS_ARCH.tar.gz` archives. Each contains, in exact
  order, `INSTALL.md`, `LICENSE`, `RELEASE_NOTES.md`,
  `SBOM.spdx.json`, `THIRD_PARTY_NOTICES.txt`, `config.example.yaml` (all mode
  0644), and the target executable `darwin` (mode 0755).
- `manifest.json`: schema version 3, release version, source commit, Go version,
  and the four ordered target names, archive SHA-256 hashes, and member
  name/mode/size/SHA-256 metadata.
- `SHA256SUMS`: sorted checksums of all four archives **and** the manifest.

Each target-specific `SBOM.spdx.json` is canonical SPDX 2.3 JSON. It identifies
the target binary and its SHA-256, the `cmd/darwin` module dependency closure,
the exact Go toolchain component, and every checked-in first-party Web UI source
asset with its SHA-256. `GENERATED_FROM` relationships connect the embedded
binary to those frontend sources. Dependencies whose license expression has not
been mechanically established use `NOASSERTION`; the separate notice bundle and
human license review remain authoritative for release approval.

The document creation time is the source commit's committer timestamp converted
to whole-second UTC. Candidate schema 2 and manifest schema 3 freeze that value,
and approved verification re-derives it before key access. Qualification also
validates generated documents offline against the official SPDX 2.3 JSON schema
pinned to the dereferenced `v2.3` commit and recorded digest. The project package
declares MIT but leaves its composite license conclusion, executable file, and
individual source-file conclusions as `NOASSERTION`; `filesAnalyzed` remains
false and no package-to-file containment is claimed.

This is a module-level inventory, not a package-file or transitive vulnerability
scan, build-provenance attestation, legal conclusion, or statement that every
dependency license was reviewed. It records no CVEs and does not replace the
candidate-bound license evidence or `THIRD_PARTY_NOTICES.txt`.

Builds use `CGO_ENABLED=0`, `-mod=readonly`, `-trimpath`, `-buildvcs=false`, a
cleared linker build ID and an embedded CLI version. Workspace selection,
user Go settings, automatic toolchain downloads, ambient Go flags, experiments
and architecture tuning are disabled. Fixed archive headers omit timestamps,
owner names and build paths. Packaging has a 30-minute overall deadline;
individual commands have shorter bounds and bounded output capture.

Reproducibility means identical inputs, dependencies and toolchain produce
identical artifacts. Packaging resolves one absolute Go executable, requires it
to match `GOROOT/bin/go`, and rechecks its file identity and executable mode
through the four-target build. This is still not a hermetic build or independent
provenance attestation: the selected Git/Go binary contents, initial PATH,
module cache and build host remain trusted. Missing public modules may be
downloaded through Go's normal module resolution. Packaging is not a
model-runtime task and is not covered by `mode: local_only` egress controls.

`cmd/package-release` remains the lower-level one-pass packaging primitive for
tests and diagnostics. It accepts explicit version and commit inputs but does
not prove reproducibility or bind an independently reviewed candidate record;
do not use it to create the production signable directory.

## Freeze and approve candidate license evidence

Before authorizing production signing, freeze and independently review the
canonical schema-2 license record described in the
[dependency license inventory](dependency-license-inventory.md). Its digest
must be obtained through the release-evidence channel and entered in the
operator checklist. Re-derive it from the exact clean candidate with:

```sh
DARWIN_LICENSE_EVIDENCE_RECORD=/ABSOLUTE/INDEPENDENT/LICENSE_EVIDENCE.json \
DARWIN_LICENSE_EVIDENCE_SHA256=sha256:EXPECTED_EXACT_LICENSE_EVIDENCE_SHA256 \
  make qualify-license-evidence
```

This mechanical gate does not decide whether distribution is legally approved.
An authorized human must review the complete project, dependency and toolchain
terms for the intended binary/source channels. The external signing
authorization then binds the exact reviewed evidence digest and separately
records `project_license: approved` and `third_party_notices: approved`.

## Sign with a separate release identity

A release signing identity must be provisioned by the operator independently
of repository access. **Never use `git_repo_DarwinRouter_ed25519` or any other
Git/SSH authentication key.** No production release key is generated or
discovered automatically.

The signing command accepts a dedicated Ed25519 seed encoded as exactly 64
lowercase hexadecimal characters (32 bytes), optionally followed by one LF.
The regular, nonsymlink seed file must have mode 0400 or 0600. This is a raw
Ed25519 seed, not an OpenSSH, PEM or DER private-key file. Keep it outside the
repository and artifact directory, in operator-controlled secret storage. The
corresponding trusted public-key file is 64 lowercase hex characters with the
same optional LF; it is not secret.

```sh
go run ./cmd/sign-release \
  --dir /ABSOLUTE/RELEASE_DIRECTORY \
  --key /ABSOLUTE/PRIVATE_RELEASE_SEED_FILE \
  --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
  --candidate-record-sha256 sha256:EXPECTED_EXACT_CANDIDATE_RECORD_SHA256 \
  --license-evidence /ABSOLUTE/INDEPENDENT/LICENSE_EVIDENCE.json \
  --license-evidence-sha256 sha256:EXPECTED_EXACT_LICENSE_EVIDENCE_SHA256 \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter \
  --expected-sums-sha256 sha256:EXPECTED_EXACT_SHA256SUMS_SHA256 \
  --trust-record /ABSOLUTE/INDEPENDENT/TRUST_RECORD.json \
  --trust-record-sha256 sha256:EXPECTED_EXACT_TRUST_RECORD_SHA256 \
  --key-id EXPECTED_RELEASE_KEY_ID \
  --key-fingerprint sha256:EXPECTED_RELEASE_PUBLIC_KEY_SHA256 \
  --authorization-record /ABSOLUTE/INDEPENDENT/SIGNING_AUTHORIZATION.json \
  --authorization-record-sha256 sha256:EXPECTED_EXACT_AUTHORIZATION_SHA256
```

Every digest and identity above is an approval input and must be obtained from
the recorded independent evidence channel, not calculated ad hoc by the signer
from whichever paths were supplied. Candidate, license-evidence and trust-record
digests cover their exact canonical bytes, including the `sha256:` prefix in
command inputs.
The external canonical [signing authorization](release-signing-authorization.md)
must bind those same candidate, license-evidence, checksum, trust and key
expectations, explicitly approve the project license, all four targets,
dependency notices and production signing, and leave publication unapproved.
Its exact digest is supplied independently as well.

Before opening the private seed, production signing re-verifies the candidate
record and canonical schema-2 license evidence against the clean exact source
commit, validates the active trust record and signing authorization, checks
every payload and the manifest contract, binds manifest identity and shared
collateral to the candidate, and matches the exact approved `SHA256SUMS`
digest. The license evidence records the exact Go toolchain and directive,
root MIT license, four target dependency closures, legal-file hashes and
rendered notice hashes. Each archive's SPDX document must also match that target
closure, the clean source's first-party Web UI hashes, and the exact binary
digest. The authorization and trust record must name the exact
same release-policy URL. It then proves the seed-derived public key matches the
expected trust identity. Signing exclusively creates `SHA256SUMS.sig`: 128
lowercase hex characters containing the Ed25519 signature over the exact
checksum-file bytes, followed by LF. It syncs the signature and containing
directory, immediately verifies the complete signed set with the approved public
key, and rechecks the source checkout. An existing signature is never replaced.
A partial/uncertain signature or late source-change failure is left for
inspection; move the directory aside and do not treat failure as permission to
overwrite or retry it.

The Go-level raw `releasepack.Sign` compatibility helper exists only for
disposable-key qualification and legacy SDK tests. It does not enforce these
production approval bindings and must not be used for a release ceremony.

This authenticates the complete archive bytes through signed checksums. It is
not Apple Developer ID signing/notarization, Authenticode or a hosted build
attestation. Those require separate provisioning and qualification if needed
for a distribution channel.

## Verify before extracting or running

Use the verifier from an independently trusted DarwinRouter source checkout,
not a binary from the unverified download. Obtain the trusted public key through
an authenticated channel separate from the release archive. A public key
downloaded beside an attacker-replaced archive does not establish trust.

```sh
go run ./cmd/verify-release \
  --dir /ABSOLUTE/RELEASE_DIRECTORY \
  --public-key /ABSOLUTE/INDEPENDENTLY_TRUSTED_PUBLIC_KEY
```

For production verification, prefer the canonical independently published
trust record plus an expected key ID and fingerprint obtained separately from
the release artifacts. Follow [release-signing identity and trust
policy](release-signing-trust.md). The independent production verifier also
binds the candidate, license evidence, checksum set and signing authorization
rather than checking the public key alone:

```sh
go run ./cmd/verify-approved-release \
  --dir /ABSOLUTE/RELEASE_DIRECTORY \
  --source /ABSOLUTE/PATH/TO/INDEPENDENT/CLEAN/DarwinRouter \
  --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
  --candidate-record-sha256 sha256:EXPECTED_EXACT_CANDIDATE_RECORD_SHA256 \
  --license-evidence /ABSOLUTE/INDEPENDENT/LICENSE_EVIDENCE.json \
  --license-evidence-sha256 sha256:EXPECTED_EXACT_LICENSE_EVIDENCE_SHA256 \
  --expected-sums-sha256 sha256:EXPECTED_EXACT_SHA256SUMS_SHA256 \
  --trust-record /ABSOLUTE/INDEPENDENT/TRUST_RECORD.json \
  --trust-record-sha256 sha256:EXPECTED_EXACT_TRUST_RECORD_SHA256 \
  --key-id EXPECTED_RELEASE_KEY_ID \
  --key-fingerprint sha256:EXPECTED_RELEASE_PUBLIC_KEY_SHA256 \
  --authorization-record /ABSOLUTE/INDEPENDENT/SIGNING_AUTHORIZATION.json \
  --authorization-record-sha256 sha256:EXPECTED_EXACT_AUTHORIZATION_SHA256
```

Its one-line JSON result records the exact candidate, license-evidence,
authorization, checksum, trust and key inputs plus the signature-file digest
observed by that verification. Retain it with the independent operator, host
and UTC time. The verifier re-derives the license evidence, validates the clean
source/candidate binding, authorization/trust policy agreement, active key
identity, exact checksum set, Ed25519 signature and every declared artifact
twice through one pinned release root. It remains a point-in-time local
observation, not remote attestation or publication approval. Raw public-key
mode remains useful for disposable qualification and emergency diagnosis.

Exit 0 means the signature, manifest and every declared file verified. Exit 1
means verification failed; exit 2 denotes invalid command arguments. The
verifier does not extract, run or install anything. Checks include safe
basenames, regular nonsymlink files, no extra/missing payloads, the exact four
target identities, exact ordered archive members and metadata, identical shared
collateral across targets, target-specific dependency notices, and agreement
between manifest and checksums. Each target SBOM must be canonical SPDX 2.3,
target-bound, source-bound and binary-bound. Manifest JSON must use the
canonical schema-3 encoding emitted by the packager (two-space indentation,
ordered fields, final LF); editing or reformatting invalidates it.

Use a quiescent directory you control for signing, verification and subsequent
installation. Checks are point-in-time observations, not a lock preventing
another process from modifying files afterward. A valid signature establishes
the signing key's approval of bytes, not that the code is safe or the claimed
build process independently occurred.

After verification, map the host identity to an archive name. `uname -s` values
`Darwin` and `Linux` map to `darwin` and `linux`; `uname -m` values `x86_64` and
`amd64` map to `amd64`, while `arm64` and `aarch64` map to `arm64`. Any other
value is unsupported and must stop installation rather than guessing.

The authenticated archive carries its own `INSTALL.md`, project license,
release notes, target-specific SBOM, exact target dependency notices and
conservative local example.
After verification, follow that embedded `INSTALL.md`; its source counterpart is
[release-install.md](release-install.md). It checks all seven members before
extraction, uses a new versioned user-controlled prefix, installs documentation
and the binary, and requires deployment-specific review of the fail-closed
example. Do not replace an existing prefix; retain the previous binary and its
matching database backup for rollback. No automatic installer, PATH change,
symlink switch or system-wide write is provided.

## Repeatable qualification and remaining release gates

After committing changes and with a clean worktree, run:

```sh
DARWIN_RELEASE_VERSION=1.0.0-rc.1 \
DARWIN_RELEASE_COMMIT=FULL_REVIEWED_COMMIT_ID \
  make qualify-release
```

Both values are required. The version must use the same restricted semantic
version syntax as `package-release`, without a leading `v` or build metadata,
and the commit must be the full lowercase ID of the current clean checkout.
Qualification fails before packaging if either identity is invalid or HEAD does
not equal the supplied commit.

This opt-in target first runs the deterministic [MVP qualification](mvp-qualification.md),
then builds all four targets twice from separate private snapshots, compares
every unsigned output byte, checks executable platform/architecture, exercises
the package/sign/verify CLIs, signs/verifies using disposable test keys, compares
library/CLI signatures, runs the native binary's version command, and proves
the native install/migration/backup/rollback mechanics and proves archive
tampering is rejected. It uses temporary files removed by the test
framework. It does not sign with an operator identity, publish artifacts, run
live model inference, or modify user configuration/databases.
Ordinary `make check` skips this expensive test but covers packaging primitives,
manifest validation, key handling, tampering, no-overwrite and source isolation.

Before a real release:

- Keep the DAR-45 deterministic MVP gate passing and complete the remaining PRD
  acceptance; passing packaging tests alone does not qualify the agent runtime.
- Confirm the committed MIT `LICENSE` is present in the exact candidate and
  complete the separate review and approval of third-party dependency notices;
  the notice generator does not make a legal approval decision.
- Provision a dedicated release key and independently publish its public-key
  trust record, rotation/revocation procedure and release approval policy.
- Record hosted CI results and native execution on supported release targets.
  Cross-building alone is not Linux runtime or Intel Mac qualification.
- Review version-specific installation/migration notes and known limitations,
  back up user databases, and stop old writers before schema upgrades.
- Produce approved signed artifacts and release notes from the reviewed commit,
  then explicitly authorize publication. No `v1.0.0` tag is implied by the PRD's
  document version or this development checkpoint.

Use the recordable [release checklist](release-checklist.md) to bind these gates,
the signing identity, publication authorization and post-publication verification
to one version and reviewed commit. An incomplete checklist is a blocked release,
not authority to infer or waive a decision.
