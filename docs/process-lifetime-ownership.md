# Process-lifetime lease ownership

New resource leases are bound to private local execution-image ownership, in
addition to their task, owner and token. This is a prerequisite for safe orphan
reconciliation. Schema 23 now uses it for narrowly scoped
[terminal reader reclamation](terminal-reader-recovery.md) and
[orphan-worker failure recovery](orphan-worker-recovery.md), not general recovery.

## Ownership protocol

The first lease acquisition lazily creates one private `darwin-owner-*` directory
under `DarwinRouter/process-owners` in the operating system's user configuration
directory (on macOS, `~/Library/Application Support`; on Linux,
`$XDG_CONFIG_HOME` or `~/.config`). Set `DARWIN_PROCESS_OWNER_DIR` before startup
to select an alternate absolute directory. Its `owner.lock` file contains
an immutable random identity. DarwinRouter holds an exclusive, nonblocking OS
flock on that file for the execution image's lifetime. The owning file and
directory handles remain strongly referenced; there is no owning Close method,
finalizer or Store.Close cleanup. Every database and Store handle in that process
shares the same identity, avoiding a retained descriptor for every short-lived
database connection.

The selected root is created with mode 0700 when absent; an existing root must
already be private and owned by the effective user. Empty, relative, unclean,
invalid-text, insecure and final-symlink locations fail closed without chmod or
fallback to temporary storage. Canonical ancestor aliases are supported. New
guard directories are created through a pinned root handle; lock contents,
child-directory and root entries are synced before publishing a reference.
Missing root ancestors are created privately and synced with their parents.
This is not power-loss qualification. Root identity/permissions are checked at
creation; ongoing verification binds the individual guard directory and file,
not every ancestor's permissions. The process selects its location only on first
acquisition; later environment changes cannot rotate its established identity.

Schema 22 stores the canonical reference privately in `lease_processes` and adds
nullable `resource_leases.process_id`. Registration and lease insertion share a
transaction. The reference binds the random ID, canonical directory path, and
directory/file device and inode identities. Subsequent bound lease use verifies
the live guard and exact stored reference before it can mutate durable state.

Renewal, release, worker append/finalization and approval consumption require the
owning execution image. Another process cannot perform these operations merely
by knowing the token and logical owner. Existing exact-event acknowledgements
remain non-mutating; acknowledging an already committed record does not grant
execution, release, retry or approval authority.

Read-only and control-store opening does not create a guard. Public lease
inspection, metrics, route explanations and runtime events do not include the
private guard reference, filesystem paths or identities.

## Observation and trust boundary

The internal probe validates the reference, owner UID, private permissions,
regular-file type, link count, exact identity contents and pinned inode identities
before and after trying an independent nonblocking flock. Symlink, hardlink,
substitution, damaged permissions/content, missing paths and invalid metadata
produce an error, never an inferred exit. Probe does not create or repair paths.
Closing a probe does not release a live owner's independent lock.

An unlocked observation retains its probe lock until explicitly closed. The
terminal-reader reclaimer holds and revalidates that observation through its
database fencing transaction; a check followed by an unlocked gap is insufficient.

Descriptors have close-on-exec set. Consequently **process exit or exec-image
replacement** releases ownership. Exec can leave the same PID alive while
replacing every in-process Go callback. This is not general PID-death proof, nor
proof that descendant processes, remote generation, billing or external effects
have stopped. Arbitrary trusted handlers spawning detached activity are outside
this in-process ownership guarantee. The reclaimer additionally requires resolved
terminal task history and never releases a writer on this evidence alone.

## Platforms and retention

The initial implementation allows reported local APFS/HFS on macOS and
ext4/tmpfs/XFS/Btrfs on Linux. Network, overlay and other unqualified filesystems,
and other operating systems, fail closed for new lease acquisition. This is a
local deployment assumption, not distributed fencing or remote-filesystem
qualification. Linux cross-compilation is not evidence of Linux runtime behavior.

Guard directories are not automatically deleted. Existing guards are never
silently recreated after damage. New default locations avoid ordinary temporary
directory cleanup; an explicit temporary/tmpfs override does not. Legacy
temporary references remain at their original locations and are probed without
consulting the new setting. They are not moved or backfilled. Reboot removal,
permission changes, lost files or copied databases can still make ownership
unknown and block availability. Do not manually remove live or referenced guards.
Retained-guard garbage collection, disk-growth bounds and reboot/host identity
handling remain operational follow-up work; this change does not qualify
unattended daemon availability across those conditions.

`make check` and `make test` select a disposable private root for synthetic guard
fixtures, rather than writing the developer's application state. For direct
`go test` runs that acquire leases, set `DARWIN_PROCESS_OWNER_DIR` to a private
test directory. The production runtime never cleans up retained guards.

## Upgrade and remaining work

Stop older writers and back up databases before migrating to schema 22. Legacy
rows retain NULL process IDs and the previous token-based behavior; they are not
backfilled with invented ownership. Missing or legacy identity is never proof
that a lease can be reclaimed. Older binaries cannot open schema 22, and no
automatic downgrade or mixed-version writer support is provided.

Tests use real owned subprocesses to verify held versus unlocked observations,
SIGKILL and exec replacement, concurrent identity stability, probe isolation,
damaged-owner refusal without repair, and rejection of substituted references.
Storage tests cover Store.Close/reopen, migration preservation, atomic
registration rollback, corrupt metadata and foreign-process mutation/approval
denial. All resources and tokens in those tests are synthetic, not user records.

General holder discovery, reconciliation of workers with unfinished children,
release of unknown readers and orphaned writer leases, idempotent reassignment, persistent
operator attention and automatic continuation remain required PRD work. No
expiry-based release, model retry, tool retry, model disabling or approval policy
change is introduced by the ownership protocol. Terminal-reader reclamation has
its own atomic receipts and stricter eligibility conditions.
