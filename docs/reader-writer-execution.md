# Reader/writer execution

The application uses durable shared reader leases for every allowed read-only
tool, including SDK extensions and delegated file reads. Schema validation,
policy and durable pending-call identity checks precede handler dispatch.
Read leases do not require operator approval. Tools with `ask` policy still
use the existing conservative exclusive approval gate, even when read-only.
Both write behavior classes require approval and a writer lease.

Readers in the same scope can run concurrently. Any unreleased writer excludes
other readers and writers; any unreleased reader excludes writers. Admission
currently rejects a busy scope rather than queuing or replaying the tool.
Independent non-filesystem scopes remain independent.

## Cancellation and ownership

Reader leases renew every ten seconds with a thirty-second lifetime. The gate
joins the callback on cancellation, verifies ownership before accepting its
result, and releases only after the callback returns. Lost ownership, callback
panic/error, invalid effect or malformed result fails closed and is not a safe
automatic-retry signal. A recoverable read failure is allowed only when the
callback returns an explicitly failed, effect-free result without a Go error.

One cancellation case retains diagnostics, not accepted output: after a joined
callback reports a valid failed/no-effect result, the gate may preserve that
exact result only when the caller is canceled and final ownership is proven.
It clears `Recoverable` and returns cancellation. Success after cancellation,
expired ownership, storage failures and ambiguous effects are not admitted by
this exception. This preserves bounded canceled-delegation evidence without
granting another turn or retry authority.

Expiry is not proof of termination. Expired, unreleased readers now continue
to exclude writers, just as expired writers already did. A paused callback
cannot be replaced merely because its heartbeat stopped. This favors safety
over availability: a crashed holder can leave an unresolved lease. General
operator reconciliation with proof of termination remains unfinished; never
release a lease solely because its timer expired.

## Built-in filesystem scope and upgrades

`read_file` and `create_file` both use the reserved `workspace` scope within
the shared database. This deliberately excludes writers across all configured
file roots, including nested, aliased and unrelated roots. Root-specific
device/inode hashes cannot prove non-overlap. Parallel reads are still allowed;
parallel writes to unrelated builtin roots are conservatively serialized.

For admission and trusted lease inspection, `workspace` and all members of the
legacy `create_*` scope namespace mutually overlap. Old unreleased leases
remain blockers without rewriting stored scope identities or approval records.
Approval validation and consumption retain their exact original scope binding.
Approval execution's `ScopeWriterState` is still an exact-scope observation;
`none` there is not proof of availability against all legacy overlap blockers.
The additive `ScopeLeases`/`scope_leases` observation provides live/expired
reader/writer counts across the complete overlap family in the same snapshot.
It is available through CLI `approvals execution`, SDK `ApprovalExecutionStatus`
and the authenticated task-approval execution endpoint. Nil/missing means a
legacy or unavailable observation, not zero. New storage-backed responses
always supply version 1 and overlap-policy version 1, bound the total to 1,000
holders, and reject malformed/overflowing data without a partial status.
No holder identities, owners or tokens are included. Multiple distinct legacy
writer scopes can legitimately coexist from before upgrade; they are counted,
not treated as authorization. Duplicate writers in one exact stored scope are
rejected as inconsistent. Expiry still does not prove termination, and even an
empty observation cannot guarantee availability after the snapshot is taken.
Do not run older binaries concurrently with this version: older admission code
does not implement the new overlap/expired-reader rules.

## Boundary and verification

Coordination requires cooperating executions using the same database. It is
not an OS sandbox and cannot fence external programs, separate databases, or
trusted handlers that spawn unjoined work or misdeclare effects. SDK hosts must
assign the same scope to overlapping resources; filesystem extensions should
use `workspace`. A standalone `tools.Executor` with a nil `Reader` retains its
legacy cooperative path; application/SDK execution always installs the gate.

Tests cover actual independent SDK clients with HTTP provider fixtures and
shared SQLite: simultaneous readers, same-scope writer denial, different-scope
independence, canceled callback joining and eventual writer admission. Built-in
application tests cover same/nested/aliased/unrelated root exclusion. Storage
and gate tests cover expired readers, legacy scope compatibility, ownership
loss, stale output rejection and callback one-use guards. These tests do not
prove OS containment or completed crash reconciliation.
