# Bounded provider recovery

Automatic routing can move to another eligible LLM after a failed first model
turn. This addresses repeated invalid or incomplete provider streams without
changing parser checks, provider safeguards, model capabilities or tool policy.

The first task must durably fail before the next one starts. No completed model
turn, tool proposal or applied/pending steering may precede recovery. Provider
panics, authentication/configuration errors, context overflow, validation
failures, persistence ambiguity and tool failures do not gain retry authority.
A completed or uncertain tool effect is never replayed. Failures after tool use
remain terminal and require explicit continuation or investigation.

Known invalid/incomplete stream errors and provider-owned request timeouts can
recover even after incomplete text. The next model receives the original
request and admitted conversation context, not the partial failed answer. Failed
attempts, delta events and failure codes remain in history; existing payload
redaction and incomplete-text retention policies are unchanged. The new terminal
code is `provider_failed_before_tools`. Caller cancellation and deadlines stop
work; a provider timeout cannot replace an uncertain callback/persistence error.

| Runtime setting | Default | Allowed |
|---|---|---|
| `fallback_max_attempts` | 3 total attempts, including primary | 1–8; omitted/0 uses default |
| `fallback_timeout` | 10m after first failed attempt | 100ms–30m; omitted uses default |

One attempt disables fallback. The timeout covers fallback admission, discovery,
capacity waiting and execution, and never extends the caller deadline. Each
provider also retains its configured request timeout. Custom provider callbacks
must honor cancellation; arbitrary uncooperative in-process code cannot safely
be killed or overlapped with a successor. Exhausted candidates/attempt budget
return an explicit recovery-exhausted error and the last failed task identity.
Candidates rejected by fresh admission do not consume an execution attempt;
the router may continue through the bounded candidate list within that same
deadline to find another eligible model.

Candidates remain subject to fresh privacy/locality, capability, health, resource,
context and cost checks. No failed model is attempted twice in the selected
chain. A pinned model does not silently switch; the existing explicitly
configured commander-default fallback is the only pinned-model exception.
Runtime-host admissions continue to authorize exactly one attempt.

Each successor records the immediate predecessor. Failed streams receive no
successful quality feedback; acceptance/rejection of the completed successor is
attributed to its actual provider/model. Usage accounting keeps every attempt
separate, marks the successor as fallback and leaves unreported failed-stream
tokens unknown. Recovery does not train weights or create skills.

Deterministic tests use local HTTP fixtures, including a repetition-abort-shaped
Ollama stream, durable submission settlement, successful alternate selection,
feedback and usage attribution, cancellation, timeout, pinning, capability
exclusion, chain exhaustion, pending steering, failed persistence and tool
boundaries. These are not a claim that the benchmark's GLM/OCR combination is
recovered. That campaign remains complete with GLM marked failed by the user.
