# Manual hosted release qualification

The **Release qualification (no publication)** GitHub Actions workflow is
manual-only. Select a reviewed branch or tag and enter the intended semantic
release version when dispatching it. The required version excludes a leading
`v` and build metadata and is validated before qualification. The workflow
checks out the dispatch event's immutable `github.sha`, verifies HEAD and a clean
worktree, and runs `make check` followed by a version- and commit-bound
`make qualify-release`. No separate commit input can redirect the checkout away
from the dispatched source.

Each Ubuntu/macOS matrix job records the requested version, verified commit,
actual Go host OS/architecture, Go version and individual gate outcomes in its
job summary.
The four-job matrix uses explicit standard hosted-runner labels:
`macos-15-intel` for Darwin/amd64, `macos-15` for Darwin/arm64,
`ubuntu-24.04` for Linux/amd64 and `ubuntu-24.04-arm` for Linux/arm64. Each job
asserts both `GOOS`/`GOARCH` and `GOHOSTOS`/`GOHOSTARCH` against its expected
target before running a gate; a runner label alone is never native evidence.
These labels and capacities must be rechecked against GitHub's
[hosted-runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
before final dispatch because runner images continue to change.

Each qualification job builds all four six-member release archives twice,
compares their unsigned bytes, verifies authenticated schema-2 member metadata,
shared collateral and target-specific dependency notices, verifies executable
formats, exercises disposable-key signing and tamper rejection, and rehearses
installation plus schema migration/backup/rollback with only the artifact
matching that job's asserted native platform. Other targets in that job are
cross-build evidence, not native execution evidence. Four-target support
requires four successful job summaries for the exact version and commit. The
summary identifies failures and skipped gates; neither qualifies a release.

The workflow has read-only repository permissions, pins the checkout and Go
setup actions to reviewed full commit IDs, disables checkout credential
persistence and Go cache uploads, forces the installed toolchain with
`GOTOOLCHAIN=local`, disables ambient Go environment/workspace/flag and
experiment settings, disables cgo, and fixes the documented amd64/arm64
architecture baselines. It has no publication or artifact-upload step.
Test-generated archives, installation, database, backup, rollback copy and keys
remain disposable runner-local files.
It does not use the repository SSH key, production signing secrets or live model
accounts. Standard Actions logs and summaries remain subject to repository
access and retention settings.

Standard hosted runners consume included Actions minutes for private
repositories and may incur metered charges afterward; see GitHub's current
[Actions runner pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing).
The release owner must also confirm the eight-build qualification fits the
current runner disk allocation rather than assuming capacity from a stale
document.

Running this workflow consumes hosted-runner time and requires the declared Go
toolchain and runner labels to be available to the repository. Adding the
workflow is not evidence of a hosted run: record the actual run URL, commit and
per-job outcomes after an operator dispatches it. Matrix labels may change their
underlying architecture; do not infer four-platform coverage from two labels.

Successful runs do not choose a distribution license, provision a production
signing identity, independently distribute a trusted public key, approve a
release version, or authorize publication. Those remain separate gates in
[release packaging](release-packaging.md).
