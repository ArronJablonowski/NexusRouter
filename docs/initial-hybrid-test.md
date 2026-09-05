# Initial supervised hybrid test

The first live test uses **GPT-5.6 Sol (`gpt-5.6-sol`) as coordinator** and an
operator-selected installed Ollama model as the local worker. This is a
supervised qualification target, not a claim that live interoperability has
already passed.

OpenAI documents streaming and function calling for the exact requested model:
[GPT-5.6 Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol).
Actual account access must still be verified.

## Initial scope

1. Sol receives a small, non-sensitive task and delegates a bounded subtask.
2. One local worker at a time returns its result through DarwinRouter.
3. Sol reviews the worker result, identifies omissions or suspected defects,
   and produces the final answer. Review opinions are not test execution proof.
4. Inspect the parent/worker event history and provide explicit user feedback.
5. Repeat with an invalid or incomplete worker result, cancellation, and a
   clean daemon restart. Confirm failures are visible and no duplicate work
   is silently dispatched.

Start without filesystem tools, autonomous writes, automatic skill activation,
or background learning. Use a small text/code task; raw Go syntax validation
does not compile or execute code. Cloud coordination means the submitted task
and returned worker content reach OpenAI: local inference is not end-to-end
local-only privacy.

## Prerequisites and readiness evidence

- A locally configured `OPENAI_API_KEY` with access to `gpt-5.6-sol`; never paste
  the credential into chat or commit it.
- A hybrid configuration with Sol pinned as the parent and an explicit local
  `workers.delegate_model`, conservative RAM/context estimates, bounded call
  and cost limits, and capacity for the parent plus one child.
- A reachable Ollama endpoint and measured worker memory use. On inspection,
  the default local endpoint was reachable and reported installed models;
  the host reported 48 GiB of memory. Model fit and latency remain unqualified.
- A successful real parent-to-child-to-parent run with durable event inspection.

The repository's automated suites pass, but that does not establish API
credentials, live-provider interoperability, acceptable latency, or production
readiness. The initial setup/debugging estimate is 30–60 minutes after account
access is available; provider or hardware issues can extend it. Full MVP work
continues separately from this first test.
