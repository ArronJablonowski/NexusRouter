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

## Rename an installed macOS data directory

The Skills form shows the configured store path, rather than a cosmetic label.
An older installation may therefore still show
`~/Library/Application Support/DarwinRouter/live-test/skills`.
The canonical path is
`~/Library/Application Support/NexusRouter/live-test/skills`.

From a validated checkout, run `python3 -B scripts/migrate-macos-name.py` to
inspect the migration without changing files. Before applying it, quiesce all
review dispatchers and reconcile every remote receipt. Wait for zero active
local and remote tasks/reservations, unload the old launch agent, and ensure
no other process is using the installation. Do not race an inter-wave gap.
Then run `python3 -B scripts/migrate-macos-name.py --apply`.

The utility refuses loaded services, open data files, existing destinations,
and conflicting environment aliases. It renames the entire data directory in
place, preserving skill/database inode identity; rewrites configured data
paths; installs `com.nexusrouter.live-test.plist` using `/opt/homebrew/bin/nexus`
and canonical `NEXUS_` environment names; and retains a relative `DarwinRouter`
compatibility alias. Both names thus reach the same process guards and resource
ledger, including when older binaries use the legacy paths. Do not remove this
alias or independently create a second guard directory during mixed-version use.

Verify the new configuration with the validated binary before bootstrapping the
new launch agent. Confirm the Skills form, skill inventory, database identity,
service label and zero duplicate daemons, then resume the campaign from its
existing receipts. The utility does not start or stop services itself.

Private originals are retained under `NexusRouter/name-migration-backup` with
mode0600. If interrupted, do not rerun blindly: reconcile the two directory
names and launch-agent files using these originals while services remain
unloaded. For rollback, restore the original config and launch agent, remove
only the verified compatibility symlink, rename the data directory back, and
load only the old service. Never overwrite a distinct destination or merge two
live stores. Durable database filenames and signed evidence are not rebranded.
