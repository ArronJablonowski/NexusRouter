# Routing performance evidence

## DAR-44 local qualification

Run the complete non-live matrix without concurrent project tests or builds:

```sh
make qualify-performance
```

The gate also runs one content-free lifecycle snapshot against a generated
100,000-task/200,000-event SQLite store three times. This covers task-duration,
canonical-event and derived-operation queries together and checks their counts,
not just elapsed time. Each iteration includes a fresh database setup outside
the timed region; the reported `ns/op` measures the read-only snapshot itself.

On September 7, 2026, Apple M4 Max / darwin arm64 / Go 1.27.1, three runs
measured the 64-model evidence selector at 16.00–16.52 microseconds per route.
The eight-model automatic fixture with 1,000 seed tasks measured 54.16–54.36 ms
mean, 58.79–59.48 ms p95, 60.10–61.13 ms p99, and 60.68–61.66 ms maximum over
100 tasks per run. This fixture includes admission, evidence reads, routing,
SQLite writes and loopback protocol overhead; provider inference is immediate.
Both the isolated selector and the broader deterministic fixture are below the
PRD's 150 ms routing-overhead target on this qualified host.

Individual SQLite/WAL `FULL`-synchronous task-start transactions measured
177.6–192.5 microseconds (approximately 5,194–5,631 commits/second), with one
writer and no contention. Fixed/adaptive in-memory reservation plus release
measured 0.277–0.469 microseconds. Race-enabled low/mid/high tier, RAM/VRAM,
thermal/unknown-pressure, queue/reject/offload, shared explicit/automatic
capacity and concurrent admission tests separately passed three repetitions.

NexusRouter does not currently enable an auxiliary intent classifier, so the
conditional 500 ms auxiliary-classification target has no executable MVP path
to measure. Adding one requires its own benchmark before activation. These local
figures are reproducible evidence for the current deterministic runtime, not a
host-independent SLA, production inference benchmark, contended-write result or
power-loss qualification.

## Schema28 indexed evidence reads

The task/kind/sequence index described in [migration notes](event-kind-index.md)
accelerates repeated per-task evidence queries without changing eligibility or
validation. The same six application fixtures were rerun with100 serial tasks
and three independent runs on the same host. These recorded runs did not overlap
other project tests/benchmarks; other host workloads remained uncontrolled.
An exploratory overlapping run was discarded.

| Pool | Seed → final tasks per run | Mean ms | p50 ms | p95 ms | p99 ms | Largest observed ms |
| --- | --- | --- | --- | --- | --- | --- |
| 2 | 0 → 101 | 7.15–7.36 | 6.83–7.26 | 10.34–10.45 | 10.67–10.88 | 10.96 |
| 2 | 100 → 201 | 11.61–12.10 | 11.74–12.30 | 13.83–14.39 | 14.02–14.61 | 15.11 |
| 2 | 1,000 → 1,101 | 22.66–22.80 | 22.45–22.82 | 24.25–24.92 | 24.79–25.40 | 27.73 |
| 8 | 0 → 101 | 9.56–9.77 | 9.61–9.93 | 14.21–14.41 | 14.53–14.88 | 15.21 |
| 8 | 100 → 201 | 15.59–15.76 | 15.21–15.47 | 19.79–19.96 | 20.14–20.49 | 20.67 |
| 8 | 1,000 → 1,101 | 49.76–49.86 | 49.24–49.41 | 53.20–55.59 | 55.48–56.60 | 57.72 |

Values are ranges of each per-run metric, not pooled percentiles. The eight-model,
1,000-seed mean is approximately56% lower than the schema27 fixture below.
New timed tasks still incur normal index-maintenance writes, but these measurements
do not separately qualify write throughput, index disk size, large migrations or
concurrent requests. All original measurement limitations below still apply.

A CPU profile of the schema27 fixture (13.11s profile duration,12.02s sampled CPU)
attributed8.95s/74.46% cumulative CPU to `OutputValidity`; it includes setup/seeding
as well as measured tasks and is not a latency decomposition. Reproduce on the
reviewed source revision with a caller-owned output directory:

