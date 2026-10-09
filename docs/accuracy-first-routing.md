# Accuracy-first automatic routing

Automatic model requests use the highest evidence-supported task accuracy among
eligible candidates. Lower price, faster responses and execution location cannot
compensate for a lower accuracy score. Explicit model choices, including an
operator-configured commander default, remain pinned under the existing explicit
fallback rules.

The model score retains confidence and recency shrinkage, domain/profile
isolation, bounded advisory influence and independent objective-invalidity
penalties. Missing evidence remains a neutral prior, not a claim that a larger or
newer model is stronger. Stable ties are deterministic. The ordinary application
path never requests exploratory model selection; evaluation callers may explicitly
set `routing.Request.AllowExploration`. The configured exploration fraction alone
does not authorize a weaker model for an ordinary task.

The policy stored in new route explanations sets `AccuracyFirst: true`. Historical
policies lacking that flag retain their original weighted-scoring representation
and remain readable. Legacy weights and scales still validate for historical
compatibility; they do not override the new default accuracy score.

Mode, privacy, credentials, capability, context, health, cost and capacity checks
remain authoritative. A paired remote system running local inference is distinct
from cloud inference; its peer permissions and destination admission still apply.
Remote model/harness selection already ranks correctness, then quality and
confidence, using caller-owned evidence separated by execution identity and host.

## Unified model selection and Specialist Cards

The commander daemon, `nexus run`, and terminal `nexus chat` install a unified
model-selection bridge when paired-client credentials, trust, dispatch storage,
and automatic evidence storage are configured. Ordinary unpinned model requests
compare eligible configured providers with permitted paired destinations using
the same accuracy scorer. Explicit commander/model defaults remain pinned.

Upgraded destinations advertise text-conversation protocol version 1. Supported
local Ollama/OpenAI-compatible models expose a built-in `nexus-direct` route;
no external harness installation is required. The existing paired model dispatch
scope permits this equivalent text-only route, not arbitrary external harnesses.
The receiver checks configured identity, model presence, credentials and
context-scaled memory capacity, then performs normal execution admission. An
optional configured weight digest contributes to the identity; without one,
the revision identifies configuration, not cryptographically attested weights.
Explicitly permitted external harnesses may also participate when they do not
expose native tools. Their deployment identities remain isolated from direct
provider routes.

Commander-owned destination/caller/model/harness evidence supplies remote votes;
a host's advertised routing score never supplies accuracy. Current full-confidence
human/deterministic verdicts contribute confirmed pass/fail observations. AI and
lower-confidence reviews remain bounded advisory observations. Unreviewed,
withdrawn, mismatched-profile or different-deployment evidence cannot establish
accuracy. Direct feedback on a unified commander task also contributes to its
exact destination identity. Credential rotation changes that identity rather
than borrowing another caller's evidence.

The Command grid reads `/app/api/v1/routing-grid`. Each Specialist Card preserves
backend rank order and shows up to three distinct model deployments. Remote rows
show the model and hostname (or a clearly labeled instance fallback) in yellow.
Expandable details include the host, paired instance, original remote model ID,
evidence domain/profile, score, confidence and sample count. Multiple harnesses
for the same destination model do not consume multiple positions.

This is a zero-cost policy preview, not an actual task reservation or an override
of a manual commander selection. Remote previews require at least 8K context;
benchmark profiles require 32K and measured profile evidence. Actual task context,
budgets and capability requirements can change eligibility. Admission metadata
is cached for at most ten seconds; trust changes invalidate it, and execution
rechecks current permissions, identity, readiness and capacity after its durable
start. The preview never consumes an exploration draw or runs inference.

## Failure and retry ownership

A unified automatic request freezes its original top three distinct model
deployments. A confirmed remote failure before any delivered answer may advance
to the next-ranked deployment, including another model on the same host. Each
attempt has a new task identity, durable retry lineage and remaining budget;
operator attempt/time limits can further restrict the chain. The fourth model is
never used, and changing rankings do not expand the saved pool.

A remote route binding is synced before submission. Status and cancellation use
the original destination and caller certificate. Lost delivery is not proof of
failure: the client requests cancellation and reconciles a running cancellation
for up to five seconds. Only a confirmed terminal failure/cancellation permits
failover. Unresolved ownership, caller cancellation, persistence/sink failures
and delivered output stop the chain. The same remote request identity is never
rebound or redispatched by this provider. Remote output is delivered when the
remote durable result completes; incremental remote token forwarding is not
implemented. On commander restart, existing interrupted-task recovery stops the
parent rather than automatically resubmitting it. The original remote binding
remains inspectable, and the remote execution deadline bounds orphaned work;
automatic restart reconciliation/cancellation remains a gap.

## Eligibility and coverage limits

Remote execution does not inherit the commander's filesystem, workboard tools,
recursive delegation or tool-result history. Tasks using those capabilities,
custom context estimators, approved compaction, host-owned workboard execution,
or unsupported conversation shapes retain configured-provider routing. Built-in
direct routing currently supports local inference; remote cloud inference needs
an explicitly qualified external harness. Unknown or denied capacity is not
replaced by an available-RAM guess.

The specialized legacy native-harness and recorded remote-automatic APIs retain
their existing selection/receipt contracts. Generic Go SDK embedders do not
implicitly install an outbound remote bridge. These boundaries are not a claim
of universal routing for every API or completed remote tool portability. Older
peers lacking conversation version 1 remain ineligible for unified chat routing
until upgraded. Physical-host/model qualification, native SSH evidence and
complete release qualification remain separate from isolated fixture results.
