# ComfyUI connector

NexusRouter's `nexus comfy` commands control an explicitly selected self-hosted ComfyUI HTTP API. The Go client is in `connectors/comfyui`. It is an additive command adapter; it does not replace Spark's NexusRouter daemon or alter ComfyUI services. Automatic model/tool-loop registration, web controls, GPU admission coordination with model inference, uploads and diffusion-model benchmarking are not implemented in this checkpoint.

## Connect to Spark

Keep ComfyUI bound to loopback. Use your existing authenticated SSH access to forward a local port, for example:

```sh
ssh -N -L 127.0.0.1:18188:127.0.0.1:8188 your-spark-host
nexus comfy info --endpoint http://127.0.0.1:18188
nexus comfy nodes --endpoint http://127.0.0.1:18188
nexus comfy queue --endpoint http://127.0.0.1:18188
```

The endpoint is an explicit HTTP(S) origin with no embedded credentials, path or query. Redirects are refused. Use a private tunnel or HTTPS rather than exposing an unauthenticated ComfyUI port. The connector makes no changes to trust, firewall, models or services.

## Run a workflow

Export ComfyUI's **API-format** node graph, rather than the UI graph containing nodes/links arrays. The supplied smoke workflow creates a small image, scales it and saves it without loading a diffusion model.

```sh
nexus comfy submit --endpoint http://127.0.0.1:18188 \
  --workflow examples/comfyui/smoke.json --receipt /private/path/new-job.json
nexus comfy wait --receipt /private/path/new-job.json --timeout 2m
nexus comfy status --receipt /private/path/new-job.json
nexus comfy download --receipt /private/path/new-job.json \
  --node 2 --image 0 --output /private/path/new-image.png
```

The receipt's parent directory must exist. Each submission requires a new receipt filename. Records contain an endpoint, client ID, workflow digest, state and confirmed prompt ID; they omit workflow contents. Receipts and downloaded files are private (0600) and existing download destinations are never overwritten. Keep receipts, workflow inputs and generated images outside Git when private.

A private receipt is persisted before sending. Exclusive creation arbitrates concurrent callers. Accepted IDs are saved atomically; `status`, `wait` and `download` resume using the same record and verify endpoint/ID consistency. Network errors, malformed responses, redirects and ambiguous server failures retain an uncertain/prepared record. They never trigger an automatic POST retry. ComfyUI's client ID is correlation metadata, **not** an idempotency key. A crash between send and receipt update may leave accepted work without a locally confirmed ID.

If a record has no confirmed ID, do not submit the workflow again: inspect the selected server's queue/history for the recorded client ID and recover the original prompt ID through an operator-verified process. ComfyUI history can disappear after service restart; absence is not proof that execution never happened. Automatic reconciliation of this uncertain interval is not implemented. HTTP 400 prompt validation rejection is recorded separately, without disclosing server response contents.

Polling deadlines and Ctrl-C stop observation only, preserving the remote job. `cancel-pending --receipt FILE` requests deletion of that exact pending ID; it does not interrupt running work. A job that starts during deletion is untouched. No global queue clear, interrupt, history deletion, arbitrary endpoint path, or model management operation is exposed.

Workflows can invoke installed custom nodes and consume GPU resources. Run trusted workflows and inspect the queue/capacity before dispatch. This CLI does not grant unattended agents execution authority or reserve capacity through NexusRouter's model scheduler.

## Validation

See `docs/validation/2026-10-13-comfyui.md` for mock failure-path tests and Spark execution evidence. Protocol reference: [ComfyUI server routes](https://docs.comfy.org/development/comfyui-server/comms_routes).
