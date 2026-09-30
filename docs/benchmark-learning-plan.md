# Benchmark learning and capability plan

## Evidence and interpretation

The September 24 review found two different gaps: execution reliability and
successful execution that still produces an incorrect or incomplete answer.
Increasing a turn budget or changing storage cannot by itself fix answer quality.

On the 11 models shared by the original all-local Standard campaign and the
historical Mac Studio exports (17 tasks each), NexusRouter passed 92/187 with
64 execution errors; Hermes passed 130/187 with zero execution errors, direct
Ollama 122/187 with 15 errors, and OpenClaw 117/187 with six errors. These are
historical observations, not controlled simultaneous A/B measurements.

After the earlier reliability fixes, regrading with behavioral-v1.1 yielded
13/17 for NexusRouter with Qwen3 Coder (including delegation to Gemma 12B),
versus Pi 10/17 and COH 11/17; Gemma 31B yielded 14/17 with all three. The latest
five-task Gemma retest passed only 1/5. The latest isolated Muse coding retest
finished 74 turns and passed 4/6 checks: it omitted a required public export and
its test discovery ran zero tests. These failures justify completion validation,
not more blind retries or a claim of general superiority.

The harnesses differ materially. Pi's Standard adapter disables tools; its
coding tools provide targeted multi-edit operations and concise guidance.
Hermes' coding adapter allows 150 turns, a 64K window, and terminal/file/code
execution tools; the earlier NexusRouter adapter allowed 32 turns. Goose's adapter
allows 100 turns. Current COH source binds execution profiles, task contracts,
output conformance and artifact requirements. Current installed source does not
prove which behavior caused a historical score. Same model names alone do not
control model digest, sampling, context, tool budget, latency, or delegation.

## Implemented first stage

* Explicit `routing.evidence_fallbacks` maps an unmeasured target domain/profile
  to one source domain/profile for the same model/provider. Source observations
  are replayed with their original timestamps and revision history; direct
  target feedback or target advisory evidence takes precedence. No mappings
  are enabled by default, no mappings are chained, and no evidence is rewritten.
  Only direct source verdicts transfer; source judge opinions do not. Confidence
  is capped at 0.25 and the stored route includes SourceDomain/SourceProfile.
* Context quality and latency are selected within the task domain/profile.
  Hard faults and resource maxima remain model/provider-wide safety signals.
  A fault at 128K allows healthy 64K; no safe tier returns zero. Sparse larger-tier
  quality observations cannot promote the window before minimum_samples.
* Optional context expansion checks fresh memory/budget availability. Actual
  local reservations scale RAM/VRAM above the configured working tier. Lacking
  a measured KV estimate, the whole baseline reservation is scaled linearly,
  conservatively over-reserving weights. The baseline still reaches normal
  residency admission so an old resident model can be unloaded. Windows are
  never reduced below the task estimate by silently truncating its input.
* New feedback records measure task-start to final-turn completion, including
  earlier turns and tools. Existing immutable latency values are retained on
  retries; corrections do not create extra samples. This does not include work
  done in earlier continuation tasks or separate fallback tasks.
* Standalone feedback commands have a configurable bounded timeout (default
  two minutes, maximum ten minutes) covering cold database validation. A timeout
  is distinguished from rejected feedback. The benchmark adapter's parent
  process must allow this deadline before terminating the command.

Example operator configuration (choose mappings for the actual workload):

```yaml
routing:
  evidence_fallbacks:
    - domain: code
      profile: default
      source_domain: coding
      source_profile: benchmark
    - domain: math
      profile: default
      source_domain: math_reasoning
      source_profile: benchmark
  weights:
    quality: 0.65
    schema_compliance: 0.10
    reliability: 0.15
    latency: 0.05
    cost: 0.03
    recency: 0.01
    uncertainty: 0.01
```

The live test deployment uses these accuracy-priority weights and 13 explicit
mappings. Defaults remain balanced for other deployments. Benchmark priors are
not evidence that short coding answers generalize to large repository tasks.

## Next stages and acceptance gates

1. **Reliable completion checks.** Define host-supplied public task contracts:
   output schema, required exports/artifacts, build command, and test command.
   Require successful test discovery with nonzero tests when tests are required.
   Offer at most two correction turns using public validation diagnostics, within
   the original token/time budget. Keep hidden grading answers unavailable.
   Validate that deliberate incomplete artifacts cannot receive success feedback,
   and successful repair beats the unchanged harness on held-out fixtures.
2. **Portable agent tools.** Add precise edits/patches, bounded search and reads,
   structured exit codes, output truncation, and bounded recoverable diagnostics
   to a supported NexusRouter coding host. Keep workspace isolation and native
   tool authority. Compare edit errors, tool calls, tokens and time on the same
   fixed models; retain changes only when quality is preserved or improved.
3. **Complete outcome telemetry.** Record a separate typed execution outcome for
   every admitted attempt, including timeouts/provider/context errors, allocation,
   token usage, peak memory and swap delta. Capacity/transport failures remain
   operational evidence, not rejected model-quality feedback. Add cooldown and
   bounded re-probing for faulted tiers, keyed by model digest/provider/config.
   Require one replay-safe outcome per attempt, including interrupted attempts;
   compare measured telemetry against independent host sampling.
4. **Task-aware context experiments.** Use model-specific token estimates plus
   tool schemas and output reserve, then test 32K/64K/128K/advertised ceilings
   only with memory headroom. Compare retrieval/coding accuracy at matched input
   lengths rather than pooling easy short tasks with long difficult tasks. Prefer
   the smallest reliable window, bound re-exploration, and prove backoff after
   timeout or swap growth beyond 5 GiB. Add actual KV-cache measurements to replace
   the conservative full-memory scaling when qualified.
5. **More precise model selection.** Separate short code generation from multi-
   file edits, debugging, tool execution, JSON, retrieval and reasoning. Add
   language, complexity, input-length and required-tool features with explicit
   evidence provenance and confidence. Use hierarchies as bounded priors; let
   exact task feedback supersede them. Measure accuracy, calibration, latency
   and regret against the best fixed model on held-out tasks.
6. **Validated procedural learning.** Extract candidate procedures from successful
   traces and recurring failures. Activate only after independent regression and
   hold-out checks; version and roll back regressions. Do not turn model confidence,
   an empty successful command, or an admission error into a positive/negative
   quality label. Preserve failure steps even when later repair succeeds.
7. **Controlled tournament.** Freeze model digests, sampler/seed, grader, hardware,
   tools, memory ceiling and equal wall/token/turn budgets. Test both isolated
   single-model operation and separately labelled delegated routing. Run at least
   three repetitions per task/model; keep a training set and an untouched hold-out
   set, freezing learning during evaluation. Report task pass rate, check pass
   rate, execution-error rate, total and p95 latency, token use and confidence
   intervals. A release must improve held-out performance without task-family
   regressions; universal superiority cannot be inferred from 17 Standard tasks
   or nine coding fixtures.

## Current limits

Historical evaluations in the live database mostly lack resource attribution;
all 284 context-attributed records examined before this change were at 32K and
had zero resource/fault measurements. This stage does not reconstruct missing
peaks or prove that 64K/128K are reliable. Fault handling is conservative and has
no automatic expiry yet. Source-profile mappings supply model priors, not large-
window qualification. Routing-map inventory shows a safe baseline without a task;
actual task routing selects a domain-specific tier. Procedural skill learning and
all later stages above remain unqualified work, not completed capabilities.
