# Secure remote instance control — DAR-133

This is an opt-in version-1 control endpoint for separately paired NexusRouter
instances. It dispatches independent native tasks through the existing SDK queue
and destination dispatcher. It does not pool GPU memory, expose the ordinary
local daemon API, or automatically trust discovered devices.

The runnable entry point is `go run ./cmd/nexus-remote`. The reusable Go API is
`remote.Client`, `remote.Server`, `remote.SDKBackend`, and `remote.TrustFile`.
The destination host owns a dedicated normal runtime configuration and telemetry
store. Do not point this experimental host at a running production daemon's
store. Normal resource measurement, shared process admission, privacy, tool
policy, task lifecycle and durable runtime accounting remain active.

## Trust and pairing

An administrator exchanges instance IDs, concrete IP endpoints, CA certificates,
and SHA-256 fingerprints of leaf certificate DER over an independently trusted
channel. Issue each node a separate certificate with its server DNS SAN and the
appropriate serverAuth/clientAuth extended usages. Keep private keys on their
own node. Install only the intended CA; CA validation alone does not grant access.
Do not put certificate keys, task inputs, or runtime databases in Git.

Each node has an absolute, owner-private registry path inside a private directory.
A peer entry authorizes the certificate identity for inbound operations and the
same peer's endpoint for outbound operations. For asymmetric needs, use separate
client and server registries. A client-only node still has an explicit private
endpoint declaration; this is not a callback and the server never connects to it.
The server independently checks the actual connecting IP.

Registry schema (replace example IP, names and fingerprint before installation):

```json
{
  "version": 1,
  "peers": [{
    "id": "node-b",
    "endpoint": "https://192.168.1.20:8443",
    "server_name": "node-b",
    "pins": ["<64 lowercase hexadecimal digits: SHA-256 leaf DER>"],
    "operations": ["info", "dispatch", "inspect", "cancel"],
    "models": ["local-chat"],
    "allow_private": true,
    "allow_public_network": false,
    "allow_cloud_inference": false,
    "max_cost": 0,
    "max_context_tokens": 32768
  }]
}
```

With a prepared `next-peers.json`, initial pairing and later updates are explicit
local administration operations:

```sh
nexus-remote replace-trust --trust /private/nexus/peers.json --expected absent < next-peers.json
nexus-remote validate-trust --trust /private/nexus/peers.json
nexus-remote replace-trust --trust /private/nexus/peers.json --expected CURRENT_DIGEST < next-peers.json
```

Validation prints the current canonical registry digest. Updates take an exclusive
local lock and compare the supplied digest, validate a private staged file, sync,
rename and sync its directory. Invalid updates preserve the old policy. A crash
can leave the `.lock` directory: an administrator must establish that no writer
remains before removing it. Do not bypass the compare-and-swap operation with
concurrent manual edits. A sync failure after rename is uncertain: reread and
compare the registry rather than repeating an old mutation blindly.

TLS 1.3 authenticates both endpoints with the CA chain plus pinned leaf certificate.
Clients verify the configured server SAN. No proxy, redirect, DNS resolution,
TLS session cache, or plaintext fallback is used. Concrete public addresses need
`allow_public_network`; private requests still require private transport addresses
at both ends. IP locality is not attestation of geography or of network isolation;
operator-managed private routing/VPN and trustworthy nodes remain prerequisites.

Trust is loaded on every HTTP request, including an existing TLS connection.
Removing a peer or pin blocks subsequent requests. Rotation permits two pins for
one peer during an explicit overlap, then removal of the old pin. Pin ownership
cannot overlap between peers. Certificate validity is rechecked on reused server
connections. Listener certificate or CA rotation needs a controlled restart;
clients reload their credentials per call. Revocation does not retroactively
cancel already admitted work. Cancel owned jobs before revocation, or use the
local destination administrator's task controls afterward.

## Host and client commands

Build with `go build -o /desired/path/nexus-remote ./cmd/nexus-remote`.
Start an explicitly configured listener; no service installation or existing
listener modification happens automatically:

