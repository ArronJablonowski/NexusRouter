# Reviewed creation of local files

`darwin chat` can offer the built-in `create_file` tool to a local model. This is
an opt-in write capability, not a shell or an existing-file editor. Enable it
only for a narrow, caller-owned directory whose contents may be shown to the
model and operator:

```yaml
tools:
  enabled: true
  read_root: /absolute/path/to/workspace
  create_enabled: true
  create_root: /absolute/path/to/output
  max_turns: 8
```

Both directories must already exist. Configure local model context and resource
metadata as for read-only tools, then run
`darwin chat --config path/to/config.yaml --model LOCAL_MODEL_ID` in a terminal.
Input **and** output must be actual terminal descriptors; redirected/headless
chat cannot approve creation. New files are readable by the model only when
they also fall within `read_root`. File tools still exclude cloud execution.
This feature does not grant native Codex or delegated workers a new filesystem
capability. Environment overrides include `DARWIN__TOOLS__CREATE_ENABLED` and
`DARWIN__TOOLS__CREATE_ROOT` through the existing layered configuration loader.

## Per-call review

`create_file` explicitly declares `non_idempotent_write`. The declaration appears
in its approval and durable tool events; it neither changes no-overwrite behavior
nor permits retry. Terminal preview rejects a contradictory declared class.
See [tool behavior](tool-behavior.md) for the separate operation/effect contracts.

The terminal shows a pending approval ID, configured root, relative path, byte
count, content SHA-256, and complete content as ASCII-quoted lines. Escapes
represent the decoded characters and newlines; content is not executed or
silently truncated. The maximum content is 64 KiB of UTF-8. Known configured
credentials in displayed fields cause rejection, not a misleading redacted
preview. This is not a classifier for every possible sensitive value.

Enter `/approve ID` or `/deny ID` with that exact pending ID. No wildcard,
blanket approval or model-generated command grants authority. Wrong, stale or
reused IDs cannot authorize a later call. Cancellation, expiry, EOF, quitting
or failed preview delivery denies unavailable review. EOF still waits for
active execution to settle. A submitted decision is not a file-creation result;
inspect the final outcome and durable records before retrying uncertain work.

A certain creation failure records `tool_failed` separately from `no_effect`
and fails the task. No effect describes filesystem state, not successful work.
Creation failures remain nonrecoverable; the separate
[read/delegation repair path](recoverable-tool-failures.md) does not change write
approval or retry policy.
That failure cannot become a successful procedural workflow merely because
separate feedback accepted an overall answer. The current loop stops rather
than automatically retrying a failed tool, including a proven no-effect failure.
Any operator-requested retry needs a fresh approval. Tool-aware fallback after
certain failures remains open; uncertain effects must not be blindly retried.

The application binds approval to task, turn, call, exact arguments, schema
and effective policy. It durably consumes approval under a writer lease before
dispatch. The terminal authenticates through the caller-owned process; it cannot
prove a human, rather than another trusted local process, typed input. Raw
review previews are ephemeral. Existing session/tool records remain sensitive
durable content subject to redaction/retention rules; enabling this tool is not
permission to persist credentials.

## Filesystem guarantees and limits

- Only new regular files are created. Relative paths stay within the pinned
  root and parents must exist. Existing files, symlinks, directories, FIFOs and
  devices are never replaced, including a target created after validation.
- The root and existing parent are pinned with `os.Root`. Content is staged in
  a private directory, synced, and published by an atomic no-replace hard link.
  Files start with mode `0600`; the containing directory is synced before a
  confirmed result. Filesystems without required operations fail safely.
- Cleanup removes only private staging artifacts, never the final target.
  Cleanup, publication, cancellation, storage or lease ambiguity is treated
  conservatively. A crash can leave a file or private staging directory even
  when completion was not acknowledged. There is no automatic deletion or
  replay of a spent approval to resolve that uncertainty.
- The lease scope hashes the pinned root's device/inode identity, so aliases
  share a scope. Coordination requires the same database and cooperative
  processes. Different nested roots have different scopes; atomic no-replace
  still prevents overwriting a shared target.
- This is **not an OS sandbox**. Leases cannot fence arbitrary external writers,
  and pinning a directory does not prevent another actor from renaming it.
  Caller-owned roots, database and filesystem remain trusted. Stronger
  subprocess/container isolation and general editing remain separate work.

## Embedding and unsupported adapters

Go hosts can enable the same configuration and supply the existing trusted
`ConfigOptions.ApprovalReviewer` or `ConfigOptions.ApprovalPresenter` SDK control.
Hosts own authentication and safe preview delivery. Without either control,
execution is denied before task dispatch. Side-effecting definitions cannot
bypass authority by changing policy from `ask` to `allow`.

The default headless CLI and daemon do not install an interactive reviewer.
Durable submission execution is rejected for this process-local capability;
there is no silent approval or resumed call with freshly attached authority.
Children remain read-only even when the parent has creation enabled.

Existing-file modification, bulk writes, executable tools and unattended
approval policies are not implemented by this increment. Full PRD acceptance
still requires the broader tool, recovery and isolation requirements.
