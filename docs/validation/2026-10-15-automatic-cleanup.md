# Automatic maintenance qualification — 2026-10-15

Qualified standalone cleanup source SHA-256: `d6653789ff9cbac9274cdde1cd5fa81ac6b8aa2b41fcb5095215091cc809b2bd`.

The cleanup policy retains three newest verified deployment snapshots and only targets explicitly registered native QA executables after seven days. Backup staging receives a one-hour grace period. Reports, original uploads, live chat state, archives and source checkouts remain outside the deletion scope. In-use/configured candidates are preserved; failed inventory prevents deletion. See [policy and operational limits](../automatic-cleanup.md).

Validation before enabling:

- `/usr/bin/python3 -B -m unittest discover -s scripts -p 'test_cleanup*.py'`: 21 passed with system Python 3.9.6.
- `python3 -B -W error::ResourceWarning -m unittest discover -s scripts -p 'test_cleanup*.py'`: 21 passed with Homebrew Python 3.14.7. A fixture-only unclosed SQLite connection warning found during the initial run was corrected with explicit connection closure.
- `go test -count=1 -race ./scripts`: passed, 5.764s, including existing migration/license utility tests and the Python maintenance safety suite. `-count=1` avoids Go reusing cached results for externally loaded Python files.
- `go vet ./scripts`, `go run ./cmd/check`, and `git diff --check`: passed. Go tests compile the wrapper; `go build ./scripts` is inapplicable because this package contains only Go test files. The shipped utility is Python, tested with both interpreters; no product binary changes.
- Actual fixture deletions verified: five snapshots reduced to three, an old registered native build removed, repeated runs idempotent, and sentinel uploads/history/reports/source preserved.
- Failure/adversarial coverage: open files/cwd/configured executables/active worker references, real lsof inventory, new usage before deletion, corrupt/incomplete/WAL snapshots, fewer than three backups, recent files, unregistered binaries, traversal, symlinked homes/tools/files/subtrees, hard links, device boundaries, changed candidates, lock contention, inventory failure, and journaled unlink failure followed by a fresh successful run. Daily background schedule template is parsed and checked. A scheduler integration regression ensures its own home argument does not incorrectly mark every candidate as in use.
- Live dry-run on the existing home: zero eligible deletions and all three current rollback copies retained. An unrelated unreadable system-service plist initially blocked inventory; production inspection is now scoped to NexusRouter service definitions and no unrelated private service content is read.

This is scoped maintenance qualification, not a full application suite or hostile same-user isolation claim. Inventory cannot eliminate the race with an uncooperative process opening a file afterward. Future deployment/build tooling must honor the shared lock. Interrupted old-backup deletion may leave an incomplete old snapshot for manual review; retained backups are never deletion targets. Linux/Spark scheduling is out of scope. No old campaign evidence or grades are changed. Linear project access remains unavailable from the earlier reauthentication gap.

Installation and first scheduled invocation are recorded below after verification.
