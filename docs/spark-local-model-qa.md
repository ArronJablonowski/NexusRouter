# DGX Spark and local-model qualification

DGX Spark uses a shared CPU/GPU memory pool. NexusRouter recognizes the kernel
DMI vendor `NVIDIA` and product `NVIDIA_DGX_Spark` as unified memory. Its RAM
measurement still comes from `/proc/meminfo`, constrained by visible cgroup-v2
limits. It does not add a second GPU pool or convert unavailable `nvidia-smi`
VRAM readings into zero capacity.

Include model weights, context/KV memory, and runtime overhead in `ram_bytes`;
leave `vram_bytes` zero on this host. The configured RAM percentage, current
availability, shared reservations, concurrency, and pressure rules still apply.
The observed host exposed 130,661,138,432 bytes of system RAM; that is a total,
not a promise that every byte is available for model execution. Other Linux
platform identities are not automatically classified as unified-memory hosts.

## Repeatable real-model remote test

`TestPhysicalTwoHostLiveModel` is opt-in. It starts an isolated temporary
NexusRouter remote service on an explicitly trusted SSH destination, with its own
configuration, telemetry database, trust registry, certificates, and journal.
It calls the destination's existing loopback Ollama service. It does not install
models, change production services, or write quality feedback.

Supply these environment variables after checking the destination model catalog,
resident models, resource availability, and binary digest:

| Variable | Required value |
| --- | --- |
| `NEXUS_REMOTE_LIVE_MODEL` | `1` |
| `NEXUS_REMOTE_TEST_HOST` | Private destination IP. |
| `NEXUS_REMOTE_TEST_USER` | SSH account. |
| `NEXUS_REMOTE_TEST_KEY` | Absolute path to the authorized SSH key. |
| `NEXUS_REMOTE_TEST_KNOWN_HOSTS` | Absolute path to verified host keys. |
| `NEXUS_REMOTE_TEST_BINARY` | Absolute destination path to the built Linux binary. |
| `NEXUS_REMOTE_TEST_BINARY_SHA256` | SHA-256 of that exact binary. |
| `NEXUS_REMOTE_TEST_MODEL` | Installed completion model name. |
| `NEXUS_REMOTE_TEST_MODEL_RAM` | Conservative total memory estimate in bytes. |

```sh
go test -race ./remote -run '^TestPhysicalTwoHostLiveModel$' -count=1 -timeout=10m -v
```

Each transport, HTTPS and SSH, must produce the correct arithmetic output,
persist one task start with the actual model/provider identity, recover the
same completed submission after reopening the caller journal, reject a changed
payload under the same request key, and durably cancel a second task after
observing streamed model output. The test cleans up its owned remote service
and temporary directory. Ollama residency remains under the provider's normal
policy; unrelated residents are never unloaded.

The separate `TestPhysicalTwoHostHTTPSAndSSH` synthetic-provider qualification
covers controlled response loss, host restart, revocation, and harness faults.
Synthetic transport evidence and real-model output evidence answer different
questions; neither establishes comparative model quality.

## Recorded development checks

On 2026-10-02, isolated real-model CLI checks verified arithmetic, extraction,
and JSON output with Qwen3 8B on Spark and Gemma4 12B Q4 on macOS, including
single-candidate automatic selection. Qwen3-Coder-Next 80B also passed arithmetic
on Spark with a 64 GiB shared-memory estimate. Both hosts rejected unknown
models, oversized context requests, and unavailable capabilities without adding
task records. Both CLI processes persisted cancellation after SIGINT.

The first live remote test assertion incorrectly expected a configured alias in
runtime events; it was corrected to check the actual provider model name.
The confirmed product defect was Linux reporting `UnifiedMemory: false` on
Spark. The patched binary was checked on the physical host and now reports
`true`, retaining unavailable discrete VRAM readings as null. Regression tests
cover DMI matching, unknown platforms, cgroup ceilings, shared RAM reservations,
and rejection of a separate VRAM budget.

These checks use synthetic prompts with real inference. They are not a model
ranking, exhaustive installed-model coverage, or production release approval.
