# Live text in interactive chat

`darwin chat --config path/to/config.yaml --model MODEL_ID` now combines
committed lifecycle events with provisional live assistant text. Configure the
model and conservative local memory estimate first; the sample configuration
does not guess those values. This works through the existing application
service, not a second inference or simulated replay of a finished answer.

Model text appears under `[assistant provisional]`, with each explicit line
prefixed `| `. Task/turn/tool status is unprefixed. A successful return from the
application produces a separate `[task completed]` line. The final answer is not
printed a second time. Model text can include intermediate assistant turns;
it is not necessarily identical to the final answer stored in task history.
Line prefixes separate presentation, not cryptographic authenticity, and a
terminal may visually wrap a long model line.

Wait for completion before entering the next ordinary prompt. During generation,
`/status`, `/steer TEXT` and `/cancel` remain available. Steering is queued for
safe runtime boundaries; it cannot alter an already-dispatched tool call.
Ctrl-C requests cancellation during work and exits when idle. `/quit` and SIGTERM
cancel and join active execution. EOF waits for the active task. A normal chat
session exits zero even after an individual failed task, as before; use headless
`darwin run` for per-task process exit status.

## Safety and failure semantics

- Text becomes eligible for delivery only after its corresponding lifecycle
  marker is committed. Raw text deltas are not persisted or replayable. Use
  task/session inspection for durable completed-turn content and state.
- Known credentials are incrementally redacted across text chunks. Possible
  secret prefixes and incomplete Unicode can delay delivery until enough
  context arrives. Provider buffering and network behavior also determine
  granularity; live text does not promise one callback per token.
- The text channel excludes tool arguments/results and delegated child streams.
  Lifecycle metadata still reports tool starts/completions. These status messages
  are not evidence that a tool's output has passed every acceptance check.
- Terminal escape sequences, clipboard controls and bidi formatting are
  filtered across fragment boundaries. The filter retains state for the whole
  task, including across intervening status messages, and resets for the next
  task. Unterminated escape payloads remain suppressed. This affects display,
  not the persisted source content or tool permissions.
- A single unbuffered delivery channel provides ordered backpressure. At most
  1 MiB of redacted text is admitted for display per requested task, before
  terminal filtering and line-prefix expansion. Exceeding it cancels/joins the
  run and reports an output failure. There is no unbounded answer accumulator.
- Broken, short or panicking output writes stop display and cancel/join work.
  Actual stdout pipes use the existing cancelable fifteen-second write bound
  and restore borrowed descriptor flags. Custom writers and regular-file
  operations retain their own blocking semantics.
- Failure or cancellation after partial output does not turn that text into a
  successful answer. Chat reports that the task did not complete successfully,
  retains the previous successful continuation source, and disallows feedback
  on the failed attempt through the current conversation's feedback commands.
  Successful completion is still distinct from semantic correctness: use the
  appropriate deterministic validators and feedback.

Delivery can fail after a completion record commits. In that case, inspect the
durable task before deciding whether to retry; an absent terminal status on
your display is not proof that the provider never completed or incurred cost.
If the output pipe itself is broken, a diagnostic may also be undeliverable.

## Combined Go SDK interface

`sdk/v1.Client.RunLiveStream` accepts a version-1 request plus two nonnil,
synchronous callbacks: `func(runtime.Event) error` and `func(string) error`.
It returns the same versioned result/error contract as other execution methods.
For a given durable marker, its event callback runs before the associated text
callback. An error or panic in either callback stops both delivery paths and
cancels execution; durable cleanup continues without further callbacks.
Cancellation from the terminal event callback is returned even if completion
has already committed and trailing text is therefore withheld.

Callbacks must return promptly, cooperate with cancellation, and must not wait
for their own task to finish. Lifecycle event payloads can contain redacted
model/tool content; the **text** callback is the channel that excludes tool and
child output. SDK clients must implement their own safe rendering and output
limits. The CLI's terminal filtering, line prefixes and display cap are not
silently applied to SDK callbacks. Existing `RunStream` (lifecycle only) and
`RunTextStream` (text only) interfaces remain available and unchanged.

## Qualification scope

Tests cover actual loopback Ollama-compatible HTTP responses blocked until the
consumer observes a text prefix, split-secret/Unicode handling, no duplicated
answer, failure after partial output, active steering, last-successful-history
retention and output-error cancellation. Stateful display tests cover escape
payloads split across fragments and status messages, per-task filter reset,
quoted model-imitation status lines, byte limits and invalid UTF-8.

Owned subprocess tests exercise the actual CLI entry point with temporary YAML
and real stdin/stdout pipes (without injected chat hooks). They prove prefix
delivery before provider completion and verify that closing the stdout reader
cancels the provider and exits normally with code 1, not by SIGPIPE.

These are local fixtures, not new live Sol/Ollama inference or universal
provider-version qualification. Full-screen editing, multiline prompt editing,
automatic context compaction and general interrupted-task resume remain outside
this change. JSON task mode remains lifecycle JSONL, not a new token protocol.