```sh
nexus-remote serve --instance node-b --listen 192.168.1.20:8443 \
  --trust /private/nexus/peers.json --cert /private/nexus/node.pem \
  --key /private/nexus/node-key.pem --ca /private/nexus/ca.pem \
  --journal /private/nexus/control --config /private/nexus/runtime.yaml
```

Use the existing shared process-owner directory environment when running beside
other NexusRouter processes. The host reads the explicit configuration file;
ordinary environment configuration overrides are not implicitly applied. Secret
lookup remains the normal environment-backed lookup without printing credentials.
Use dedicated runtime paths and operator-approved tool roots. Remote dispatch
can use the tools allowed by that runtime: the transport is not a tool sandbox.
Do not change the runtime configuration while this host is running; restart it
with the intended configuration. The queue's configuration digest fences drift.

All client commands require `--instance DESTINATION`, `--trust`, `--cert`, `--key`
and `--ca`. `info` returns allowed models and advisory dispatcher availability.
`dispatch`, `status`, `cancel`, and `events` also require `--request KEY`.
`events` requires `--task TASK_ID` and accepts `--after SEQUENCE` (default zero).
For dispatch, provide a saved task JSON file on stdin, rather than a prompt in
process arguments:

```json
{"version":1,"model_id":"local-chat","prompt":"Your task","domain":"coding","profile":"default","context_tokens":32768,"max_cost":0,"private":true}
```

Persist the request ID and exact task before sending. IDs have 16–64 ASCII
letters, digits, underscores or hyphens. No implicit retry or new ID generation
occurs. `private:true` requires explicit private-task permission, private network
addresses, and local inference at the destination. `private:false` requires
explicit cloud-inference permission even if a local model happens to be selected.
The SDK backend requires known, finite, nonnegative model cost estimates within
the requested ceiling, including zero. These are admission estimates, not a hard
provider billing guarantee. Destination runtime constraints still apply.

The `tasks` command lists the authenticated caller's retained request IDs and
current lifecycle state, task IDs and cancellation flag. It uses the same
`inspect` permission and works over HTTPS or SSH. Pages contain at most 100
requests in request-ID order; pass the returned `next` as `--after-request` while
`has_more` is true. The HTTP cursor is `X-Nexus-After-Request`.

This is a live traversal, not a snapshot: restart with an empty cursor to see new
requests sorting before an earlier page. It includes terminal and uncertain
requests, so callers can filter running tasks without silently hiding ambiguity.
`unknown` means the destination cannot presently resolve an owned reservation;
it is not a failure verdict or permission to dispatch again. Recover using the
original persisted request key and exact payload. Listing never submits work.
Prompts, result text, configuration digests and other callers' requests are not
returned. Use `status` and `events` for an individual owned request's details.

## Explicit external harness dispatch

An operator may configure the same pinned `native_harnesses` registrations used
by the local SDK/CLI in the destination runtime configuration. The remote host
requires `native_harness_evidence_dir` when registrations are present and owns
that ledger handle until HTTP handling and the dispatcher stop. Successful
outputs record actual execution identity; they do not receive quality credit
without bound evaluation through the existing host review path.

Both peer registries must explicitly include permitted registration IDs in the
optional `harnesses` array, in addition to the allowed `models`. Omitting this
array grants no external harness access. For example, a peer can authorize
`"harnesses":["pi-local"]` alongside `"models":["local-chat"]`. Dispatch JSON
then adds `"harness_id":"pi-local"` and
`"harness_difficulty":"hard"` (easy/medium/hard/unknown are supported). These
fields participate in the durable request digest; changing a harness or difficulty
requires a different intended request. Omitted fields preserve existing native
request serialization and retry identity.

`info.harnesses` lists only registrations authorized for that caller and attached
to its visible models. It reports ID, model ID, kind, model revision and configured
host-tool mode, without executable/source paths or credentials. This is a
configuration claim, not a live executable or tool capability attestation.
The destination rechecks registration identity, executable pins, local resource
reservation including harness overhead, privacy, context and tool policy through
the ordinary SDK dispatcher. Unknown registrations and wrong model/harness pairs
are rejected; no substitution to native completion or another harness occurs.
Automatic cross-instance model/harness selection remains unfinished and `auto`
is rejected in this explicit protocol.

