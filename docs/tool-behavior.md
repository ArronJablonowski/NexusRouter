# Declared tool behavior and observed effects

Tools now declare operation behavior separately from the result of a particular
execution. Neither an idempotency declaration nor a successful approval makes an
uncertain effect safe to repeat.

| Declaration | Trusted handler contract | Execution requirement |
| --- | --- | --- |
| `read_only` | Does not mutate the scoped external resource | Normal policy; `ask` still requires approval |
| `idempotent_write` | Repeating identical inputs is intended to produce the same external state, accounting for all side effects | Per-call approval and single-writer lease |
| `non_idempotent_write` | No repeatability guarantee | Per-call approval and single-writer lease |

These are trusted host declarations, not a proof of a handler's implementation.
Network calls, notifications and other secondary effects must be included when
deciding whether an operation is idempotent. Runtime bookkeeping, inference usage
and telemetry are not promises of zero cost merely because a tool is read-only.
In-process handlers remain cooperative trusted code, not sandboxed code.

## Registration and compatibility

`tools.Definition.Behavior` uses `runtime.ToolBehavior`; the same type and constants
are available through `tools` and `sdk/v1`. An omitted declaration defaults
conservatively from the existing `ReadOnly` flag: true becomes `read_only`, false
becomes `non_idempotent_write`. No tool becomes idempotent by default.

An explicit declaration must match `ReadOnly`: explicit read-only tools must also
set `ReadOnly: true`; both write classes must leave it false. Unknown values and
contradictions reject registration. The compiled registry and extensions retain
immutable normalized declarations. `Registry.Descriptions()` and
`Extension.Descriptions()` return sorted, owned name/scope/behavior metadata,
without handlers, raw arguments or schema content. Provider tool schemas and
wire payloads do not gain a model-authored behavior field.

The built-in `read_file`, `delegate` and `delegate_batch` declare read-only
behavior within their existing restricted capability scopes. `create_file`
conservatively declares non-idempotent write behavior; its no-overwrite and
per-call review rules are unchanged. There is no new built-in arbitrary writer.
SDK hosts may register an explicitly idempotent implementation through the
existing reviewed tool-extension interface.

## Durable binding

The runtime obtains the declaration from an optional trusted
`ToolBehaviorProvider` before committing `tool.started`. The same snapshot is
attached to `tool.completed`; a declaration error or panic stops before dispatch.
The registry-backed executor supplies normalized metadata automatically. Legacy
custom executors without this optional interface remain unclassified, never
implicitly read-only or idempotent.

`tool_behavior` is an optional validated field in version-1 events and approval
requests. Historical absent fields stay absent and match only absent fields;
old journals are not relabeled. New paired events must match exactly during
replay, including interrupted-delegation recovery. Approval request, decision,
consumption and execution inspection bind the declared class to the actual
outstanding or completed tool journal. Dropping or changing a typed binding does
not recover old authority. The policy digest also includes the declaration in a
versioned policy/behavior envelope. This is an additive JSON-record extension,
not a new SQL column or a rewrite of existing records.
Do not use older binaries to continue newly typed histories: mixed-version
execution against the same database is not qualified by this additive format.

Strict approval command parsing accepts the optional field only under its exact
name with a valid nonempty value; null, aliases, duplicates and unknown values
are rejected. Existing CLI/API/SDK approval inspection exposes it in the request.
Terminal `create_file` review displays the known non-idempotent declaration and
rejects a contradictory class. Audit loading retains the classification as
metadata, without elevating it into validation evidence or copying raw tool data.

## Effects, failure and retry

Each execution still reports `none`, `confirmed` or `uncertain` as its observed
effect, independently of `Failed` and `Recoverable`. A declaration cannot erase
a confirmed effect, convert uncertainty to no effect, bypass inherited denials,
reuse consumed approval or remove writer ownership requirements. The runtime
rejects a read-only declaration paired with a confirmed effect while retaining
that observed effect as evidence. Registry execution treats a violating handler
conservatively as uncertain.

Neither write class enables automatic replay or provider fallback after tool
effects. The existing explicitly recoverable NoEffect path only permits a fresh
model turn and a separately admitted proposal. See
[recoverable failures](recoverable-tool-failures.md). Read-only declarations do
not make generic errors/panics successful. Idempotency here is also distinct from
submission deduplication: a repeated submission ID is not a tool retry permit.

Tests cover normalized defaults, immutable metadata, exact approval/lease binding,
read-only contradictions, replay drift, recovery and audit propagation. Real
loopback-provider SDK tests execute one controlled file effect per approved call
and prove that uncertainty or a later provider failure does not trigger fallback.
Fixtures do not prove arbitrary user handlers are truly idempotent, and this
checkpoint does not add automatic retries, stronger process isolation or full
PRD qualification.
