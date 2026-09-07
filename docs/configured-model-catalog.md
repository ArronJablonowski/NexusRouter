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
