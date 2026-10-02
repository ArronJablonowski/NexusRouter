# Dedicated-provider warm memory admission

Warm admission is an opt-in, conservative **incremental memory estimate** for
an already loaded model. It does not add provider-reported bytes to available
RAM, turn storage size into reusable memory, or treat Spark GPU memory as a
second independent pool. The default remains the full cold-load estimate.

## Concurrent models and the Spark reserve

NexusRouter can admit multiple models concurrently when their **combined full
cold estimates**, existing host usage, and the safety reserve fit. Set
`hardware.concurrent` to `auto` (adaptive admission) or an explicit ceiling such
as `2`, and set `workers.max` to at least that many workers. Estimates must include
weights, KV/context allocations, runtime buffers, and output workload. A provider
also needs its own concurrency and loaded-model limits configured accordingly;
a router slot alone does not make a serial provider run in parallel.

On a detected DGX Spark, all RAM admission and capacity planning preserve an
**8 GiB (8,589,934,592 bytes)** platform reserve. This conservatively exceeds
8 decimal GB. The stricter of this reserve and `hardware.max_ram_usage_pct` applies.
Every active reservation across the host coordinator is charged against the
same pool, including different models and daemons. CPU and GPU RAM are never
added together on the Spark. A container smaller than the reserve admits no new
model work. Pressure, stale measurements, and concurrency limits still deny work.

Parallel configurations may retain qualified warm settings, but use full cold
estimates. The warm optimization below remains serial; changing concurrency does
not silently grant unsafe residency credit. Dedicated warm providers still have
one configured model each. A dedicated provider used solely with cold accounting
can have several configured models and `OLLAMA_MAX_LOADED_MODELS` greater than one.

The reserve is enforced at admission using measured available RAM, including
Linux reclaimable memory. It is not a promise that unrelated processes or an
underestimated provider allocation cannot consume RAM later. No unrelated model
is unloaded, no process is killed, and percentage limits are never raised to make
a task fit. Upgrade every daemon sharing the Spark coordinator before relying
on the new floor; older binaries do not implement it. Qualify estimates and monitor the host before production deployment.

## Supported warm boundary

The initial implementation is limited to coordinated native execution on a
unified-memory host, with one configured model on a dedicated loopback Ollama
provider, one worker, and fixed concurrency of one. The operator must exclusively
reserve that provider for NexusRouter. Do not enable this setting on a shared
Ollama service. Run the dedicated instance with `OLLAMA_NUM_PARALLEL=1`,
`OLLAMA_MAX_LOADED_MODELS=1`, and `OLLAMA_KEEP_ALIVE=-1`. Its model directory may
reuse existing installed files; do not expose its listener outside loopback.
Other clients must not load, unload, replace, or reconfigure its model.

A qualified deployment supplies:

- Provider `dedicated_warm_memory: true`, with `manage_residency: false`.
- Model `ram_bytes`: the full cold-load requirement at its working context.
- Model `warm_ram_bytes`: a separately qualified conservative incremental
  requirement, including inference buffers and context/runtime headroom. It
  must be at least 1 GiB and half the cold estimate, and less than the cold
  estimate. The validation floor is not a measurement or an automatic estimator.
- Model `residency_digest`: the exact installed Ollama model digest.

Before selecting the warm estimate, NexusRouter performs bounded read-only
inventory and residency queries. Installed and resident identities must match
the configured digest and normalized model name. Exactly one resident must be
present, its context must equal the requested working tier, and its reported
footprint must not exceed the cold estimate. Expiry must be more than 24 hours
away; the normal short-lived Ollama cache is insufficient. The observation must
be no more than one second old when fresh host capacity is checked.
The durable coordinator then repeats the observation under its cross-process
admission transaction, requiring no other active reservation. Expired but
unreleased reservations also block this warm verification until release or
process-proof recovery. A concurrent daemon cannot change the admitted model
between that final check and committing the reservation. Observations never
grant permission to unload models; external users of the dedicated provider
remain outside the supported ownership boundary.

Missing, stale, malformed, changed, or unavailable facts select the full cold
estimate. Context expansion, native harness overhead, custom provider factories,
discrete-memory hosts, and uncoordinated execution do not receive the warm
estimate. Automatic candidate capacity previews remain conservative cold-load
previews; this change does not promise automatic selection of a warm-only fit.

The normal measured RAM percentage, pressure checks, local concurrency gate,
and durable cross-daemon coordinator remain authoritative. Every active request
is charged its complete selected estimate; no resident-byte subtraction occurs.
Warm receipts bind the original cold estimate and pinned residency digest in
addition to the charged RAM, context, configuration digest, and task identity.
There is no new unload or inference retry authority. Do not mix older binaries
that cannot interpret these warm receipts into the same coordinator database.
Remote servers now install the same coordinator as the main daemon before
starting their dispatcher. Daemons sharing a coordinator must agree on its
resource policy; incompatible policies fail startup rather than creating
independent allowances. Do not isolate production coordinator directories to
bypass this contention.

These remain estimates, not a hard guarantee of provider allocation. Qualification
must cover the deployed model, context, output limit, provider version, and
workload; a single arithmetic result does not qualify arbitrary prompts or
context growth. On the tested Spark, `memory.current` accounted for only about
1.3 GB while Ollama reported about 6.3 GB resident. A Linux cgroup `MemoryMax`
therefore must not be presented as enforcement of the whole unified GPU pool.

## Qualification

`TestWarmMemoryUsesQualifiedBudgetAndPreservesAdmission` covers warm selection,
cold fallbacks, identity/context/expiry drift, oversized resident reports,
pressure, harness overhead, concurrency, and idempotent release.
`TestWarmChargesRemainDurableAndContendAcrossStores` verifies persisted identity,
replay conflicts, aggregate incremental RAM contention, and release through two
independent coordinator handles.

The existing real-model two-host test accepts these additional variables only
for an explicitly prepared dedicated provider:

| Variable | Meaning |
| --- | --- |
| `NEXUS_REMOTE_TEST_PROVIDER_ENDPOINT` | Dedicated Spark loopback Ollama endpoint. |
| `NEXUS_REMOTE_TEST_WARM_RAM` | Qualified incremental bytes. |
| `NEXUS_REMOTE_TEST_MODEL_DIGEST` | Exact installed and resident digest. |

Start with an empty dedicated resident list. The fixture audits one cold and
three warm durable reservations independently through read-only SQLite after
HTTPS/SSH arithmetic and streamed cancellation. It does not alter the existing
shared Ollama service or install a persistent production deployment.

The 2026-10-02 Spark check used the pinned Qwen3-Coder-Next model at 8192
context, a 64 GiB cold estimate and a 32 GiB warm estimate. HTTPS and SSH
arithmetic, caller replay, payload-conflict rejection and cancellation after
streamed output passed in 15.918 seconds with the final transaction fence. The independent receipt audit found
one cold and three warm charges. The temporary dedicated provider was stopped
after qualification; the existing shared Ollama service was unchanged. This is
a measured development checkpoint, not exhaustive workload or release approval.


Parallel cold-accounting qualification on the Spark used Qwen3 8B and
Qwen3-Coder-Next with 16 GiB and 64 GiB estimates. Both completed over the secure
remote HTTPS route in 61.04 seconds; a single observation confirmed two active
reservations and both residents. Across 101 samples the minimum available RAM
was 62,667,771,904 bytes. This verifies the tested two-model workload, not arbitrary
model combinations. The temporary dedicated service allowed two loaded models,
kept provider parallelism at one request per model, and was stopped afterward.
