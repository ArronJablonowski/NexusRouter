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
The qualification gate builds all four release targets twice, compares their
unsigned bytes, verifies formats, exercises disposable-key signing and tamper
rejection, and executes only the artifact matching that job's native platform.
Other targets are cross-build evidence, not native execution evidence. The
summary identifies failures and skipped gates; neither qualifies a release.

The workflow has read-only repository permissions, pins the checkout and Go
setup actions to reviewed full commit IDs, disables checkout credential
persistence and Go cache uploads, and has no publication or artifact-upload
step. Test-generated archives and keys remain disposable runner-local files.
It does not use the repository SSH key, production signing secrets or live model
accounts. Standard Actions logs and summaries remain subject to repository
access and retention settings.

Running this workflow consumes hosted-runner time and requires the declared Go
toolchain and runner labels to be available to the repository. Adding the
workflow is not evidence of a hosted run: record the actual run URL, commit and
per-job outcomes after an operator dispatches it. Matrix labels may change their
underlying architecture; do not infer four-platform coverage from two labels.

Successful runs do not choose a distribution license, provision a production
signing identity, independently distribute a trusted public key, approve a
release version, or authorize publication. Those remain separate gates in
[release packaging](release-packaging.md).
