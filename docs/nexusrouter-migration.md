# NexusRouter name migration

The product is now NexusRouter. The executable is `nexus`, the Go module is
`github.com/ArronJablonowski/NexusRouter`, and release archives begin with
`NexusRouter_`. The macOS operating-system identifier remains `darwin`.

## Existing installations

Keep the existing configuration and its database, memory, skills and working
paths. This rename does not move, rewrite, reset or copy stored tasks, feedback,
usage, approvals or benchmark evidence. Explicit `--config` and database paths
continue to work. New default user configuration lookup uses
`nexusrouter/config.yaml`; when absent it falls back to `darwinrouter/config.yaml`.

`make build` creates `bin/nexus` and a `bin/darwin` compatibility symlink. The old
`cmd/darwin` Go entry point also remains available, but reports the new name.
Packaged releases install `nexus`; existing automation can keep invoking the
previous path by pointing that path at the verified new executable. Stop the
existing daemon before replacing its executable, retain a rollback copy, and
restart the same service with the same configuration. Do not start a second
service on the same data.

Runtime settings accept `NEXUS_API_TOKEN`, `NEXUS_PROCESS_OWNER_DIR`,
`NEXUS_RESOURCE_COORDINATOR_DB` and `NEXUS__SECTION__FIELD`. Corresponding
`DARWIN_` environment names remain supported. Canonical settings win when both
names are supplied, including explicit empty values. Configuration precedence
is independent of environment entry order. Both API-token names are redacted.
Build/qualification-only `DARWIN_*` switches retain their existing interface.

For installed macOS browser approval, the new service/configuration name is
preferred; the existing `com.darwinrouter.live-test` and Application Support
location remain discoverable when the new configuration is absent.

## Deliberate compatibility and historical references

- The shared `DarwinRouter/process-owners` and `DarwinRouter/host-resources`
  locations remain stable. Changing them would split admission between older
  and newer processes and could cause resource overcommit or unsafe recovery.
- Existing database filenames, durable tool namespaces, HTTP headers, cookie
  identifiers and cryptographic domain-separation strings remain stable. They
  are versioned protocol/storage identifiers rather than displayed branding.
- Git history, immutable benchmark evidence, previously signed release evidence,
  external upload URLs, existing issue IDs and historical comments retain their
  original identities. New release records must be generated and authorized
  for the new product and module name; old signed records must not be edited.
- Existing Linear URL slugs and DAR issue identifiers may remain as stable
  links. Workspace, team, project and maintained product descriptions use
  NexusRouter.

SDK consumers update imports to the new module path together; mixing old and
new import paths creates distinct Go types. The repository destination must be
renamed on GitHub before the new module path is advertised as downloadable.
