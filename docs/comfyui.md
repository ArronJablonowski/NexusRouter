# ComfyUI connector

NexusRouter's `nexus comfy` commands control an explicitly selected self-hosted ComfyUI HTTP API. The Go client is in `connectors/comfyui`. It is an additive command adapter; it does not replace Spark's NexusRouter daemon or alter ComfyUI services. Approved local chat tools and authenticated web controls are available when explicitly configured. GPU admission coordination with model inference, uploads and diffusion-model benchmarking remain unimplemented.

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

For the verified additive installation on Spark, run `~/.local/bin/nexus-comfy comfy help`; on the workstation use `~/.NexusRouter/bin/nexus-comfy comfy help`. Substitute `nexus-comfy` for `nexus` in the examples above. These installations preserve the existing daemon executable. After integrating this branch into a normal NexusRouter build, the same subcommand is available as `nexus comfy`.

## Enable chat and web controls

Add this operator-owned block to your existing NexusRouter configuration, selecting the origin reachable from the process running NexusRouter:

```yaml
comfyui:
  enabled: true
  endpoint: http://127.0.0.1:8188
  receipt_directory: /absolute/private/comfy-receipts
```

Create the receipt directory with mode 0700 before startup. On Spark, use ComfyUI's loopback origin above; on a workstation, use the origin of your private SSH tunnel. Receipts must remain private regular files; symlink receipts and different-endpoint records are rejected. Configuration inspection redacts both the origin and directory. The default disabled configuration preserves prior configuration encoding/identity.

Use the updated `nexus-comfy` executable with the existing configuration. Terminal chat (`nexus-comfy chat --config FILE --model LOCAL_MODEL`) requires a real terminal and shows the exact workflow arguments before `/approve` or `/deny`. Local chat models receive `comfy_inspect`, `comfy_status`, `comfy_submit` and `comfy_cancel_pending`. Inspect supports one installed node schema using `kind: node` and its simple class name. Writes always require review even when the general tool policy allows writes; an explicit deny policy remains denied. Input size and duplicate fields are checked before requesting approval. Cloud coordinators and worker children receive no ComfyUI execution authority.

For the web interface, use the updated executable when starting the configured daemon (`nexus-comfy serve --config FILE`) with Web UI enabled. Do not start a second daemon on the same database/listen address. Restart an existing daemon only at a verified idle boundary. The authenticated **ComfyUI** page is at the configured application prefix plus `/comfy` (normally `/app/comfy`). It accepts an API-format workflow file, retains the job reference, lists saved jobs, observes progress, previews PNG/JPEG/WebP outputs, downloads images and removes a specific waiting job. Signed-in browser sessions and the existing same-origin CSRF controls protect mutations; the browser cannot choose a server or local filesystem path.

Queued browser chat uses the existing durable tool-approval flow: it presents the exact ComfyUI workflow/cancellation arguments in the chat approval dialog and waits for a decision before dispatch. Ordinary background/native API callers without a trusted presenter cannot execute these writes. Chat and the ComfyUI page share the configured private receipt directory, so a job created by chat can be inspected and downloaded from the page. A same-key/same-graph repeat returns the saved record; a different graph conflicts rather than dispatching again.

A lost submission response retains its original reference and blocks automatic resend. Refreshing a page recovers saved receipts; it does not submit workflows. Polling pauses while the tab is hidden and does not overlap. Accepted receipt state confirms the original submission, not continuous visibility after ComfyUI loses its history. Uncertain delivery still requires operator reconciliation. Audio/video preview, uploads, decorated custom-node class-name discovery and shared GPU scheduling are not included.

These controls were added to the isolated executable and feature branch. Existing daemon processes/configuration are not changed automatically. See the chat/web validation report for deployment and test limitations.
