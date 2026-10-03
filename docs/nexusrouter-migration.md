# NexusRouter name migration

The product is now NexusRouter. The executable is `nexus`, the Go module is
`github.com/ArronJablonowski/NexusRouter`, and release archives begin with
`NexusRouter_`. The macOS operating-system identifier remains `darwin`.

## Existing installations

Existing configuration and explicit `--config` and database paths continue to
work. The offline macOS procedure below migrates the installed directory name
without copying or resetting stored tasks, feedback, skills or other evidence.
New default user configuration lookup uses
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

## Unified installation home

All owned source code, worktrees, skills, configuration, binaries and runtime
resources belong below `~/.NexusRouter`:

- `code/`: existing main checkout, preserving uncommitted work and Git history.
- `worktrees/`: associated source and validation checkouts, preserving contents.
- `skills/`: existing skill store, moved without copying or resetting it.
- `config/`: configuration and private Spark pairing credentials.
- `data/`: task databases, process guards and resource accounting.
- `bin/nexus`: verified installed executable.
- `migration-backup/`: private originals and the exact move inventory.

`python3 -B scripts/migrate-nexus-home.py` emits a path-only dry-run inventory.
The earlier Application Support/NexusRouter migration is superseded and its
command refuses application. Explicit configuration paths remain supported;
default user configuration prefers `~/.NexusRouter/config/config.yaml`, and
browser approval prefers `~/.NexusRouter/data/live-test/config.yaml`.

Before applying, pause dispatch, reconcile the current remote task and receipts,
and verify zero active tasks/reservations on both hosts. Unload both service
labels and stop validation processes using affected checkouts, preserving their
logs for restart. Then run the migration with `--apply --binary PATH`, supplying
the already validated executable selected for the rollout. The utility refuses
loaded services, open files, destination collisions, or ambiguous environment
aliases. It does not start/stop services or kill tasks itself.

Directory moves preserve data and inode identity. Legacy paths become aliases
so historical receipts, registered worktrees and shared resource guards still
resolve to the same files. Only OS-required launch-agent integration remains
outside the parent folder; its executable/config paths point inside the home.
Do not remove aliases or create independent legacy stores during compatibility.
Verify the skill inventory, database identities, Git state, new Skills UI path
and service health before resuming existing review receipts and validation.

A failure after mutation requires manual reconciliation against the private
`migration-backup/moves.json`, original configuration and launch agent. Do not
rerun blindly, merge competing stores, or rewrite signed historical evidence.
Rollback while stopped by restoring original config/service files, removing
only verified aliases, and reversing the recorded directory moves.
