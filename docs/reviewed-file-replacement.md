# Reviewed existing-file replacement

The opt-in local `replace_file` tool replaces a complete UTF-8 regular file after
exact per-call approval. It is a limited editing primitive, not a shell, patch
engine, unattended agent, or filesystem sandbox.

```yaml
tools:
  enabled: true
  read_root: /absolute/narrow/project
  replace_enabled: true
  replace_root: /absolute/narrow/project
```

Keep both roots narrowly scoped and free of credentials. Replacement defaults
off. Terminal chat requires a terminal for both input and output. A trusted Go
SDK host can supply the same approval reviewer/presenter used for reviewed file
creation. Ordinary unattended callers and durable submissions are rejected;
delegated workers never receive this tool. File-tool admission excludes cloud
execution. In particular, this does not yet grant a Sol coordinator permission
to dispatch writes to its local delegated workers.

## Approval and execution

The three arguments are `path`, `expected_content`, and `content`. Each content
is limited to 64 KiB of UTF-8 bytes; they must differ. The target must already
exist within the pinned root and its parent must exist. Missing files, final
symlinks, directories, devices, FIFOs, traversal and special permission bits
are rejected. Use `create_file` for new files.

The terminal shows the complete old and new strings, ASCII-quoted to make
terminal controls inert, together with byte counts, SHA-256 digests, root, path,
metadata warning and exact approval ID. It rejects known configured credentials
instead of presenting a misleading redacted edit. No preview is truncated;
previews exceeding the complete 1 MiB bound are denied. SDK hosts are responsible
for an equally faithful review surface and must not treat model output as
operator consent. The full proposed content is sensitive durable session data.

The dispatcher consumes the exact approval before dispatch under the shared
`workspace` writer lease. The handler reads and checks the exact preimage twice,
stages and syncs the new bytes, and publishes with a rename. Readers see an
atomic replacement. This is **not an external-writer compare-and-swap**: unrelated
editors can change a file after the final check. Stop other writers while
reviewing/executing; leases fence only cooperating NexusRouter instances sharing
the durable lease store.

The backup contains the checked, reviewed preimage. It cannot recover an external
writer's intervening changes in the final-check-to-rename gap.

Basic permission bits are retained. Ownership, extended attributes, ACLs and
hard-link identity are not preserved by inode replacement. Do not use this tool
where those properties must survive. The warning is part of terminal approval.

## Recovery copies and uncertain outcomes

Before publication, the handler retains the original bytes in a private sibling
directory: `.darwin-replace-<random>/original` (directory mode 0700, copy mode
0600). A successful result contains its path relative to `replace_root` as
`backup`. Once publication is attempted, the copy is retained even if rename or
sync reports an error. Cancellation or a journal failure after an effect does
not authorize another execution. Confirmed and uncertain effects are never
automatically retried.

Inspect both the current target and the recovery copy before deciding whether
to restore. Restoration and removal of backups are separate operator actions;
NexusRouter does neither automatically. Backups contain the original file's
sensitive contents and require the same care as source files. This repository
ignores `.darwin-replace-*/`; add an equivalent exclusion to other configured
workspaces before use. Ignore rules are not access controls or secret detection.

Failures before publication normally remove staging artifacts; cleanup failure
is conservatively uncertain. A process crash can leave a private staging
directory even before publication. There is no startup cleanup or automatic
restoration. Full process-crash/power-loss qualification, arbitrary external
writer fencing, richer edits, delegated writes and stronger isolation remain
open.

Tests cover exact approval, stale content, recovery bytes and modes, pinned
roots, rejected targets, cancellation, consumed approval after journal failure,
and real macOS PTY approve/deny and pipe rejection. These fixtures are not a
claim of live-model editing qualification.
