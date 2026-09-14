# Manual hosted release qualification

The **Release qualification (no publication)** GitHub Actions workflow is
manual-only. Select a reviewed branch or tag and enter the intended semantic
release version plus independently reviewed canonical candidate-record and
license-evidence SHA-256 values when dispatching it. The required version
excludes a leading `v` and
build metadata and is validated before qualification. The workflow
checks out the dispatch event's immutable `github.sha`, verifies HEAD and a clean
worktree, independently re-derives and verifies the exact candidate record, and
runs the canonical version- and commit-bound native-evidence wrapper. No
separate commit input can redirect the checkout away
from the dispatched source.

Each Ubuntu/macOS matrix job independently freezes the candidate record and
requires its digest to equal the candidate-record dispatch input before
re-verifying it against the clean checkout. It independently freezes the
schema-3 license record through `scripts/license-evidence-bootstrap.sh`,
requires its digest to equal the dispatch input, re-verifies it against the
clean checkout, and records the expected digest and gate outcome. The record
binds the commit, exact `go.mod`/`go.sum` hashes, fixed reconstruction policy,
Go version/directive, root MIT license digest, all four target module/legal-file
closures, exact Go `LICENSE`/`PATENTS` hashes and rendered notice hashes. The
bootstrap compiles the evidence command with fresh private home, temporary,
module and build caches under the official proxy/checksum policy; it disables
private/direct fallback, VCS downloads, authentication, toolchain downloads and
telemetry, then removes the workspace. Freeze and verification each perform
dependency reconstruction in a separate fresh cache, so the job cannot succeed
from a populated setup-go or runner cache. Each job also records the requested version, verified commit,
actual Go host OS/architecture, Go version and other gate outcomes in its job
summary. The native wrapper itself runs `make check`, rechecks the source, runs
`make qualify-release`, and checks the source again before creating its record.
The four-job matrix uses explicit standard hosted-runner labels:
`macos-15-intel` for Darwin/amd64, `macos-15` for Darwin/arm64,
`ubuntu-24.04` for Linux/amd64 and `ubuntu-24.04-arm` for Linux/arm64. Each job
asserts both `GOOS`/`GOARCH` and `GOHOSTOS`/`GOHOSTARCH` against its expected
target before running a gate; a runner label alone is never native evidence.
These labels and capacities must be rechecked against GitHub's
[hosted-runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
before final dispatch because runner images continue to change.

Each qualification job builds all four seven-member release archives twice,
compares their unsigned bytes, verifies authenticated schema-3 member metadata,
shared collateral, target-specific SPDX 2.3 SBOMs and dependency notices,
verifies executable formats, exercises disposable-key signing and tamper
rejection, and rehearses
installation plus schema migration/backup/rollback with only the artifact
matching that job's asserted native platform. Other targets in that job are
cross-build evidence, not native execution evidence. Four-target support
requires four successful job summaries and four retained native evidence bundles
for the exact version and commit. Each bundle contains the primary native
record, canonical install-rehearsal record, bounded transcript, and canonical
install-only and combined native/install verification results. The combined
result is produced by the offline `verify-native-release-evidence` command from
separately computed record digests and public identity expectations. The
summary identifies failures and skipped gates; neither qualifies a release.

Each SBOM is checked against its exact binary digest, target dependency and Go
toolchain closure, and the clean commit's first-party Web UI source hashes. The
SBOM is a module-level inventory only: the workflow does not perform a
vulnerability scan, establish build provenance, reach a legal conclusion, or
resolve dependency licenses recorded as `NOASSERTION`.

The workflow has read-only repository permissions, pins the checkout and Go
setup actions to reviewed full commit IDs, disables checkout credential
persistence and Go cache uploads, sets the bootstrap parent to the runner's
temporary directory, forces the installed toolchain with
`GOTOOLCHAIN=local`, disables ambient Go environment/workspace/flag and
experiment settings, disables cgo, and fixes the documented amd64/arm64
architecture baselines. A pinned `actions/upload-artifact` step runs only after
successful native qualification and retains both canonical JSON records, the
bounded gate transcript, and both verification results for 30 days. It does not
upload release archives or publication assets. Test-generated archives,
installation, database, backup, rollback copy and keys remain disposable
runner-local files.
It does not use the repository SSH key, production signing secrets or live model
accounts. Standard Actions logs and summaries remain subject to repository
access and retention settings. Treat the workflow artifact as sensitive
operator evidence even though the qualification path uses no production secrets.

The independently dispatched candidate and license-evidence digests remain the
job's external inputs. The archive and backup digests cannot exist before the
rehearsal; the native wrapper emits them through one fixed, bounded transcript
line only after validating the generated companion record, and binds them into
the schema-2 primary native record. The job computes the companion-record digest,
checks those bindings, derives the expected archive name independently from the
dispatched version and asserted native target, and runs
`verify-install-rehearsal` with the observed archive/backup digests and fixed
schema boundary. It then runs `verify-native-release-evidence` over both retained
records, binding their independently computed record digests to the checked
version, commit, observed target, Go version, archive, schemas, and backup. This
catches truncation, noncanonical data, cross-record mismatch, and workflow
plumbing errors. It is same-job byte and identity verification, not independent
operator approval. It does not authenticate the transcript, prove physical
hardware or virtualization provenance, or attest that the recorded operations
occurred. A later reviewer must retrieve the bundle, obtain its digests and
expectations through the external evidence channel, and repeat the offline
verification before approval.

Standard hosted runners consume included Actions minutes for private
repositories and may incur metered charges afterward; see GitHub's current
[Actions runner pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing).
The release owner must also confirm the eight-build qualification fits the
current runner disk allocation rather than assuming capacity from a stale
document.

Running this workflow consumes hosted-runner time and requires the declared Go
toolchain and runner labels to be available to the repository. Adding the
workflow is not evidence of a hosted run: record the actual run URL, commit and
per-job outcomes, workflow artifact IDs, record/transcript digests and observed
targets after an operator dispatches it. Download the evidence before its
30-day retention expires. Matrix labels may change their
underlying architecture; do not infer four-platform coverage from two labels.

The expected candidate and license-evidence digests are independent dispatch
inputs, but the job does not authenticate the person or process that approved
them and does not retain either externally reviewed input record. Preserve those
exact records and human review evidence
in the operator-controlled release evidence channel. Successful runs do not
choose a distribution license, provision a production
signing identity, independently distribute a trusted public key, approve a
release version, or authorize publication. Those remain separate gates in
[release packaging](release-packaging.md).
