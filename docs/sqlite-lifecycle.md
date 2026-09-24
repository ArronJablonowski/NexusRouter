# SQLite lifecycle and request overhead

The daemon dispatcher owns one fully validated telemetry store. Submission,
status polling, routing, explicit execution, and delegated execution borrow it
rather than reopening the database for each request. Borrowers never close it.
The dispatcher cancels and joins its workers before closing the store and
removing its binding. A Service used afterward returns to standalone open/close
behavior, including full validation. An unexpectedly closed active handle is an
error, not permission to open a replacement database silently.

A borrow checks the database file identity, schema cookie, supported schema
version, and WAL mode. Replaced files and changed schemas fail closed. Full
integrity, schema, and history validation still runs on every fresh Open;
per-operation journal and transactional invariants remain unchanged. This is
reuse of an already-open validated store, not a global cache of validation
results for arbitrary connections. It does not protect against malicious
in-place filesystem edits that race active database operations.

The connection limit remains one per Store. The SQLite DSN applies busy_timeout,
synchronous=FULL, and foreign_keys=ON to every connection, including a replacement
created by database/sql. No durability setting is weakened. Read-only standalone
open behavior and independent inspection/maintenance paths are unchanged.

## September 24, 2026 measurements

A consistent copy of the benchmark database contained approximately 570,000
events and occupied 598 MB. Using a deterministic in-process provider with the
same database and request, three standalone executions took 11,376, 12,661, and
11,836 milliseconds. After attaching the dispatcher-owned store, three executions
took 4.866, 2.446, and 2.270 milliseconds. These isolate database/application
setup overhead, not LLM generation speed or concurrent-load throughput.

On the live local-model HTTP endpoint, three requests before deployment took
27.73, 23.20, and 26.31 seconds. After restarting with the change, the first probe
took 30.22 seconds including startup wait; the subsequent probes took 1.48 and
0.95 seconds. All returned the same requested exact answer. Startup validation
remains substantial; the benefit is avoiding it on warm request paths.

## Verification and remaining scope

Tests cover shared ownership, completed submission execution, intentional
shutdown/detachment, unexpected close, cancellation, file replacement, schema
and version changes, journal-mode changes, ordinary writes, and forced SQL
connection replacement retaining safety settings. Existing dispatcher tests
exercise cancellation, concurrent work, idempotency, and restart recovery.

This change does not optimize every maintenance query, add indexes, remove
historical evidence, or increase writer concurrency. Profile connection wait time
and individual query plans before changing those policies. Startup and standalone
CLI validation costs still grow with history. Large-history measurements should
use a consistent backup, never modify production evidence for a timing test.