The native integration fixture qualifies installed harnesses through actual OpenSSH,
pinned mTLS and the durable SDK queue with a local synthetic provider. It covers
lost-response recovery, duplicate suppression, returned identity/difficulty,
results/events, and queued/running cancellation. This is not a real-model quality
measurement or physical two-host qualification.

The opt-in all-harness matrix accepts an operator-owned JSON array of five pinned
`config.NativeHarness` registrations via `NEXUS_REMOTE_HARNESS_FIXTURES`, with
`NEXUS_REMOTE_SSH_NATIVE=1`. Run `go test -race ./remote -run
'^TestRemoteSDKAllNativeHarnessesSSH$' -v`. The fixture registrations must use
model ID `chat`, revision `fixture-v1`, and text-only mode; they identify installed
runtimes, never a production provider. The test creates disposable providers,
ledgers and SSH credentials. OpenHands SDK 1.50.1 requires at least 16,384 context
tokens; its adapter rejects smaller contexts before admission or process launch.
The other four fixtures currently use 8,192.

## Protocol and durability

| Method and path | Scope | Result |
| --- | --- | --- |
| GET `/v1/remote/info` | info | versioned allowed model catalog and availability |
| GET `/v1/remote/tasks` | inspect | paginated caller-owned lifecycle metadata |
| POST `/v1/remote/tasks/{key}` | dispatch | durable submission status; JSON task body |
| GET `/v1/remote/tasks/{key}` | inspect | submission state and completed result |
| POST `/v1/remote/tasks/{key}/cancel` | cancel | durable cancellation request/status |
| GET `/v1/remote/tasks/{key}/events` | inspect | committed event page for an owned task |

Every request names the destination using `X-Nexus-Instance`; it is checked against
the listener identity and echoed on responses. This header never establishes
caller identity. Events use `X-Nexus-Task` and `X-Nexus-After`. Request bodies are
bounded at 1 MiB; results/event pages at 8 MiB; event pages at 100 events. Requests,
connections, headers and backend deadlines are bounded. There are 32 simultaneous
HTTP operation slots. This is not a multi-tenant billing or rate-quota system.

Caller identity comes from the authenticated certificate pin. Task keys and event
access are scoped to that caller. Caller-scoped task inventory is available; there is no remote database-wide task listing,
filesystem endpoint, runtime configuration mutation, or cross-caller cancellation.
`info` retains configured capabilities and adds fresh advisory observations.
The shipped SDK backend filters the paired model/cloud scope before probing
provider inventories, so callers cannot trigger unrelated provider lookups.
Each distinct eligible provider/locality policy is queried once per request;
no result is cached across requests. A model observation is `present`, `absent`
or `unknown`, with `checked_at`. Credentials missing, rejected policy, or a failed
provider query yield unknown, not absence or available capacity. Inventories do
not attest model digests, native tool support or configured capabilities.

Provider probes have two-second limits inside a ten-second observation budget.
The response also includes a fresh RAM observation (`measured` or `unknown`),
using the normal host profiler with a two-second context. Missing measurements
stay absent. Available RAM is a snapshot, not unreserved capacity or a promise of
admission; no model is loaded/unloaded and no inference runs for `info`.
`available` continues to describe dispatcher health only. Dispatch independently
checks live admission. Resource and model observations older than 15 seconds,
future observations and inconsistent capacity values fail closed. Custom backends
can implement `InfoFor(ctx, allowedModels, allowCloud)` to scope discovery before
network effects; the server still filters returned models independently.

The private control journal stores caller/key, canonical task hash, submission
binding, and append-only action/outcome metadata. It stores no prompt or result.
The existing runtime store retains normal task evidence. A journal identity is
bound to one destination. Keep both stores and the stable node identity together;
never delete/reset them to recover an uncertain dispatch. Backups must be SQLite
consistent. Audit growth and retention need operator disk monitoring; this first
version does not automatically prune replay identities or audit history.

