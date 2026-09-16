# Replaceable context planning

`sdk.ConfigOptions.ContextEngine` installs a trusted, process-local context
planner. It participates in actual automatic/explicit task execution and bounded
child delegation, not only diagnostics. The public `contextengine.Engine`
contract combines:

- `Assemble`: select/order complete context bundles.
- `Estimate`: measure the actual provider request, including model, tools and
  output schema, through the existing conservative admission wrapper.
- `PrepareCompaction`: choose a retention plan before canonical compaction.

`contextengine.Compact` materializes a selected plan through the host's canonical
compaction implementation. The engine does not author replacement history or
grant policy authority. This selection-based contract is intentional: changing
packing/retention strategy must not rewrite a user's request, invent tool results
or silently change an approved summary.

## Assembly and execution

An assembly contains four bundles: previous `History`, retrieved factual `Memory`,
procedural `Skills`, and `Current` input. Each bundle must be an independently
complete message sequence. History and current input are mandatory, exactly
once; current input must be last. An engine can omit either optional bundle or
change its position. It cannot select individual messages from a tool batch,
change roles, inject new messages, or modify bundle text through its returned
plan. Stable/project instructions already present in history or explicit current
messages remain part of those protected bundles; this adds no new project-prompt
configuration field or measured cache-hit guarantee.

The default order remains history → memory → skills → current. Nil retains the
default planner and built-in estimation; `ContextEstimator` alone remains
supported. Supplying both `ContextEngine` and `ContextEstimator` is rejected as
ambiguous. Typed-nil engines are rejected at SDK construction.

For each admitted attempt, the application freezes one owned assembly before
automatic ranking and reuses it for dispatch. It does not call a potentially
nondeterministic planner again after selecting a model. New retry attempts may
prepare fresh context. Iterative model turns and queued steering still use the
runtime's normal message/tool loop; assembly is not rerun to rewrite that history.
The engine's estimator checks each actual model turn and auxiliary audit/summary
request. Its estimate can raise but never lower the built-in serialized-byte
floor and output/framing reserve.

Protocol encoding and the journal's separate persistence-redaction boundary
still apply; this is not a universal byte-identical export guarantee. The custom
Codex continuation path avoids scrubbing an already-scrubbed assembly a second
time, since literal replacement need not be idempotent. Existing Codex admission
limits remain: fresh tasks require one user message, and compaction/reviewed-summary
imports are supported. Deferred mid-task replacement requires a durable
version-two plan plus a completed native turn; paused tool turns remain
ineligible.

Omitting memory removes its last-use update from that attempt. The engine cannot
weaken existing local-only privacy restrictions: a task pinned local during
retrieval remains local even if its optional bundle is later omitted. Context
limits, tool permissions, worker limits and resource reservations remain active.
Whole-bundle selection is not per-fact semantic ranking; the memory/skill stores
and existing retrieval policies determine the contents of those bundles.

## Compaction and approval

Explicit inline compaction lets the engine retain at least the requested number
of recent messages. It may retain more, but cannot change the supplied summary
or choose a no-op/invalid cut. Canonical compaction expands cuts to preserve tool
call/result pairs and retains original system messages. The source digest and
sequence always name the original durable history, not the sanitized callback
copy. Compaction does not delete the original session.

When an operator asks for a model-generated summary, retention is selected once
before the bounded auxiliary call. The resulting draft records that exact
selection and checkpoint. It must still be reviewed before use. Applying an
approved draft bypasses re-planning and validates the frozen checkpoint exactly;
replacing the engine cannot reinterpret the approved artifact. Changing the
summary or retention requires a new draft/review. These hooks do not enable
background inference or automatic summary approval. The stock history-first
engine and a described stable custom engine may activate an already approved
replacement at a later safe turn boundary. Custom-engine activation requires
its frozen identity and tier digests to match the durable plan; descriptor drift
fails closed. Stateful Codex execution additionally requires the provider
rollover capability and a completed native `stop` boundary before activation.

Completed `delegate` and `delegate_batch` calls may remain in a planned
parent's live suffix. The accepted worker start freezes a child-local prompt,
distinct child session, inherited context-engine identity, delegation scope,
and parent/child tool-policy snapshots. The child policy must be provably equal
or stricter; ambient parent history, memory, skills, tools, and permissions are
not imported. Activation independently reconstructs the exact parent tool
origin, work and execution journals, worker lease, completed model/tool
boundary, accepted result, and policy/engine digests. Failed or uncertain tool
effects, cancellation, owner or lease recovery, unreleased leases, policy or
engine drift, shared child sessions, and ambiguous historical delegations all
fail before activation. Exact retry and reopen validation rederive these facts
from the durable journals rather than trusting the normalized binding alone.

## SDK example

```go
type ProjectContext struct{ contextengine.Default }

func (ProjectContext) Assemble(ctx context.Context, in contextengine.Assembly) (contextengine.Plan, error) {
    if err := ctx.Err(); err != nil {
        return contextengine.Plan{}, err
    }
    // Keep skills close to the current task; omit the optional memory bundle.
    return contextengine.Plan{Version: 1, Order: []contextengine.Tier{
        contextengine.HistoryTier,
        contextengine.SkillsTier,
        contextengine.CurrentTier,
    }}, nil
}

client, err := sdk.New(sdk.ConfigOptions{
    ProjectFile:   "config.yaml",
    ContextEngine: ProjectContext{},
})
```

Import `context`, `github.com/ArronJablonowski/DarwinRouter/contextengine`, and
`sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"`. Embedding `Default`
provides conservative estimation and unchanged compaction selection. A custom
estimator may instead implement `Estimate` explicitly.

## Boundaries

Callbacks receive isolated, owned copies and a cooperative three-second deadline.
Raw byte/count/UTF-8 checks precede app redaction; the library also limits encoded
assembly/compaction input and output to 4 MiB. Invalid plans, incomplete tool pairs,
panics, callback failures and expired contexts fail admission without exposing
backend error text. Callback cancellation is not forced termination: trusted
engines must return promptly and support concurrent calls. The host does not
abandon goroutines for uncooperative extensions.

Custom application callbacks receive current-credential-scrubbed context, including
decoded structured tool output. The selected sanitized messages are used for
inference; the source journal is unchanged. Other personal data and arbitrary
encoded secrets are not universally removed. Pure `contextengine` library calls
do not know application credentials: embedding hosts must apply their own
redaction/privacy boundary.

Engines must operate locally without external context transmission. They are
trusted Go code, not a sandbox; the callback API cannot prevent an extension
from opening its own network connection. Engine implementations and state are
not serialized in submissions/configuration and must be supplied by the embedding
host after restart. The shipped CLI/daemon use the built-in planner; there is no
dynamic plugin loader or HTTP/YAML facility for installing arbitrary Go code.
