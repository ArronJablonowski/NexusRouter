# Signed-in Sol skill drafting

The experimental `codex_app_server` provider can generate inactive procedural
skill proposals from accepted recorded task examples using exact `gpt-5.6-sol`.
It reuses the signed-in Codex CLI rather than requiring a separate API key or
extracting credentials. This is cloud inference even though the CLI runs locally.

Use the existing `GenerateSkillDraft` or `GenerateSkillSelection` service/SDK
operations, or the corresponding skill-generation CLI/API operations, with a
configured Codex model ID. Enabled background learning may also select that
model. All participating source tasks must be `cloud_allowed`, the skill scope
must have `local_only: false`, and deployment mode must be hybrid or cloud-only.
Local-only source content never falls back to Codex. Do not enable cloud sharing
for a scope merely to work around a denied generation.

The provider must specify the installed CLI's absolute `executable` path. The
existing supported CLI version, exact model and launch-profile checks still
apply. No new executable, provider or learning setting is enabled by upgrading.
Model context/cost metadata and per-call maximum cost remain mandatory;
automatic learning additionally requires an aggregate generation budget.
Estimated cost is admission accounting, not a measured bill or account usage cap.

## Durable and tool-free execution

Provider construction is inert. The generation attempt (and any budget
reservation) is committed before estimation or CLI startup. Context estimation
includes the closed output schema. Only after successful estimation does the
one-shot provider launch an owned CLI session with its private working directory.
The existing 30-second generation deadline includes startup and streaming.
Completion or failure closes the provider and removes that owned directory.

Trusted instructions remain a system message; recorded examples remain an
untrusted user JSON envelope. Generation exposes no Darwin tools. Unexpected
tool calls, malformed streams, non-stop finishes, excess output and invalid
schemas/results are rejected. The shared CLI launch profile independently
restricts built-in tools, extensions and inherited context; this feature does
not broaden those permissions or prove universal subprocess containment.

The schema has eight host-defined fields and excludes key, identity and
provenance. It permits an all-empty abstention shape, which the host rejects as
an unqualified proposal. Host parsing still rejects duplicate or unknown fields,
invalid identifiers, blank required content and output over 64 KiB. A response
matching a JSON schema is not proof that the proposed workflow is useful or that
its proposed validation cases have passed.

The host derives provenance from admitted source sessions and accepted evidence.
Native source preparation decodes the original serialized messages before
redaction, including structured tool arguments/results and JSON-escaped secrets.
Ambiguous field spellings, duplicate keys and malformed structured observations
are rejected. Current credentials are checked again after context estimation and
after CLI startup; if the admitted inputs would change, generation fails rather
than silently modifying the estimated request. This is not an atomic secret-
rotation boundary or a general detector for arbitrary secret encodings.

Direct-generation input digests now include the native executable and structured-
output mode. Planned selections already bind the full configuration; changing
the executable or other bound policy invalidates an old selection. Existing HTTP
generation keeps its prior unstructured-output default and transport controls.

Successful generation saves a `drafted` proposal in telemetry. It does not publish
or activate it. Explicit publication or an enabled learner may publish an inactive
version; activation separately requires trusted deterministic validation.
Failed and uncertain started attempts never authorize automatic redispatch of
the same attempt ID. Inspect the saved result before taking any operator action.

## Qualification

Synthetic tests cover the real bridge's role separation and output-schema wire
shape, durable launch ordering, privacy/cost/context denial, provenance, redaction,
cleanup, failed-attempt retention, executable-bound selections and no redispatch.
The ordinary suite does not launch Codex.

An explicit supervised live check is available:

```sh
DARWIN_CODEX_LIVE_SKILL_DRAFT=1 go test -race ./internal/app \
  -run '^TestLiveCodexSkillDraft$' -count=1 -v
```

This uses account usage. It sends only two synthetic accepted Go-code examples
from a loopback fixture, not user workflows or actual local-model inference.
Output content is withheld from the test log. Live execution evidence is recorded
in [implementation progress](progress.md); the existence of this command alone
does not establish that it passed.

The first explicit live check passed in 12.51 seconds: one launch/stream, 1,106
response bytes, a durable draft with host-derived provenance, unchanged synthetic
sources, and no publication or activation. This qualifies that bounded path,
not semantic workflow correctness or real-local-model background learning.

This integration uses the documented per-turn `outputSchema` interface.
See [official Codex app-server documentation](https://learn.chatgpt.com/docs/app-server).
Standalone validation-engine configuration and full background-learning
qualification with real heterogeneous local models remain separate work.