A request is audited before dispatch. The canonical task hash is reserved before
SDK submission; the SDK key derives from destination, caller and request ID.
The SDK's durable idempotency makes retry safe even if the process loses the
response before recording its submission binding. A changed payload with the
same key conflicts. After an uncertain response, retry only the original key and
payload, then inspect its status. Never generate a replacement key merely because
a connection was lost. Cancellation acknowledgement is not proof that external
side effects have stopped. Running cleanup remains cooperative and uncertain
effects are not replayed.

HTTP 400 denotes invalid input, 403 denied access, 409 identity conflict, and 503
unavailable or uncertain delivery. TLS failures can also make delivery unknown.
A failed audit prevents new dispatch; failed outcome persistence suppresses a
success response even when the mutation already committed. Audit `succeeded`
means the control operation succeeded, not that inference passed. Runtime status
is authoritative for task outcomes. Unauthenticated TLS failures do not get a
caller-owned durable audit record. Inputs, credentials and raw backend errors
are not included in transport error responses.

## Verification and remaining acceptance

Race-tested loopback fixtures cover mutual authentication, missing client certs,
pin mismatch, scope/privacy rejection, live revocation on a reused TLS connection,
rotation overlap and old-key removal, ownership isolation, duplicate concurrent
requests, changed-payload conflict, lost binding/reopen recovery, atomic trust
updates, cost ceiling enforcement, and fail-closed audit persistence. Real SDK
integration with a local fixture provider covers queued and running cancellation,
committed progress, results and duplicate suppression (one executed first task,
one separately canceled running task; the queued canceled task never executes).

This is a first implementation, not completed DAR-133 qualification. Remaining:
physical two-system/network-fault tests; cross-platform certificate/storage and
service packaging; device discovery/pairing UI; native-harness capability attestations;
caller inventory UI; automatic remote destination selection integrated with
DAR-132's versioned, accuracy-first model–harness evidence; and operational
retention and deployment-wide abuse policy. The endpoint currently takes an explicit model and peer.
It does not choose a winner based on idle capacity or claim remote evidence is
integrated into the joint ranker. A full repository check is required before push.

NVIDIA PAIR is product inspiration for explicit pairing and separate-task private
compute routing. Its product page does not establish this protocol's security or
provide acceptance evidence for NexusRouter.

## SSH transport

SSH is selectable per peer with `"transport":"ssh"`; omit the field or use
`"https"` for a direct connection. Add this object to the otherwise unchanged
peer entry:

```json
"transport": "ssh",
"ssh": {
  "user": "nexus",
  "port": 22,
  "identity_file": "/private/nexus/ssh_identity",
  "known_hosts_file": "/private/nexus/known_hosts"
}
```

The endpoint remains the HTTPS task-control URL. Its concrete IP identifies the
SSH host as well; its port identifies the destination task endpoint. OpenSSH
`-W` opens a direct-tcpip stream to that same IP and endpoint port from the remote
host, and the client performs the normal pinned mutual TLS handshake inside it.
For example, `https://192.168.1.20:8443` with SSH port 22 connects to SSH on
192.168.1.20:22 and forwards to 192.168.1.20:8443. The endpoint must listen on that
address. Arbitrary jump hosts, shell commands and remote installation are not
part of this transport. Existing dispatch/status/events/cancel CLI commands work
unchanged. TLS credentials and paired permissions are still required.

The installed OpenSSH executable must be available on PATH. Identity and known
hosts files must be regular owner-private files. Paths must be literal absolute
paths without whitespace, quotes, percent/dollar expansion, tilde or backslash.
Populate known_hosts from independently verified host keys; this implementation
never performs trust-on-first-use or learns changed keys automatically. Rotate
SSH keys through the remote account's authorized_keys and update the dedicated
known_hosts file through normal administrator controls. TLS peer revocation is
independent and continues to block task access through an existing SSH account.

