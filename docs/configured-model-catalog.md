# Configured model metadata catalog

DarwinRouter exposes the validated routing metadata declared in configuration
without probing a provider, reserving hardware, opening the task database or
running inference. This is an inspection snapshot, not a readiness report or
execution authorization.

Use the CLI:

```sh
darwin models list --config config.yaml
```

The command now returns a versioned object with `version`, `config_id` and a
`models` array. Earlier development builds returned the bare configured-model
array; consumers must update for this intentional pre-release schema change.
`config_id` is the SHA-256 fingerprint of the same redacted configuration used
by automatic route explanations.

Each model includes its Darwin alias, provider alias, implementation name,
locality, capabilities, context-window declaration, optional configured cost,
RAM/VRAM estimates, optional GPU binding and optional failure domain. A null
`estimated_cost` means no estimate was configured; zero is a distinct explicit
estimate. Catalogs contain at most 256 models and each model at most 128 unique
capabilities.

Authenticated daemon clients can call:

```text
GET /v1/routing/models
```

The endpoint accepts no query or request body, is bounded by the same catalog
capacity domain as `GET /v1/models`, and returns the same native versioned
record as the CLI and Go SDK. The SDK method is
`Client.ConfiguredModelCatalog(ctx)`.

Provider endpoints, credential environment-variable names and credential
values are never included. The result also deliberately contains no discovered
installation, live health, quota, current capacity, loaded-residency, measured
billing or availability claim. All normal privacy, provider, resource, budget
and tool-policy checks run later when a task is admitted.

`GET /v1/models` remains the separate OpenAI-compatible minimal catalog. It
contains only configured Darwin model IDs and compatibility fields; its response
shape has not changed.

### Cloud context recommendations

Cloud models use their configured/provider-recommended context window by default;
the local 32K starting tier, RAM/VRAM fit checks and learned local context tiers
do not reduce cloud allocations. Explicit request context budgets remain bounded
by the provider window.

For `codex_app_server`, new explicit/automatic admissions and catalog inspections
read `models_cache.json` from `CODEX_HOME` (or `~/.codex`). A catalog fetched within
24 hours supplies the exact model's `context_window`, replacing stale configured
ceilings and starting tiers in an owned snapshot. `max_context_window` is not the
recommended default and is deliberately not used. Changes are picked up without
a daemon restart; running attempts keep their admitted snapshot. This reads
metadata only and does not dispatch inference or consume credentials.

Missing, stale, malformed, duplicate, or oversized catalogs leave the configured
limit intact. Other cloud providers currently retain their configured limits:
OpenAI-compatible `/models` does not standardize a recommended context field.
This is not a claim of universal discovery. Provider recommendations unavailable
from a supported metadata source must not be guessed from local capacity or a
different API product's advertised maximum.
