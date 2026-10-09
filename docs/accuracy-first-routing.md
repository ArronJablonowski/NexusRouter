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

## Candidate-pool boundary and remaining work

This change makes the current automatic selection paths accuracy-first. It does
not introduce a single global candidate pool for all application requests. The
ordinary application router considers configured provider/models; paired-remote
automatic dispatch discovers and ranks permitted models on paired destinations.
A browser chat using a configured commander is still an explicit default-model
request. It does not automatically discover and switch to another host.

Meeting the full product requirement of comparing local and all paired-remote
models for every unpinned request still requires unified candidate discovery and
execution integration. That work must preserve tool/filesystem authority,
conversation continuity, destination-scoped evidence, durable request ownership,
cancellation, stream behavior and restart-safe routing. The current scoped ranker
can compare candidates from multiple locations, but its read-only selection is
not a replacement for those execution guarantees. Do not describe this scorer
change as completed global routing or deploy it as proof of that capability.