The client disables user/system SSH configuration, agent use and forwarding,
password/interactive authentication, connection multiplexing, proxies, X11 and
local commands. It enables strict host-key checking and uses one bounded
noninteractive connection per operation. There is no automatic direct-HTTPS
fallback after SSH authentication, host-key or connection failure. Requests keep
the same durable caller key across transport changes and uncertain delivery.
Subprocess pipes implement deadlines and cancellation; closing the transport
terminates and reaps its SSH child. No SSH account or system service is modified.

Use a dedicated remote account/key with forwarding restricted to the NexusRouter
endpoint (`AllowTcpForwarding local` and exact `PermitOpen` in sshd policy), and
no agent/X11/TTY or general command authority. Bind/firewall SSH to the intended
private network for private tasks. A tunnel changes the source IP seen by the
inner TLS server; that address cannot prove the original SSH client's network
location. Both nodes and the SSH account/network policy are trusted deployment
components, not an untrusted multi-tenant sandbox.

Qualification: `NEXUS_REMOTE_SSH_NATIVE=1 go test -race ./remote -run
'^TestSSHNativeLoopback$' -v` starts an isolated loopback sshd with disposable
keys and uses the installed real OpenSSH client. It verifies dispatch, duplicate
suppression, cancellation, wrong-host-key refusal, unauthorized-client-key refusal
and no direct fallback. It does not change the system SSH daemon. The ordinary
suite additionally checks effective OpenSSH configuration and a subprocess stream
fixture with router revocation. Native loopback qualification passed on this Mac;
separate-machine, network interruption and cross-platform qualification remain.

OpenSSH behavior reference: [ssh(1)](https://man.openbsd.org/ssh.1) and
[ssh_config(5)](https://man.openbsd.org/ssh_config).

### Interrupted SSH responses

Native loopback qualification also covers a real destination TLS socket cut
inside the OpenSSH tunnel after dispatch intake commits but before response
headers arrive. The fixture independently checks the persisted submission ID
and queued state, then retries the original key/payload and receives that same
submission. A second cut after queued cancellation commits is recovered through
status and an idempotent cancellation retry. The real SDK dispatcher then proves
result/event delivery and running cancellation over SSH: one successful fixture
execution, one separately canceled running execution, and zero execution for the
queued canceled task. These are controlled transport failures using disposable
sshd keys on one Mac, not physical two-host, packet-loss/partition or cross-platform
qualification. Run with `NEXUS_REMOTE_SSH_NATIVE=1 go test -race ./remote`.


## Per-peer request limits

The destination enforces separate one-minute allowances for each authenticated
peer and operation: 60 info, 60 dispatch, 600 inspect and 120 cancel requests by
default. Status, event and task-list reads share inspect. Override all four in the
inbound peer entry with `"request_limits":{"info":60,"dispatch":60,"inspect":600,"cancel":120}`.
Each value must be 1–10000; omission uses defaults, not unlimited access. Updates
use the normal expected-digest trust replacement and apply on the next request
without resetting consumed counts. The client-side registry does not impose the
destination's allowance. Permissions and revocation remain independently enforced.

An exhausted operation returns HTTP 429 with integer-seconds `Retry-After` and the
Go client returns `remote.ErrRateLimited`. No backend call or durable submission
reservation occurs. Retry later with the same request key and payload; never
switch destinations merely because a response was lost or throttled. Clients do
not automatically retry. Cancellation uses its own allowance, so exhausting
dispatch or discovery does not consume cancellation's budget; the existing global
32-request concurrency bound still applies and is not a reserved cancellation lane.

These are fixed windows starting at each operation's first request, with an
allowance-sized boundary burst possible. Counters are process-local and reset on
restart; they are not durable billing quotas or cross-listener rate coordination.
Removed peers are pruned when requests are processed. Certificate rotation under
the same peer ID does not reset consumption. The first throttled request per peer,
operation and window is durably audited as `rate_limited`; subsequent rejections
in that window are not individually journaled, to bound amplification. An audit
write failure returns 503 and still never calls the backend. Recognized requests
consume allowance before scope checks, including denied requests. TLS failures and
unknown endpoints do not allocate per-peer buckets. This complements resource
admission; journal retention, perimeter protection and physical-host validation
remain separate requirements.
