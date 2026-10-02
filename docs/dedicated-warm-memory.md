# Dedicated-provider warm memory admission

Warm admission is an opt-in, conservative **incremental memory estimate** for
an already loaded model. It does not add provider-reported bytes to available
RAM, turn storage size into reusable memory, or treat Spark GPU memory as a
second independent pool. The default remains the full cold-load estimate.

## Supported boundary

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
