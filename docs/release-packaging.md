# Release packaging and verification

Status: release preparation, not a published or fully qualified v1.0.0. The
tooling below creates local artifacts only. It does not create Git tags, upload
files, change a GitHub release, install binaries, or use the Git SSH key.

## Build from a reviewed checkpoint

Use a trusted Go toolchain matching `go.mod`, Git, and a clean committed checkout.
Run `make check` first. Use an explicit semantic version without a leading `v`
(prereleases such as `0.0.0-dev.1` are accepted; build metadata is not). Supply the
full lowercase 40-character commit ID printed by `git rev-parse HEAD`.

From the repository root, replace the uppercase placeholders:

```sh
go run ./cmd/package-release \
  --version 0.0.0-dev.1 \
  --commit FULL_REVIEWED_COMMIT_ID \
  --source /ABSOLUTE/PATH/TO/DarwinRouter \
  --out /ABSOLUTE/EXISTING/PARENT/new-release-directory
```

The output directory must not exist; its parent must exist and be under your
control. Prefer a directory outside the source checkout. A sibling `.lock` file
prevents cooperating packagers from using the same destination. All artifacts
are staged before an atomic no-replace directory rename on macOS or Linux. A
failed build does not publish a partial artifact set or overwrite an old one.
An abrupt process kill can leave private staging directories and the lock;
inspect their ownership and confirm the original process is gone before manual
cleanup. Do not delete an active packager's lock. Publication is not qualified
against power loss or a hostile process changing parent directories.

The packager checks that HEAD matches the supplied commit and that tracked and
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
  `THIRD_PARTY_NOTICES.txt`, `config.example.yaml` (all mode 0644), and the
  target executable `darwin` (mode 0755).
- `manifest.json`: schema version 2, release version, source commit, Go version,
  and the four ordered target names, archive SHA-256 hashes, and member
  name/mode/size/SHA-256 metadata.
- `SHA256SUMS`: sorted checksums of all four archives **and** the manifest.

Builds use `CGO_ENABLED=0`, `-mod=readonly`, `-trimpath`, `-buildvcs=false`, a
cleared linker build ID and an embedded CLI version. Workspace selection,
user Go settings, automatic toolchain downloads, ambient Go flags, experiments
and architecture tuning are disabled. Fixed archive headers omit timestamps,
owner names and build paths. Packaging has a 30-minute overall deadline;
individual commands have shorter bounds and bounded output capture.

Reproducibility means identical inputs, dependencies and toolchain produce
identical artifacts. This is not a hermetic build or independent provenance
attestation: the installed Git/Go executables, their location on PATH, module
cache and build host remain trusted. Missing public modules may be downloaded
through Go's normal module resolution. Packaging is not a model-runtime task
and is not covered by `mode: local_only` egress controls.

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
  --key /ABSOLUTE/PRIVATE_RELEASE_SEED_FILE
```

Signing first checks every payload and the exact manifest contract, then
exclusively creates `SHA256SUMS.sig`: 128 lowercase hex characters containing
the Ed25519 signature over the exact checksum-file bytes, followed by LF. An
existing signature is never replaced. A partial/uncertain signature write is
left for inspection; do not treat failure as permission to overwrite it.

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

Exit 0 means the signature, manifest and every declared file verified. Exit 1
means verification failed; exit 2 denotes invalid command arguments. The
verifier does not extract, run or install anything. Checks include safe
basenames, regular nonsymlink files, no extra/missing payloads, the exact four
target identities, exact ordered archive members and metadata, identical shared
collateral across targets, target-specific dependency notices, and agreement
between manifest and checksums. Manifest JSON must use the canonical schema-2
encoding emitted by the packager (two-space indentation, ordered fields, final
LF); editing or reformatting invalidates it.

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
release notes, exact target dependency notices and conservative local example.
After verification, follow that embedded `INSTALL.md`; its source counterpart is
[release-install.md](release-install.md). It checks all six members before
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
the native install/migration/backup/rollback mechanics, and proves archive
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
