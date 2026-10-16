# Automatic local cleanup

The standalone `scripts/cleanup-nexus-home.py` utility is dry-run by default. It does not call models, restart NexusRouter, or modify application databases. On the commander, a private copy runs daily at 03:30 local time through `com.nexusrouter.cleanup`, while the user session is available. Missed calendar events follow macOS launchd behavior; this is not a continuous monitor.

## Scope and safeguards

- Deployment snapshots live directly under `bin/backups`, with timestamped names. Keep the newest three by name. Validate their native rollback executable, configuration directory, complete file tree and SQLite integrity before pruning anything older. A missing, corrupt or incomplete retained snapshot blocks backup pruning. Nonempty database WAL files also block pruning.
- Any deployment backup modified within the last hour defers backup cleanup, allowing deployment staging to finish. In-use/configured old backups can make retention exceed three. Incomplete old snapshots are conservatively preserved for manual review.
- Only individually registered native QA binaries directly inside a resource run directory are eligible after seven days. Files must be at least 20 MiB, owned by this user, and recognized as ELF/Mach-O. Registration never deletes anything. It explicitly declares that the file is disposable, not a user document or permanent service binary. Unregistered binaries remain untouched.
- Never delete whole QA directories. Chat history, uploads, reports, source code, outputs, configuration, logs, current installed binaries and archived databases are outside the target policy. Registered binaries cannot be inside the development checkout.
- Check open files, mapped executables and working directories using `lsof`; check active command-line paths and NexusRouter LaunchAgent/Daemon configuration paths, including stopped services. Inventory failure defers cleanup. Recheck immediately before each deletion.
- Reject symlinks, foreign ownership, writable-by-others directories, hard-linked files, mounted subtrees and changed candidates. Use descriptor-relative deletion without following links. A nonblocking lock prevents overlapping maintenance and registration.
- Persist intent and per-item results privately in `tools/maintenance/last-cleanup.json`. A failure stops remaining deletions. The next run takes a fresh inventory and plan, rather than replaying stale intent. A crash during an old backup deletion may leave an incomplete old snapshot for manual review; the three retained snapshots are never deletion targets.

The inventory is a point-in-time check, not a kernel prohibition against a process opening a file afterward. Deployment/build tooling must take the same exclusive `tools/maintenance/cleanup.lock` while staging or using registered artifacts. Do not configure registered files as permanent services outside NexusRouter's service definitions. The age delay and repeated checks reduce races with existing tooling; this utility is not a defense against a hostile process running as the same user. Automated Linux scheduling and Spark cleanup are not enabled by this macOS job.

## QA build workflow

After creating a disposable QA executable, register its exact path relative to NexusRouter home:

```sh
/usr/bin/python3 -B "$HOME/.NexusRouter/tools/maintenance/cleanup-nexus-home.py" --register-qa resources/example-qa-2026-10-16/nexus-candidate
```

Never register original uploads, release artifacts, production service binaries or reports. Hold the maintenance lock throughout any future use of an old registered binary. Renamed/new binaries require registration; cleanup deliberately does not infer disposability from arbitrary filenames.

## Installation and operation

Qualify the source with the Python safety suite and `go test -race ./scripts` first. Copy the qualified script to the private `tools/maintenance` directory. Render `scripts/com.nexusrouter.cleanup.plist.in` with `__NEXUS_HOME__` replaced by the absolute home path using `plistlib` (XML escaping must be preserved). Save the user LaunchAgent with mode 0600. Run the installed script without `--apply` and review the preview before loading the LaunchAgent. After qualification, bootstrap it into the current user's launchd GUI domain and run one monitored invocation.

Manual preview uses no arguments; explicit cleanup uses `--apply`. Results are in the private last-run JSON and scheduler logs. Log files contain path/status metadata, never file contents or secrets. Scheduler logs are small append-only diagnostics; they are not themselves subject to cleanup. To disable the schedule, boot out only `com.nexusrouter.cleanup`; the commander remains running. Keep the utility and its registry private. Updating the installed copy requires repeating qualification and preview.
