# Routing benchmark checkpoint

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

These results do not establish the PRD's 150 ms deterministic-routing or 500 ms
auxiliary-classification SLA. They cover tiny prompts/answers, one provider,
no concurrent requests, no review calls, and at most 1,000 seed tasks. Larger
outputs, more models, sparse domain/profile populations, long sessions, real
resource profiling, database contention, and live providers remain unqualified.
The Go parser rechecks stored source during reads; larger code can cost more.
Repeat longer runs and record latency distributions before setting performance
gates. Hosted CI performance is not verified.
