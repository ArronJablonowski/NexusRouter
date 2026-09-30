# Managed local model residency

Task reservations and model residency are different: releasing a RAM/VRAM
reservation does not tell an inference server to evict cached weights. NexusRouter's
managed Ollama path adds provider inspection and confirmed unloading before
low-memory model switches, followed by fresh hardware admission.

## Explicit ownership

Enable this only for a dedicated Ollama server used by one NexusRouter Service
or daemon and no other clients:

```yaml
providers:
  - id: local
    kind: ollama
    endpoint: http://127.0.0.1:11434
    manage_residency: true
```

The default is false. This setting authorizes unloading configured idle models;
it does not delete models from disk, prune routing candidates, pull models, or
change tool permissions. Existing user configuration is not changed automatically.
An embedding host must reuse a Service to share process-local coordination.
Daemon processes additionally use the private per-host resource coordinator,
so independent NexusRouter workspaces contend for the same bounded local
capacity rather than trusting separate in-memory counters.

Managed providers require a loopback root endpoint, local models and distinct
canonical model identities. Duplicate provider entries sharing a loopback port
are rejected, including common localhost/IPv4/IPv6 aliases. This is conservative
configuration validation, not proof of exclusive server ownership. Proxies or
forwarders on different ports and outside clients remain an operator concern.
Do not enable this setting on a shared inference server.

## Admission and confirmation

The application protects active managed model use throughout the task, including
tool pauses and auxiliary model operations. A model still in use cannot be
evicted to admit another model. Low-memory switching inspects the provider's
resident inventory and unloads only unambiguous configured idle models.

An unload uses a non-streaming empty generation request with `keep_alive: 0`.
Success requires an unload acknowledgement and a subsequent resident-inventory
observation that no longer contains the target. NexusRouter does not equate an HTTP
200 response or a released logical reservation with confirmed absence.
These operations follow Ollama's [generate contract](https://docs.ollama.com/api/generate)
and [running-model inventory](https://docs.ollama.com/api/ps).

After switching, actual hardware pressure is measured again and the existing
RAM/VRAM/concurrency budget must still admit the replacement. Reported model
sizes are never credited as reclaimed physical memory. A failed or ambiguous
unload does not dispatch the replacement. The next admission must inspect
current provider state rather than assume that the previous operation succeeded.
Within an uncoordinated embedding Service, an uncertain unload remains blocked
until fresh inventory shows both its identity and original digest absent;
queued retries do not send that unload again. A daemon with host coordination
does not invoke this process-local unload authority at all. Ollama may load the
selected model during inference, but NexusRouter will not unload or steal a
peer daemon's active or uncertain resident model. Durable provider-lifecycle
operations remain a separate post-MVP enhancement.

Residency management is authorized admission-time maintenance, not a guarantee
that the task will subsequently run. Basic request constraints, credentials and
continuation-source checks precede managed explicit admission. Later context,
memory, tool, persistence or inference checks can still reject the task after an
idle model was unloaded. That does not count as task completion or positive
fitness evidence. No lifecycle operation creates an evaluation success record.

Only exact model identities and an omitted final `:latest` tag are matched;
registry/library aliases and digest equivalence are not guessed. No inference
prompt or model output is sent by residency operations. Requests use the same
loopback-deny-external transport boundary as local inference, with bounded
responses, deadlines, no redirects and no automatic unload retries.
An entire managed admission has a cooperative ten-second deadline, unloads at
most eight different idle models, and accepts at most 256 inventory entries
and 256 configured models across the catalog.
Low-memory selection includes live reservations and the selected device's
headroom. Unknown residents or ambiguous equal-digest aliases block switching;
they are never silently evicted.

## Qualification limits

The optional unload implementation is Ollama-first. Custom provider factories
and other inference engines do not acquire unload authority implicitly. The
daemon's durable host reservation coordinates NexusRouter processes only; it
does not fence outside clients, prove GPU placement, or claim durable provider
residency ownership. Provider absence is an observation, not protection against
another client immediately reloading that model. Physical memory recovery is
separately checked through the resource profiler and remains subject to its
limitations.

Tests use controlled local HTTP servers and injected hardware observations;
they do not establish physical VRAM release on all supported hardware classes.
