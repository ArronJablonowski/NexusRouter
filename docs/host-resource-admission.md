# Host-wide local resource admission

NexusRouter daemons coordinate local model capacity through a private SQLite
WAL database that is separate from project telemetry. This prevents daemons
using different task databases from independently consuming the same configured
RAM, VRAM, device, and concurrency budget.

The default database is stored under the operating system user configuration
directory at `NexusRouter/host-resources/resource-coordinator.db`. Its immediate
directory must be private and the database must be owner-only. Controlled tests
and deployments may select an absolute path with
`NEXUS_RESOURCE_COORDINATOR_DB`. When `NEXUS_PROCESS_OWNER_DIR` is explicitly
set and no coordinator override is supplied, the daemon keeps the coordinator
beside that private owner root so isolated process groups share one authority.

Each claim binds the exact process guard, daemon generation, task and session,
provider and model profile, device, estimated RAM/VRAM, context budget, redacted
configuration digest, and lease window. Acquisition is serialized with
`BEGIN IMMEDIATE`. The process-local `resources.Budget` remains the fast first
gate; the durable claim is acquired before provider construction or streaming.

Active daemons renew claims before expiry. Losing renewal cancels primary model
execution, but capacity is not released until the provider call has returned and
the renewal worker has joined. A killed process leaves its claim fenced. A later
daemon may recover it only while holding positive `processguard` evidence
that the exact owner stopped; damaged or unverifiable evidence remains fenced
after expiry. Expired claims continue charging RAM, VRAM and concurrency until
the exact owner releases after joining execution, or stopped-owner recovery
commits. Late owner release persists cleanup but returns the expiry error so it
cannot turn lease loss into successful execution. Released and expired
reservation identities can never be replayed as fresh authorization.

Fixed and adaptive coordinators persist one exact host policy. A daemon with
incompatible concurrency, percentage, age, or adaptive settings fails closed
instead of applying a more permissive policy to the same database. To change
that policy, stop every NexusRouter daemon, preserve the database for audit if
needed, and select a new empty coordinator database deliberately.

Health reports include an identifier-free `resources/reservations` check.
Metrics add only fixed aggregate gauges for coordinator availability, active,
expired and released claims, and reserved RAM/VRAM. Byte totals and device-pool
holder counts include expired, unreleased claims; the top-level active count
continues to describe unexpired grants. Reservation IDs, task IDs,
process paths, model/provider names, devices, endpoints, configuration digests,
prompts, outputs, and credentials are never exported by those surfaces.

The coordinator governs NexusRouter processes on one host. It does not fence
unrelated inference clients or prove that an inference server actually released
physical memory. See [Managed local model residency](model-residency.md) for the
separate provider-lifecycle boundary.
