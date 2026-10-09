# Assign a task to a host and model

In a new Web UI chat, start the prompt with `@hostname/model-ID`, followed by
self-contained task text. The hostname may also be a paired-host ID. Use `local`
(or this commander's reported hostname) for a model configured on this host.

```text
@dgx-spark/laguna-s Review the code below for cancellation bugs.
@local/qwen-coder Explain this failing test.
```

These are illustrative IDs. Type `@`, choose a verified host, then choose one of
its models. Tab or Enter accepts the highlighted option; choosing an option does
not submit a task. Escape closes suggestions. The composer displays the resolved
host/paired ID and model/configured ID before sending. Autocomplete inserts stable
IDs, while the visible label retains the reported hostname and model name.

A unique advertised model name also works. Encode spaces when typing a name
manually, for example `@dgx-spark/Laguna%20S`. Prefer autocomplete/configured IDs
for names containing spaces or punctuation. Exact, case-sensitive IDs take
precedence over name aliases. Otherwise names are matched without case and must
resolve unambiguously; duplicates are rejected.

## Execution and recovery

Local selections use the existing durable browser submission flow and persist a
strict no-fallback flag. Remote selections use the caller-bound recorded dispatch
flow, saving host and request ID before network dispatch. The destination checks
the expected advertised model name against its configured alias before admitting
work. Remote direct tasks never activate a commander's alternate default model.
All permission, privacy, capability, context and resource checks remain enforced.
A failed or unavailable exact assignment does not choose another model or host.

New remote exact-name assignments require an updated peer advertising
`targeting_version: 1`. The UI refuses unsupported peers before dispatch. SDK
backends implement the name check; custom backends must implement equivalent
checks before advertising support. Existing host-only/dropdown requests keep their
legacy wire shape. This capability flag is distinct from conversation transfer.

If delivery becomes uncertain, the saved remote request remains inspectable and
cancelable through the existing recovery controls. It is not resent or assigned
elsewhere. Reloading a receipt does not start another task. **New remote task**
creates independent work; it does not retry or cancel the previous request.

## First-version boundaries

- This is leading-mention parsing in the Web UI composer, not arbitrary natural
  language routing or a CLI/SDK prompt parser. Embedded mentions and email
  addresses inside ordinary task text are not interpreted as assignments.
- Use one exact assignment in a new chat. Existing local chat history and files
  are not transferred to a remote host. Provide the necessary code/context in
  the task text. Separate tasks are needed for multiple specialists.
- The remote composer keeps its existing private, zero-cost local-model policy
  and conservative context ceiling (up to 4096 tokens within peer/model bounds).
  Cloud/paid remote tasks and portable tool authority need a separate reviewed
  budget/permission flow. Locally configured cloud models still follow ordinary
  local submission privacy and admission policy.
- `@host/auto`, `@auto/model` and automatic fallback for an exact assignment are
  not supported in this version. Plain prompts retain existing routing policy.
- Catalogues are metadata previews. Fresh destination admission is authoritative;
  unavailable/denied destinations may still reject a task after the preview.
