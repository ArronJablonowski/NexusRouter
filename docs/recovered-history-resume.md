# Recovered-history resume

NexusRouter can queue new work from an exact failed task history only after
durable recovery has proven that history safe to reuse. This is not execution
retry: the new task receives the recovered context plus a new prompt, while the
interrupted provider call and any tool effect are never replayed.

Eligible sources are replay-validated `recovered_model` or
`recovered_delegation` histories. Ordinary failed, canceled, running, completed,
worker-derived, stale, corrupt, pending-tool, or uncertain-effect histories fail
closed. The caller presents the content-free `TaskHeadFence` returned by session
task inspection. Admission re-derives the private history digest and recovery
authority from SQLite; the public caller never supplies that digest.

## CLI

```sh
nexus resume \
  --config examples/local.yaml \
  --key unique-resume-key-001 \
  --task TASK_ID \
  --session SESSION_ID \
  --sequence HEAD_SEQUENCE \
  --event HEAD_EVENT_ID \
  --model local-fast < prompt.txt
```

The key must contain 16–128 printable non-space ASCII bytes. Standard routing
constraints accepted by `nexus submit` are also accepted. `--continue-task`,
stored/manual compaction, and JSON lifecycle streaming are rejected because the
source fence already identifies the only permitted history. Input is nonblank
UTF-8 bounded to 1 MiB. Success prints one durable submission status; an
independently running daemon executes the queued work.

## HTTP API

Send authenticated `POST /v1/tasks/{source_task}/resumes` with
`Content-Type: application/json`, one `Idempotency-Key`, and this strict body:

```json
{
  "version": 1,
  "source": {
    "version": 1,
    "task_id": "TASK_ID",
    "session_id": "SESSION_ID",
    "head_sequence": 4,
    "head_event_id": "HEAD_EVENT_ID"
  },
  "request": {
    "model_id": "local-fast",
    "prompt": "Review the safely recovered result."
  }
}
```

Unknown, duplicate, case-mismatched, ambiguous, oversized, or path-mismatched
fields are rejected before admission. HTTP `202` represents queued or running
work; an already-known terminal idempotent result may return `200`. Errors use
fixed codes and do not expose prompts, source content, credentials, or internal
storage details. Browser origins are denied.

## Go SDK

Call `Client.SubmitResume(ctx, key, fence, Request{Version: 1, ...})`. The SDK
performs public shape and cancellation checks before storage access. Preserve
the exact key, fence, request, and configuration when retrying uncertain
delivery. Admission is not evidence that a daemon has executed or accepted the
new task output; inspect the returned durable submission ID.
