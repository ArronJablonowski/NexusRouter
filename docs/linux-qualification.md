# Optional live Linux cgroup qualification

Run `make qualify-linux-cgroup` (or `sh scripts/qualify-linux-cgroup.sh`) from a checkout with Go, cached Go
dependencies, a running Docker daemon, and an already available `alpine:3.22`
Linux amd64 or arm64 image. This is an explicit developer qualification, not part
of `make check` or CI. The script does not download images or Go dependencies.

The script resolves the cached tag to an immutable image ID, requires the image to match the Docker server's architecture and
cross-builds NexusRouter for that architecture, then
runs only `nexus resources` in an ephemeral container with no network, a
read-only filesystem, all capabilities dropped, no new privileges, 512 MiB RAM,
no swap allowance, and a 1.5-CPU quota. Only the temporary built binary is mounted
into the container; the workspace, credentials, and Docker socket are not
mounted. Normal exit removes temporary binaries and captured output and attempts
cleanup of only the exact container ID created by this invocation. Uncatchable
signals or an unresponsive Docker daemon can prevent cleanup; inspect Docker
state after such interruptions. The resource command has a 15-second container-side timeout.
Docker calls and compilation have no independent wall-clock deadline; the script
depends on a responsive local Docker daemon and host toolchain. It prints the
Docker server platform and image ID/repository digests to identify the environment.

A host-side Go verifier checks the observed cgroup-v2 limits and diagnostic JSON:
the source must be `linux-proc-cgroup-v2`, total RAM must be positive and no more
than 512 MiB, available RAM must not exceed total RAM, and CPU capacity must be
one (the conservative floor of the 1.5-CPU quota). Unsupported cgroup layouts,
missing images, unavailable Docker, and ignored limits fail rather than skip.

This qualifies one live container resource profile. It does not establish OOM
recovery, thermal behavior, GPU scheduling, hidden-ancestor limits, cross-process
reservations, production model execution, or the entire Linux MVP. On macOS the
observed Linux kernel belongs to Docker's VM, not the macOS host.

## Verified run — September 5, 2026

The live command passed on Docker Linux/arm64 with Alpine image ID and repository
digest `sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce`.
The kernel exposed a 536,870,912-byte memory limit, a 150,000/100,000 CPU quota,
and zero swap allowance. NexusRouter reported the cgroup-v2 source, the same
total RAM limit, bounded available RAM, and one effective CPU.

Separately, Linux/arm64 test binaries for `resources` and `internal/app` passed
inside network-disabled, read-only Alpine containers with writable temporary
storage. Resource tests used 512 MiB and 1.5 CPUs; application tests used 1 GiB
and 2 CPUs. These cross-compiled tests did not enable the race detector; the
full native `make check` race suite passed separately on macOS. No production
models were called and no user workspace was mounted writable.