```sh
go test ./internal/app -run '^$' -bench '^BenchmarkAutomaticTaskOverhead/pool_8/seed_1000$' -benchtime=100x -count=1 -cpuprofile /absolute/owned/cpu.pprof -o /absolute/owned/app.test
go tool pprof -top -cum /absolute/owned/app.test /absolute/owned/cpu.pprof
```

An attempted combined-count JSON query measured115.17–115.71ms and increased
allocations; it was discarded before this checkpoint. The accepted change adds
the index and bounds the planning population count at201; it does not replace
fresh evidence validation with a cache or drop malformed observations.

## Schema27 latency distributions

Measured September 6, 2026 on Apple M4 Max, darwin/arm64, Go 1.27.1,
with 100 serial completed tasks per case and three separate runs. No race
instrumentation or concurrent project benchmark/test suite was running for these
recorded measurements. Other host workloads, CPU scheduling and thermal state
were not controlled. An earlier overlapping exploratory run was discarded.

```sh
go test ./internal/app -run '^TestTaskLatencyNearestRank$' -bench '^BenchmarkAutomaticTaskOverhead$' -benchtime=100x -count=3
```

The benchmark now reports empirical nearest-rank `p50-ms`, `p95-ms`, `p99-ms`,
`max-ms`, and `samples` in addition to Go's mean and allocations. Every successful
timed task contributes one sample; sample allocation and sorting occur outside
the timer. Clock reads and result checks add small measurement overhead. At 100
samples p99 is the second-largest observation, not a confidence bound or a
reliable estimate of rare production tails. The following ranges retain the
minimum and maximum of each metric across the three runs; they are **not pooled
percentiles or averages of percentiles**.

| Pool | Seed → final tasks per run | Mean ms | p50 ms | p95 ms | p99 ms | Largest observed ms |
| --- | --- | --- | --- | --- | --- | --- |
| 2 | 0 → 101 | 11.71–12.04 | 11.49–12.12 | 18.72–19.53 | 19.41–20.28 | 21.25 |
| 2 | 100 → 201 | 22.35–22.63 | 23.04–23.86 | 26.99–27.42 | 27.70–27.96 | 28.33 |
| 2 | 1,000 → 1,101 | 36.76–37.18 | 36.54–37.16 | 39.46–39.94 | 39.95–41.36 | 42.09 |
| 8 | 0 → 101 | 16.70–17.32 | 16.28–17.12 | 27.33–28.36 | 28.33–35.82 | 43.21 |
| 8 | 100 → 201 | 29.14–29.24 | 29.07–30.31 | 38.90–40.04 | 39.67–40.84 | 41.80 |
| 8 | 1,000 → 1,101 | 111.97–113.83 | 112.4–113.2 | 118.1–123.0 | 122.1–131.7 | 141.4 |

These are whole `Service.Run` tasks, including routing, persistence, loopback
provider execution and completion—not isolated deterministic routing latency.
The corpus grows during each run, so the distribution spans changing history
sizes. Discovery is initially warmed; its normal five-second TTL may expire
during longer cases. Refreshes are not suppressed or separately counted.
Fixtures use deterministic nonempty checks, not representative human feedback,
audits, code outputs or broad domain/profile distributions. There is one provider,
no request concurrency, mocked resource sensors and no real inference. These
measurements do not establish either PRD latency SLA or performance equivalence
with the older three-operation results below.

## Isolated routing core

```sh
go test ./routing -run '^$' -bench '^BenchmarkSelect' -benchmem -count=3
```

Separate non-overlapping runs on the same host measured only `routing.Select`.
Fixtures build real domain-keyed quality, compliance, reliability, latency, cost,
recency, advisory and validity evidence. Before timing they require a successful
primary, expected eligible/excluded/fallback counts, the intended exploration
state and a first fallback in a different failure domain. This prevents an
accidental all-rejected fast path from appearing as successful routing.

