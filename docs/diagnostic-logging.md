# Diagnostic logging

DarwinRouter continuously records redacted input, complete model turns, tool
activity, routing decisions, revisions, evaluations, and terminal errors in its
SQLite event journal. `darwin logs` exposes those committed records as versioned
JSON lines without creating another automatic content store.

```sh
# Follow new activity and errors. No prompts or answer content by default.
darwin logs --config /path/to/config.yaml --follow

# Inspect one task, including its redacted prompt, answers, and tool I/O.
darwin logs --config /path/to/config.yaml --task TASK_ID --include-content

# Resume an export using the last diagnostic.checkpoint.next_after value.
darwin logs --config /path/to/config.yaml --task TASK_ID --after 1234 --include-content
```

`--limit` limits visible records per page (1–100, default 100). A one-shot command
reads one page from `--after` (default 0). Its final `diagnostic.checkpoint` record
contains the next position and whether more records remain. `--follow` starts at
the current head unless `--task` or `--after` is supplied; it catches up through
available pages, then polls once per second until interrupted. Each poll scans
at most 1,000 matching journal entries, with an eight MiB output page ceiling.
Task-filtered SQLite queries may inspect additional unrelated rows, with a
five-second query timeout. Model delta
and worker heartbeat records are authenticated but suppressed; complete model
output appears on `turn.completed`.

Each activity line includes durable task, session, turn, and attempt identities;
the actual model/provider; selected context; whole-task and current-turn elapsed
milliseconds; reported token usage; structured error codes; and routing rank,
fitness, exclusion, candidate, and policy metadata when available. Unavailable
measurements are `null`, including resources, token usage, and context sizes.
`response.revision` records show when output correction was requested. These
logs do not create quality feedback or convert execution errors into failures
of a model's reasoning. `evaluation.recorded` contains runtime checks identified
by `code`; it is not the external quality-feedback verdict. Inspect the existing
`/v1/feedback/TASK_ID` history for accepted/rejected quality feedback and revisions.

`--include-content` explicitly includes private session content. It preserves
the already-redacted journal and additionally removes currently configured API,
provider, collector, and `security.redact_env` credentials at export. If two JSON
keys collide after redaction, export fails instead of silently losing content.
The original journal is never rewritten. Existing runtime content limits still
apply; output is not silently truncated by the logging command.

The daemon also emits content-free `http.request.completed` JSON lines to its
existing stderr log. These include a fixed route label, method, status, generic
HTTP error code, duration, and byte count. They cover rejected requests that
never reach task admission. URL parameters, query strings, request/response
bodies, credentials, and raw error messages are excluded. A streaming response
can report HTTP 200 even when a later runtime failure occurs; inspect its durable
task errors for that distinction.
