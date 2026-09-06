# Terminal reader reclamation

The daemon's dispatcher now reclaims a narrow, verifiable class of orphaned
resource leases. It does not resume tasks, rerun models/tools, release writers,
or turn a candidate result into accepted output.

## Eligibility

Every condition must hold:

- The lease is an unreleased reader with valid schema-22 process ownership.
- Its original execution-image guard exists, passes identity/permission/content
  validation, and can be exclusively locked by an independent probe.
- A complete, bounded replay agrees with the task's terminal head: completed,
  failed or canceled, with no pending tool, uncertain effect or interrupted turn.
- The exact lease, process reference and terminal state remain valid inside the
  reclamation transaction. The acquired probe is revalidated before commit.

The probe lock stays held through the database commit. Its public descriptive
state cannot be modified to forge an acquired proof; confirmation checks private
acquisition state and rejects closed or damaged observations.

Lease expiry is **not** proof of owner termination and is not needed when valid
unlocked ownership and resolved terminal work are proven. Alive, unknown,
legacy, malformed, running and effect-unresolved cases remain unchanged. Writers
remain outside automatic reclamation even after process exit.

## Durable change and scheduling

Schema 23 adds private `lease_recoveries` records. Reader release and one immutable
receipt per lease commit atomically. Receipts contain the task terminal sequence
and state, time, reason, process identity and binding digests—not the raw lease
token, model output or guard path in the receipt body. The private table key still
references the lease token. No public API exposes those capabilities.

An already reclaimed lease is not modified again; exact repeat inspection leaves
the receipt timestamp, journal and release state unchanged. Failed transactions
roll back both receipt and release. Ordinary released leases do not acquire
invented recovery receipts.

At startup and each reconciler tick, the dispatcher first processes its existing
submission-recovery page, then visits at most 32 terminal-reader candidates using
an independent private rowid cursor. Each page shares a five-second cooperative
deadline. Unknown holders and candidate failures do not permanently pin later
rows; operational/corruption errors degrade supervisor health. A complete pass
resets the cursor. Bounds constrain returned candidates and replay, not the total
database scan cost or arbitrary filesystem-call latency.

Each candidate's journal replay is limited to 10,000 events and 8 MiB. Reclamation
does not modify events, task heads, submission results, fitness, approvals or
configuration. The existing submission reconciler can separately repair an
interrupted parent before that parent's reader becomes eligible. Ordinary CLI
task execution does not start a persistent sweep by itself.

## Qualification

Real subprocess tests verify a held owner is not reclaimed, then SIGKILL and wait
for that exact child before allowing reclamation. Storage tests cover unsafe
histories, writers, legacy/missing guards, atomic rollback, concurrent attempts,
idempotency, corruption, migration and cursor progress. Guard tests reject forged
public state, stale/closed proof and identity damage after probe acquisition.

The application test kills the actual coordinator/worker service at two SQLite
boundaries. Before worker finalization, running work and readers remain unresolved.
After worker finalization but before the enclosing reader release, existing
submission recovery repairs the parent as failed/interrupted. Starting the real
dispatcher then reclaims only that terminal parent's orphan reader, records one
receipt and permits a new writer. Full source journals remain equal, repeated
recovery is non-mutating, and fixture model-call counts do not increase.

```sh
go test -race ./internal/processguard -run Proof -count=3
go test -race ./internal/telemetry -run TerminalReader -count=3
go test -race ./internal/app -run '^TestWorkerFinalizationProcessDeathRecoveryBoundary$' -count=3
```

## Limits and next requirements

The [execution-image guard](process-lifetime-ownership.md) attests only local
in-process lifetime, including exec replacement. It does not attest detached
children, remote generation or external effects. SDK handlers must honor the
cooperative in-process lease contract. Missing guard files after temporary-folder
cleanup, copied databases or reboot remain unknown; they are never recreated as
proof. Supported local-filesystem and retention limits still apply.

The separate [orphan-worker sweep](orphan-worker-recovery.md) can now fail an
interrupted supervisor with one already-terminal child and verified ownership.
This does not broaden terminal-reader reclamation authority.

Running-child recovery, missing child outcomes, writer/uncertain-effect
resolution, idempotent reassignment, automatic continuation, public holder and
receipt inspection, persistent operator attention and robust guard retention are
still outstanding. This checkpoint is not general orphan recovery or complete
PRD acceptance. Qualification uses synthetic loopback providers and local macOS
execution; Linux compilation is not Linux runtime or power-loss qualification.