| Case | Mean ns/op across runs | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| 2 eligible models | 368.1–370.0 | 656 | 7 |
| 8 eligible models | 1,429–1,434 | 3,824 | 12 |
| 64 eligible models | 15,204–15,314 | 42,808 | 27 |
| 64 candidates, 8 eligible and 56 constraint exclusions | 7,259–7,526 | 23,416 | 94 |
| 8 models, cold-start exploration | 1,199–1,235 | 3,824 | 12 |

The constrained case exercises mode/privacy, health, permission, capacity,
context, cost and capability denials while retaining viable diverse fallbacks.
The exploration case leaves half the pool without measured history. Time and
random draw are fixed inputs; these cases do not model stochastic workload
variation. They exclude configuration, classification, evidence retrieval,
hardware profiling, reservation, database I/O and inference. Results are Go
benchmark means, not latency percentiles.

The large gap between pure selection and the full task fixture motivates profiling
evidence reads/admission/persistence next; it does not attribute the entire gap
to a particular query. Neither benchmark qualifies auxiliary model classification
or concurrent production latency. No timing threshold is enforced in CI.

## Historical three-operation checkpoint

Local measurements on Apple M4 Max, darwin/arm64, Go 1.27.1. Each displayed
measurement is the mean of three timed operations in one benchmark run, not a
percentile or statistically qualified regression threshold.

## Reproduction

```sh
go test ./internal/telemetry -run '^$' -bench BenchmarkOutputValidity -benchtime=3x -count=1
go test ./internal/app -run '^$' -bench BenchmarkAutomaticTaskOverhead -benchtime=3x -count=1
```

Both commands create temporary private databases and require no provider keys.
The application benchmark uses two or eight models, immediate loopback Ollama
fixtures, a warmed discovery cache, and mocked fresh resource capacity. It
includes admission, SQLite reads/writes, provider HTTP, and durable task
execution, but excludes real inference and hardware sensors. It seeds checked
tasks evenly across models through the runtime. Each timed operation adds a
task; final task count is seed count + one warmup + timed operations.

## Full automatic-task fixture

| Model pool | Seed tasks | Final tasks | Mean ms/op |
| --- | ---: | ---: | ---: |
| 2 | 0 | 4 | 4.23 |
| 2 | 100 | 104 | 15.35 |
| 2 | 1,000 | 1,004 | 34.35 |
| 8 | 0 | 4 | 4.24 |
| 8 | 100 | 104 | 16.67 |
| 8 | 1,000 | 1,004 | 102.72 |

## Fixed-corpus validity reads

Each populated lookup validates the latest 100 checked attempts. Go-source
fixtures contain a package declaration and a 4 KiB comment. These reads do not
include routing, discovery, hardware profiling, or inference.

| Lookup | Corpus tasks | Mean ms/op |
| --- | ---: | ---: |
| Text validity | 100 | 9.62 |
| Text validity | 1,000 | 9.76 |
| Go-source validity | 100 | 16.95 |
| Go-source validity | 1,000 | 16.42 |
| Model with no history | 100 | 0.49 |
| Model with no history | 1,000 | 0.59 |

Before this checkpoint, the 1,000-task cold-model lookup measured 35.00 ms.
Schema 8 adds a model/provider turn-start index. The read path uses that index
to check candidate population, selects a bounded recent set before loading
output evidence, and chooses a join plan for sparse versus dense model history.
The choice changes query planning only, not evidence eligibility or ordering.
An index-only intermediate implementation regressed populated reads; the
benchmarks caught that regression before the checkpoint was committed.

## Limits and remaining work

The DAR-44 qualification above establishes the deterministic target on its
named local host, not a portable production SLA. No auxiliary classifier is
enabled or measured. The fixtures cover tiny prompts/answers, one provider,
no concurrent requests, no review calls, and at most 1,000 seed tasks. Larger
outputs, more models, sparse domain/profile populations, long sessions, real
resource profiling, database contention, and live providers remain unqualified.
The Go parser rechecks stored source during reads; larger code can cost more.
Repeat longer runs and record latency distributions before setting performance
gates. Hosted CI performance is not verified.
