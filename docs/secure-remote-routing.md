# Secure remote instance control — DAR-133

This is an opt-in version-1 control endpoint for separately paired NexusRouter
instances. It dispatches independent native tasks through the existing SDK queue
and destination dispatcher. It does not pool GPU memory, expose the ordinary
local daemon API, or automatically trust discovered devices.

The main release binary exposes `nexus remote` (from source: `go run ./cmd/nexus remote`).
The standalone `nexus-remote` command remains a compatibility entry point using the
same `internal/remotecli` implementation. The reusable Go API is
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
nexus remote replace-trust --trust /private/nexus/peers.json --expected absent < next-peers.json
nexus remote validate-trust --trust /private/nexus/peers.json
nexus remote replace-trust --trust /private/nexus/peers.json --expected CURRENT_DIGEST < next-peers.json
```

Validation prints the current canonical registry digest. Updates take an exclusive
local lock and compare the supplied digest, validate a private staged file, sync,
rename and sync its directory. Invalid updates preserve the old policy. A crash
can leave the `.lock` directory: an administrator must establish that no writer
remains before removing it. Do not bypass the compare-and-swap operation with
concurrent manual edits. A sync failure after rename is uncertain: reread and
compare the registry rather than repeating an old mutation blindly.

For changes to one member, the local membership commands preserve all other
entries and their scopes:

```sh
nexus remote peers --trust /private/nexus/peers.json
nexus remote pair --trust /private/nexus/peers.json --expected CURRENT_DIGEST < verified-peer.json
nexus remote revoke --trust /private/nexus/peers.json --instance node-b --expected CURRENT_DIGEST
```

`peers` returns the configured registry and its canonical digest; it does not
probe availability. `pair` accepts one `Peer` object using the schema above,
without the `version`/`peers` envelope. Use `--expected absent` only to create a
new registry. Verify the certificate fingerprint, endpoint, SSH host key where
applicable, and requested permissions through a trusted channel first. This is
an offline registration command, not a certificate-exchange handshake or a
trust-on-first-use discovery operation. Neither peer discovery nor SSH access
establishes NexusRouter authority by itself.

Adding an existing ID, sharing another member's certificate pin, removing an
unknown ID, or using a stale digest fails without changing the registry. For
intentional certificate rotation or scope changes, use `replace-trust` with the
current digest. Both pairing and whole-registry replacement reject unknown JSON
fields, oversized input and trailing values. Successful mutations print the new
digest. A write may succeed even if printing it fails; reread with `peers` before
retrying an uncertain operation. Revocation blocks subsequent authenticated
requests but does not cancel already admitted jobs. These commands are local
administrator operations and do not expose a network membership-management API.

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

Build the main entry point with `go build -o /desired/path/nexus ./cmd/nexus`.
For the compatibility executable, use `go build -o /desired/path/nexus-remote ./cmd/nexus-remote`.
Existing release archives build `cmd/nexus`, so remote commands are included in
the same executable and generated dependency closure. No extra network listener
starts unless the operator explicitly runs `nexus remote serve`. OpenSSH remains
an external runtime prerequisite for SSH transport. Packaging/native service
installation and physical two-host qualification remain distinct checks.
Start an explicitly configured listener; no service installation or existing
listener modification happens automatically:

```sh
nexus remote serve --instance node-b --listen 192.168.1.20:8443 \
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
binding, and action/outcome metadata (append-only during task operations). It stores no prompt or result.
The existing runtime store retains normal task evidence. A journal identity is
bound to one destination. Keep both stores and the stable node identity together;
never delete/reset them to recover an uncertain dispatch. Backups must be SQLite
consistent. Audit growth needs operator disk monitoring. Explicit archive-then-prune
maintenance is described below; request replay identities never expire automatically.

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

## Exact configured harness identity preview

`nexus remote harness-identity --instance NODE --model MODEL --harness REGISTRATION
--context 32768` (with the usual trust/certificate flags) requests a configured
identity for that exact model/registration/context. The Go client exposes
`HarnessIdentity`; the transport is `GET /v1/remote/harness-identity` with
`X-Nexus-Model`, `X-Nexus-Harness` and `X-Nexus-Context` headers. Both HTTPS and SSH
use the same request path and checks. No prompt is sent.

The existing `info` permission and rate budget apply. Both peer registries must
allow the model and harness, and the context must fit the peer ceiling. The SDK
backend also verifies its configured registration/model pair, cloud-discovery
scope and model context bound before deriving identity. Unsupported contexts or
inconsistent backend identity fail closed. Responses bind the instance, request,
exact versioned harness identity and fresh computation timestamp. They disclose
no executable/source paths, endpoint URL, credentials, tool bodies or outputs.

The host derives identity using the same effective configuration constructor as
execution, including context, output limit, deadline, adapter/model revision,
transport and native tool policy. This performs no provider query, executable
launch, secret lookup, task reservation or inference. It is a configuration
preview: it does not verify installed artifact bytes, model residency, capacity
or future admission. Discovery availability is separate. Actual completed
execution identity must be checked before accepting learning evidence. This
preview is a prerequisite for automatic remote routing; it does not yet rank
nodes, authenticate imported quality scores or persist a chosen destination.

### Pinning the previewed execution identity

An external-harness dispatch may include `expected_harness_identity` containing
the complete `identity` object returned by `harness-identity`. Keep the same model,
registration and explicit context. The optional field is bound to the remote
request digest and SDK durable submission payload; changing it under a reused
request key conflicts. Omitting it preserves the previous remote payload encoding.
Native routing without an external harness and automatic harness selection cannot
carry this pin.

The normal SDK admission derives the effective identity and rejects mismatches
before queue admission. Execution compares it again before the native harness is
run. This prevents a stale preview from silently executing another configuration;
it is still not proof of model residency or eventual success. Pin failures do not
authorize rerouting an ambiguously delivered request to another instance.

Durable submission contract generation advances to 7. Existing queued intents
from earlier generations are fenced by the usual configuration-change handling;
they are not silently reinterpreted or replayed. Account for this boundary before
upgrading a running host. No live runtime or queue was upgraded by these changes.

## Durable caller destination binding

Use `dispatch --routes /private/nexus/caller-routes` with the usual flags and
exact task JSON, or Go `Client.DispatchRecorded`, to persist the chosen destination
before any dispatch request. The route directory's parent must already exist.
`route-binding --routes /private/nexus/caller-routes --request KEY` reads the saved
choice locally without remote credentials. A binding contains only the version,
request ID, destination ID, caller leaf-certificate fingerprint and task SHA-256;
retain the exact task separately under the caller's normal privacy policy.

The store creates private immutable files using synced temporary content and an
atomic no-replace hard link, then syncs the directory before sending. Concurrent
processes can agree on one identical binding; different destinations, task content
(including any expected harness identity), or caller certificates conflict.
Opening the store syncs its parent directory as well. Corrupt, symlinked or
permissive records fail closed. Filesystems that cannot provide the required link
and sync operations fail before dispatch; physical power-loss and cross-platform
filesystem durability have not been qualified.

After an uncertain response, reopen the store and retry the same destination,
request, task and caller certificate. It will recover the destination's existing
submission rather than select a new node. Revocation and current peer scopes are
still checked for every network request. A certificate change between recording
and TLS setup is rejected before sending. Credential rotation deliberately does
not rewrite saved bindings: use current credentials to inspect/cancel the owned
remote request, and reconcile its state before planning further work. Do not
remove a binding, change directories, or generate a new key merely to bypass an
uncertain submission. There is no automatic deletion or expiry; retention needs
an explicit completed-work reconciliation policy. Bindings prove saved intent,
not that the destination received or completed the task.

Ordinary `dispatch` remains available for callers already providing equivalent
durability. This store does not select a destination; it is the prerequisite for
persisting an automatic selection before execution. Cross-instance evidence and
accuracy ranking remain separate work.

## Reconcile completed remote harness evidence

`nexus remote reconcile --routes /private/nexus/caller-routes --evidence
/private/nexus/remote-evidence --request KEY` takes the saved exact task JSON on
stdin and the usual trust/certificate flags. The saved route selects the
instance; an optional `--instance` must agree. The task must have pinned
`expected_harness_identity`. Go callers use `Client.RecordedOutcome` followed by
`VerifiedOutcome.Record`.

This path only reads authenticated status and canonical event pages. Each read
checks current paired trust and the original caller certificate. A succeeded
single-task result must match a complete, bounded, stable-head native harness
journal, actual identity, task class, context, submission ID, exact user prompt,
private locality when required, and output hash. Failed, running, incomplete,
altered or mismatched evidence is rejected. Redacted input that cannot be matched
to the saved prompt is not silently accepted. No inference is repeated.

Recording first publishes a private immutable receipt binding the saved route,
submission, event hash and execution. It then appends that execution to a separate
ledger for the destination/caller pair. Identical retries recover partial writes;
changed receipts conflict. No prompt or result text is copied into these stores.
No quality review or vote is created: content still needs a bound evaluation.
Remote-advertised quality scores are never imported through this API.

Use one stable paired instance ID for one runtime and preserve its canonical
history. Reusing an ID for another system is not supported. Caller certificate
rotation deliberately isolates ledgers; historical evidence is not automatically
merged. The paired runtime remains trusted to report its own canonical events;
these checks do not attest a compromised destination's operating system or model
weights. No automatic retention, cross-instance ranking, or automatic review is
provided by this reconciliation step.

### Bound operator/evaluator reviews

`reconcile` returns `receipt`, `receipt_sha256` and `execution_sha256`. Preserve that receipt and
review the exact remote result it identifies. An authorized local operator can
then use `nexus remote review --routes /private/nexus/caller-routes --evidence
/private/nexus/remote-evidence --request KEY --review /private/nexus/review.json`
with the usual trust/certificate flags and the exact saved task JSON on stdin.
The optional `--instance` must match the saved destination. The review file must
be an absolute regular owner-private file, at most 32 KiB; symlinks, unknown JSON
fields and trailing values are rejected.

The file contains `version:1`, the exact `receipt_sha256` from reconciliation,
and `review`, the existing `harness.Review` JSON object. Its `ExecutionDigest`
is the returned `execution_sha256` (Go callers use `receipt.Execution.Digest()`). `ID` is an immutable unique review ID; `ExpectedHead` is empty for the
first review and names the exact current review ID for a revision. Preserve the
original file for identical retries. Supply `Verdict` (passed/failed), `Method`
(deterministic/human/automated_ai), versioned rubric in `MethodVersion`, truthful
`Reviewer`, `Confidence`, `Quality`, and UTC `CreatedAt`. A withdrawal uses
`Verdict:withdrawn`, the current head, empty method/version and zero scores.

Go hosts call `Client.ReviewRecordedOutcome` with `OutcomeReview`. Before this
operator/evaluator-only call the embedding host must authenticate the reviewer
and evaluation method; a JSON label is not proof of authorship. This is not a
remote HTTP endpoint or model tool. It does not run a judge, invent scores or
import a destination's advertised quality totals.

Every call freshly authenticates the saved peer and caller certificate, rechecks
its canonical completed events and output, and requires both the receipt and
execution digests before writing any evidence. The receipt binds destination,
caller, request and event history even if another system reports an identical
execution. The destination-specific ledger applies atomic expected-head updates.
Concurrent conflicting revisions have one winner; stale revisions fail. Replaying
an identical old review after a newer revision does not restore the old head or
add another vote. Failed/partial lineages cannot be reviewed through this path.

AI judgments remain advisory under the existing ranker's capped weighting; they
are not counted as confirmed deterministic or human samples. Reviews remain
local to the caller's evidence store and do not modify the destination journal.
Automatic evaluation scheduling and cross-instance selection remain unfinished.

## Destination-specific accuracy ranking

Go hosts can call `Client.RankRecordedCandidates` with the private remote evidence
root, a `harness.Request`, ranking policy, proposed `DestinationCandidate` values,
and a uniform draw used only for explicitly authorized evaluation exploration.
The result identifies the chosen destination, configured model/registration,
caller fingerprint, exact execution identity, scores, sample counts and excluded
candidates. This is an advisory decision; it neither dispatches nor changes an
existing saved request's destination.

The embedding host supplies candidates only after its own fresh capability,
resource, locality and credential checks. Raw free RAM from discovery does not
establish admission headroom. The method independently filters current paired
model/harness/privacy/budget scope, requires info and dispatch permissions, and
fetches the exact configured identity over the selected HTTPS or SSH transport.
A failed observation is ineligible, and configuration drift is incompatible.
Actual admission and executable/model checks still occur at the destination.
Caller certificate changes during observation reject the selection.

Quality comes only from the caller-owned ledger for that destination and caller
fingerprint. Existing ledgers are opened read-only; missing evidence stays unknown
without creating a database, and corrupt existing evidence fails closed. No
remote-advertised score is accepted. The same exact model/harness identity on two
systems remains two candidates, with separate evidence; the same identity listed
twice for one destination is rejected instead of double-counted. Task profile,
difficulty, context/configuration and current review heads retain their ordinary
isolation and weighting, including capped advisory AI judgments.

`harness.SelectScoped` reuses the ordinary selector's eligibility and scoring,
then compares correctness, quality and confidence in that order. Cost remains a
budget gate. Ties use a deterministic destination/identity key; insufficient
evidence does not establish a comparative winner. Ordinary routing never explores.
Explicitly budgeted exploration retains the existing capped probability and
least-observed eligible alternative rule.

Before executing a new choice, the host must recheck admission, pin the selected
identity into the task and persist the destination through `DispatchRecorded`.
For an already bound request, recover its saved destination rather than ranking
again. Automatic discovery-to-candidate admission, persisted automatic dispatch,
UI integration and physical two-host qualification remain unfinished.

## Durable automatic dispatch

Go hosts can submit a new `AutomaticRequest` with `Client.DispatchAutomatic`.
The request contains the prompt and ordinary `harness.Request` routing constraints;
the host supplies freshly checked destination candidates, ranking policy and an
exploration draw. Save the exact request before calling. Remote task privacy is
`Routing.LocalRequired`, so private tasks must set it explicitly. The destination
retains its normal admission, executable, context, cost and tool checks.

For a new request key, the client ranks from destination-specific evidence and
fresh identities, then atomically saves an owner-private `.choice.json` before
any dispatch. It retains the intent digest, destination, caller fingerprint,
configured model/harness, exact identity, selected score/counts/reason, exploration
flag, selection digest and selection timestamp. It stores no prompt. The ordinary
route binding is then synced before the pinned request is sent. The result
includes the saved choice even if delivery is uncertain. `RouteStore.AutomaticChoice`
reads it and `AutomaticChoice.Task(originalRequest)` reconstructs the exact task
for status reconciliation and content review.

On restart or retry, an existing choice is recovered without ranking or querying
alternate candidates. Current policy, candidates and draws cannot replace it.
Changed prompts/constraints or caller certificates conflict; corruption, unsafe
files and a manually bound request without an automatic choice also fail closed.
Current paired dispatch permissions and caller credentials are still checked
before sending. A failed admission or lost response does not trigger fallback.
Concurrent first callers can propose different nodes, but only the atomically
published winning choice may dispatch; another intent cannot reuse its key.

A crash after choice publication but before route binding is recoverable using
the same request. Sync failures are uncertain and preserve files; retry the same
key and saved intent rather than deleting records. Physical power-loss and
cross-platform filesystem qualification remain outstanding. This Go entry point
connects the ranker to durable submission; it does not implement automatic
capability/capacity collection, background evaluation scheduling, pairing UI or
CLI input from untrusted candidate claims. Those integration requirements remain.

## Measured harness capacity

`nexus remote harness-capacity --model MODEL --harness REGISTRATION --context
TOKENS` uses the normal destination/trust/certificate flags over HTTPS or SSH.
The Go method is `Client.HarnessCapacity`; the endpoint is
`GET /v1/remote/harness-capacity` with the identity-preview headers. Both paired
registries enforce model/harness/context scope, and the destination applies cloud
permission before measurement. It shares the bounded info allowance and audit.

The response binds instance, request, exact configured identity, resource need
and a fresh capacity result. The host derives context-adjusted model RAM/VRAM
plus fixed harness overhead using its ordinary reservation calculation. A cloud
model contributes only its local harness process overhead. The capacity planner
uses the same dispatcher Service, profiler, hard limits and live in-process
reservations. It performs no reservation, provider construction, secret lookup,
inference, model load/unload or runtime-store write. The SDK backend limits the
observation to two seconds. Invalid, stale or internally inconsistent results
fail closed; a valid pressure result reports wait rather than admission.

`RankRecordedCandidates` now requires this capacity observation as well as exact
identity. Its prior host-supplied capacity flag remains a veto, but a true flag
cannot override a measured wait or unavailable observation. Older/custom backends
without the new capacity method are excluded from automatic ranking; explicit
dispatch retains ordinary destination admission. This is an advisory snapshot,
not a reservation or proof of executable/model capability, provider credentials,
or all other processes' future usage. Shared process admission and all normal
execution checks still apply when dispatch actually runs. Native capability and
credential collection, evaluation scheduling and UI integration remain open.

### Fresh harness prerequisites

`nexus remote harness-readiness --model MODEL --harness REGISTRATION --context
TOKENS` observes the configured executable SHA-256, credential presence, and
provider model inventory through HTTPS or SSH. Paired model/harness and cloud
permissions are checked before file, secret or provider access. The SDK callback
gets a two-second deadline; responses expire after fifteen seconds. No credential,
installation path, prompt, provider error or quality claim is returned.

A successful inventory distinguishes present from absent; failed discovery is
unknown. Missing credentials, incompatible configuration or a changed executable
skip discovery. Observation does not execute the harness or inference, reserve
capacity, or create evidence. Executable reads are capped at 256 MiB. Installation
files and secret callbacks are trusted host inputs. This does not attest package
dependencies, runtime version, model weights or actual tool support; execution
still verifies its runtime contract.

Automatic ranking requires both measured capacity and fresh readiness. Negative
host gates remain vetoes; positive flags cannot bypass an absent/unknown model,
changed executable, missing credential or incompatible identity. Capabilities and
context are intersected with observed configuration, and cost uses the greater
estimate. Hosts without this endpoint are excluded from automatic selection.
Explicit dispatch still performs normal admission. These observations are advisory:
credentials, files and provider availability can change before execution.

### Discovering candidates and automatic CLI dispatch

`catalogue` returns paired model/harness configuration without provider inventory
or resource probes. `candidates` enumerates only the caller's trust registry and
returns observed candidates plus discovery exclusions. Each eligible registration
gets fresh readiness and measured capacity. Discovery has a thirty-second total
context and at most 4,096 registrations; exhausted time or changed caller/trust
configuration aborts the result. No discovery adds a peer, grants permission,
starts inference or imports remote quality scores. Readiness/capacity can change,
so `rank` and dispatch recheck them against current caller-owned outcome evidence.

`candidates` and `rank` read a `harness.Request` JSON object from stdin, for example:

```json
{"Version":1,"Task":{"Domain":"coding","Profile":"benchmark-v1","Difficulty":"hard"},"Mode":"local_only","LocalRequired":true,"ContextTokens":32768,"MaxCost":0,"Capabilities":["chat"]}
```

`auto-dispatch` reads `{"Version":1,"Prompt":"...","Routing":{...}}`, where
`Routing` is that same request. Supply the usual `--trust`, `--cert`, `--key` and
`--ca`, plus `--routes`, `--evidence`, and a persisted `--request` key. Do not supply
`--instance`, `--model`, `--harness` or `--context`: selection uses the routing
request and paired catalogue. CLI automatic operations reject `AllowExploration`;
ordinary accuracy-first selection uses the default policy. Explicit evaluator
workflows can use the Go API's bounded policy and draw.

Save the original JSON and caller key before the first command. On uncertain
transport or output failure, rerun the exact same request/key. An existing durable
choice is recovered before any catalogue query, even if discovery is unavailable;
there is no automatic fallback to another destination. `automatic-choice --routes
DIR --request KEY` reads the stored choice without network traffic or exposing
the prompt. The choice's Go `Task(originalRequest)` reconstructs the exact pinned
payload for existing reconciliation/review operations. UI and background evaluator
integration remain separate work.

### Following and reviewing an automatic request

The `auto-status`, `auto-cancel`, `auto-output`, `auto-reconcile` and `auto-review`
commands read the original `AutomaticRequest` JSON from stdin. Supply the same
`--routes` and `--request` plus normal trust/TLS flags; omit destination/model/
harness/context overrides. They verify the saved choice against the ordinary route
binding and the exact original request. No command discovers candidates, dispatches
inference or repairs a missing binding. Missing/inconsistent records fail; recover
an interrupted first dispatch with the original `auto-dispatch` request instead.

Status and cancellation authenticate as the original bound caller. `auto-output`
returns text only after canonical completed events, intent, attribution and output
hash have been checked, alongside receipt/execution hashes for an evaluator.
Treat this stdout as sensitive task content. `auto-reconcile --evidence DIR`
records the immutable receipt and execution, not a quality verdict; its result
contains hashes/receipt without output text. Result text is held only in memory
by the verified outcome object and is not added to the evidence store.

After evaluating that exact output, `auto-review --evidence DIR --review FILE`
applies an operator-supplied bound review using the existing private-file rules.
It reauthenticates the destination and canonical completion before writing.
Identical review retries count once, revisions require the current expected head,
and automated AI reviews remain advisory. Neither reconciliation nor successful
execution supplies a pass automatically. Reviewer authentication remains the
embedding host/operator's responsibility. The Go equivalents are ResolveAutomatic,
AutomaticStatus, CancelAutomatic, AutomaticOutcome and ReviewAutomaticOutcome.

`nexus remote auto-review-state --routes DIR --request KEY --evidence DIR` reads
the original automatic request from stdin and authenticates its saved destination
and canonical completion before inspecting local evidence. It returns receipt and
execution hashes, whether the execution has been recorded, and a copy of its
current review head. Missing evidence remains unrecorded without creating a ledger.
Existing ledgers are opened read-only and their full logs are validated.

The classification distinguishes pending, unverified, withdrawn, AI advisory and
confirmed reviews; the generic harness snapshot API also identifies non-quality
execution lineages. It contains metadata and hashes, not output text. Use the
returned head ID as `ExpectedHead` when deliberately revising a review. A concurrent
change still causes a conflict at write time: inspection is not a lock or permission
to overwrite feedback. This command performs no content judging or evaluator
inference and does not turn a pending output into a pass.

### Local control-audit inspection

Administrators can inspect the destination's existing private control journal:

```sh
nexus remote audit --journal /private/nexus/control --instance node-a
nexus remote audit --journal /private/nexus/control --instance node-a \
  --after 100 --through 205
```

The first call returns at most 100 entries, `through` (the journal's current
maximum audit sequence), and `next` when more entries remain. Continue with
`--after` set to `next` and the same `--through`; stop when `next` is absent.
Start a new scan to include later events. Sequence numbers need not be contiguous.
The example numbers above must be replaced with values from the actual response.
Each page is a consistent SQLite read transaction. Concurrent appends do not
extend the selected prefix; this assumes the operator retains the same journal
and does not replace or manually edit its history during the scan.

This command uses local filesystem authority, requires the exact instance ID,
and opens SQLite read-only/query-only. It requires no peer credentials, runtime
configuration, dispatcher or network connection. Missing journals are errors and
are not created. Unsafe file permissions, symlinks, wrong journal identity,
invalid cursors and malformed/oversized returned records fail the operation.
The output includes timestamps, caller, destination, operation, request ID and
recorded outcome; it contains no prompt/result text and is not a quality verdict.
These metadata can still be sensitive: protect any redirected exports.

Inspection neither deletes audit records nor expires durable request identities.
Inspection itself does not implement archive verification, automatic retention,
compaction or an authenticated remote audit endpoint. Operators must continue monitoring disk
usage; deleting replay identities to reclaim space can duplicate previously
accepted work and is not a supported retention procedure.

### Explicit audit retention with verified archives

Use the local administrator commands to move an old audit prefix into a private
archive. These commands do not run on a timer and never run from peer requests.
The archive directory must already exist with owner-private permissions.

```sh
nexus remote audit-archive --journal /private/nexus/control --instance node-a \
  --archive /private/nexus/archives/control-batch-001.json
# Inspect the archive receipt and protect/back up the saved file, then use its hash:
nexus remote audit-prune --journal /private/nexus/control --instance node-a \
  --archive /private/nexus/archives/control-batch-001.json --expected ARCHIVE_SHA256
```

Archive creation takes a consistent database snapshot and exports the oldest at
most 10,000 currently stored audit entries. Optional `--through` selects an exact
existing upper sequence, subject to the same row limit. The JSON is canonical,
limited to 32 MiB, written with private permissions, synced, and published without
overwriting a different existing file. No rows are removed during export. Save
its returned `sha256`, instance, entry count and upper sequence with the archive.
Repeated export to the same file succeeds only when the exact snapshot matches;
use its original `--through` if newer events have arrived.

Pruning requires the exact archive hash, a valid private file, matching instance,
and an exact comparison of every currently stored row in the selected prefix.
An omitted/changed row, stale conflicting export, malformed file or unavailable
archive aborts before deletion. A transaction removes only that audit prefix and
appends an `audit_prune` marker containing the archive SHA-256. Failure to write
the marker rolls back the deletion. Concurrent new rows above the prefix remain.
An unchanged archive can recover a lost successful response while its marker is
still in the live journal; `already_applied` reports that case. If a later prune
has archived that marker too, the old retry fails closed. Inspect the retained
archive chain instead of reconstructing or resetting the journal.

Request IDs, caller ownership, task hashes, submission bindings, runtime task
records and model/harness learning evidence are never pruned by these commands.
Removed SQLite pages become reusable; the file is not vacuumed or guaranteed to
shrink, and retained request identities still require disk planning. Coordinate
maintenance with paginated audit readers: pruning during their multi-page scan
can remove rows they have not read. Archive creation uses a single transaction
and does not have that multi-page exposure.

Keep all archives, including earlier batches referenced by later prune markers.
The hash binds exact bytes for maintenance; it is not a digital signature,
external attestation or protection against an administrator rewriting both the
database and archive. The operator remains responsible for backup durability,
access permissions, archive lifetime and applicable retention requirements.
No live journal is pruned merely by upgrading or starting NexusRouter.

### Durable caller-side evaluator attempts

Embedding hosts can call `Client.EvaluateRecordedOutcome` with the original
request and a trusted `RemoteEvaluator`, or `EvaluateAutomaticOutcome` with the
original automatic request. Both authenticate the saved destination, canonical
completion, actual model/harness identity and exact output before evaluation.
They never dispatch task inference, run discovery or replace a saved route.

The evaluator uses the public `evaluation.Evaluator` interface. The host must
supply a fixed descriptor and enforce the evaluator's resource, cost, credential,
network and secret-handling policies. `Local` is trusted host configuration, not
a claim accepted from a peer or model; private tasks reject nonlocal evaluators.
The invocation is bounded to at most five minutes with cooperative cancellation.
An in-process extension is trusted host code, not an isolation boundary.

One attempt is durably claimed per receipt under the existing private,
caller/destination-separated evidence root. Admission binds the exact input hash,
evaluator descriptor, locality and timeout before invoking the evaluator. Keep
this root stable. Do not delete an attempt or choose a new root to retry an
uncertain invocation. Concurrent callers cannot invoke the same admitted attempt
a second time. Missing admission/terminal persistence leaves it unresolved;
there is no automatic timeout-based reclaim or retry of evaluator inference.

Validated results are saved before applying feedback. A later call with the same
policy and request reauthenticates completion and reconciles the saved result
without invoking the evaluator again. This repairs, for example, an interrupted
ledger write after successful evaluation. Revocation blocks reconciliation until
explicitly resolved. Changed evaluator/input/policy conflicts with the admission.
Failed evaluations create no quality feedback; abstention produces unverified
feedback. Accept/reject is always `automated_ai` advisory evidence, never a human
or deterministic verdict. Current operator heads are not superseded. A result
can remain completed with `review_applied=false` on conflict, independently of
the already successful original task. `review_applied=true` means the review was
recorded, not that a later operator revision cannot become the current head.

Attempt files contain hashes and host provenance. The saved advisory audit can
quote reviewed content in its findings, so its private directory and backups
must receive the same protection as the source task. Raw evaluator errors are
not persisted. This API supplies durable orchestration. Background scheduling and review UI
remain separate integration work. No evaluator is invoked by default.


### Explicit provider-backed content evaluation

`nexus remote evaluate` consumes the original `Task` JSON on stdin;
`nexus remote auto-evaluate` consumes the original `AutomaticRequest` JSON.
Supply the same trust/TLS flags, `--routes`, `--evidence` and `--request` used for
recording the outcome. Also supply `--config /private/nexus/runtime.yaml`,
`--reviewer MODEL_ID` and an explicit `--review-max-cost AMOUNT` (zero for a
configured zero-cost local model). Omit destination/model/harness/context
overrides: evaluation uses the saved route and original request.

The evaluator must be enabled by the runtime evaluation policy. Its configured
model must declare context and estimated cost; local models must declare RAM.
Private tasks require a local evaluator, and the runtime local/cloud mode remains
binding. Local review uses the shared host resource coordinator and the existing
auxiliary provider lifecycle. Known secrets are redacted from review input and
findings. Credentials are resolved only when the admitted evaluator executes.
Review is bounded to one minute, 4096 output tokens and no tools, without provider
fallback or an automatic second attempt. The cost ceiling checks configured
estimates; it is not a provider billing guarantee or measured usage accounting.

The CLI emits the durable attempt status even when reconciliation fails after
admission. Keep the same config, evaluator, privacy and cost policy to reconcile
an existing attempt. The stored descriptor binds these settings, and changing
them produces a conflict rather than another evaluator call. These explicit
commands apply automated advisory evidence; they do not claim objective proof
of correctness or trigger another original-task execution.

### Waiting for remote completion and review

`nexus remote watch-evaluate` accepts the same original Task JSON and flags as
`evaluate`, plus a required `--review-wait 1h` (positive, at most 24 hours).
`auto-watch-evaluate` takes the original AutomaticRequest JSON instead. The
process checks the saved caller/destination binding and freshly authenticated
status every 15 seconds while the task is queued or running, then authenticates
canonical completion and applies the same durable one-attempt review protocol.
The wait budget includes evaluation/reconciliation; expiration during an admitted
review does not grant permission to repeat that uncertain evaluator call.

These are bounded foreground processes that an operator can supervise in the
background. They do not install a service or automatically enroll unrelated
requests. Stop signals cancel observation/evaluation without canceling the
original remote task. Reuse the same route, request, evidence root and evaluator
policy on restart. The Go host equivalent is `Client.WatchRecordedEvaluation`,
with an explicit deadline and poll interval between one second and one minute.

A task ending failed or canceled emits `task_failed` or `task_canceled` with no
review. An expired wait emits `waiting` plus an error, not a quality rejection.
Unknown state, changed identity, revoked access or transport failure stops the
watcher for investigation; it never redispatches, selects another destination,
reclaims a missing evaluator result or silently retries a failed provider call.
Successful evaluation remains automated advisory evidence. Whole-fleet automatic
enrollment, background service installation and a review UI are not provided by
these per-request watch commands.

### Submit and review in one workflow

`nexus remote dispatch-evaluate` combines recorded direct dispatch with the
completion/review watcher. Supply the original Task on stdin, `--instance`,
trust/TLS flags, `--routes`, `--evidence`, `--request`, evaluator `--config`,
`--reviewer`, `--review-max-cost`, and a positive `--review-wait` budget.
`auto-dispatch-evaluate` takes an AutomaticRequest, omits `--instance`, and uses
the same default accuracy-first discovery/selection policy as `auto-dispatch`.
Explicit exploration remains disabled in these CLI commands.

Reviewer configuration, privacy, declared cost policy and private evidence-root
storage are validated before original work is submitted. The storage preflight
creates, syncs and removes a small private probe; it creates no quality evidence.
It cannot guarantee against later disk exhaustion or permission changes. Actual evaluator resource admission and credential
resolution occur after successful canonical completion; a later evaluator failure
does not undo the original task. No remote task is implicitly canceled when the
caller exits or its review deadline expires.

The JSON result separates `dispatch`, optional saved `choice`, and `evaluation`.
`phase=finished` means the workflow returned a terminal observation; inspect the
evaluation status/verdict to distinguish accepted output, advisory rejection,
abstention and a failed/canceled original task. `dispatch_failed_or_unknown`
requires inspecting the saved request rather than inventing a new key. A
`review_pending` error can occur after successful dispatch; restarting the same
command with the exact original request/policy and private stores preserves both
submission and evaluator replay protection. Automatic retries use the saved
choice without fresh discovery. There is no hidden fallback or automatic retry
of uncertain evaluator inference. These opt-in combined commands enroll their
own request for review, not every task submitted by another client.


## Browser membership administration

The authenticated Settings page can manage an existing private trust registry
when the operator sets `web_ui.remote_trust_file` to its absolute, normalized
path before starting the daemon. The default is empty (disabled). Initialize
that registry using the local CLI first, and use the same registry path for the
remote service/client whose membership should change. This option neither starts
a remote server nor installs SSH credentials; no live configuration is changed
by building or opening the application.

The panel lists configured HTTPS/SSH peers and their complete scopes. It does
not claim fresh availability. Pairing uses guided fields for one peer, requires
explicit confirmation of independently verified identity and permissions, and
uses the displayed registry digest to prevent overwriting concurrent changes.
Revoke preserves other peers and blocks subsequent requests using the registry;
already admitted work is not implicitly canceled. Certificate rotation remains
the CLI `replace-trust` operation.

The same-origin BFF requires the existing authenticated browser session plus
CSRF authority for mutations. Requests have a closed, bounded JSON schema and
bounded concurrency. The browser cannot select an arbitrary registry path.
Missing, unsafe or invalid registries fail closed and are not initialized by a
browser read. Peer details are rendered as text, with no credential contents
read. All authenticated operators of an enabled browser interface have this
membership authority; there is no separate multi-user administrator role.
After a conflict or uncertain write, refresh and inspect current membership
before retrying. Discovery-based onboarding, live remote capability/task views,
and physical two-host qualification remain separate work.

### Explicit live inspection from Settings

To enable the per-peer inspection buttons, additionally configure all three
`web_ui.remote_client` paths: `certificate_file`, `key_file`, and `ca_file`.
They must be absolute normalized paths. The existing private-key permission,
CA/certificate and pin checks run on every remote call. The same
`web_ui.remote_trust_file` selects peers and HTTPS/SSH transport. These credentials
are fixed at startup and are never supplied by browser requests or returned in
inspection results. Empty or partial credentials do not enable inspection.

An authenticated operator explicitly chooses **Inspect capabilities** or
**Inspect caller's tasks**. Each request requires same-origin CSRF authority,
has a ten-second total context deadline, and shares the bounded browser operation
slots. There is no automatic scanning, polling, inference, dispatch, cancellation
or retry. Destination `info`/`inspect` scopes and fresh revocation apply through
the standard remote client. Task pages contain at most 100 request summaries;
Next preserves the returned cursor and refresh starts a new live traversal.
The caller is the configured client certificate identity, shared by authorized
browser operators, not a distinct principal for each browser session.

Results display the caller's check time and the remote observations, including
model/harness metadata and advisory availability/resources. They do not attest
model quality, reserve capacity, or grant routing authority. A failed inspection
clears the prior result and remains unavailable/unknown; it is not a task failure
or quality rejection. The browser cannot supply a URL, trust file or credentials.
Physical two-system and cross-platform browser-to-SSH qualification remain open.

### Browser task status, results and cancellation

`web_ui.remote_task_controls: true` separately enables task controls and requires
the configured remote client. It defaults to false; existing inspection setup
does not silently acquire a cancellation UI. Each listed caller-owned request
can load its current status and successful result text. The UI only offers
cancellation after loading an active queued/running submission, and requires an
explicit confirmation identifying the request and peer.

The BFF checks session/CSRF authority and validates the original instance/request
IDs. Cancellation first re-reads that request through the configured client,
requires the expected submission ID and an active state, then calls Cancel once.
The remote service's caller ownership and `inspect`/`cancel` scopes remain
required. No request is dispatched or retried. Cancellation races with normal
completion; a returned cancellation request is not a promise that work stopped.
An uncertain response clears the actionable browser state until the operator
loads current status again. Results are rendered as text and the browser
projection excludes the destination configuration digest and lease metadata.

Status is manually refreshed; this is not event streaming. The controls apply
to the configured certificate's submissions, shared by authorized browser
operators. They do not authorize task dispatch, change peer scopes, revoke
credentials or enable controls on another daemon. No live configuration or
remote work is changed by building this feature.


### Guided pairing fields

Settings exposes instance/TLS identity, explicit HTTPS or SSH transport, allowed
operations, model/harness allowlists, private/public/cloud permissions, context
and estimated-cost ceilings, and optional bounded per-minute request limits.
Only capability inspection is selected by default. Model and harness lists are
empty; private, public-network and cloud permissions require explicit choices.
SSH identity and known-hosts paths refer to the local router host. The server's
normal validation remains authoritative; the browser does not read key files,
create credentials, contact candidates or trust discovery advertisements.

The exact peer configuration preview updates with each edit. Any field change
clears the identity-verification checkbox, and an uncertain/conflicting save also
requires renewed confirmation after refresh. Fingerprints may be pasted as
colon-separated hex and are previewed in the canonical lowercase format. Hidden
SSH fields and disabled custom limits never enter an HTTPS/default-limits peer.
This replaces JSON-only entry, not the requirement to independently verify the
endpoint, certificate fingerprint, SSH host key and allowed scopes. Automatic
unpaired-device discovery and cross-machine onboarding remain incomplete.


Cancellation and revocation use inline confirmation groups with explicit confirm
and dismiss buttons. Loading status or refreshing membership invalidates an open
confirmation. An uncertain cancellation disables another attempt until status is
loaded again; the client does not automatically retry. Revocation continues to
require the displayed registry digest and does not cancel already admitted work.


### Browser lifecycle event pages

With remote task controls enabled, loading a request's current status exposes
progress controls for its task IDs. Refresh starts at the first page and Next
loads at most 100 additional lifecycle facts. Each request rechecks authenticated
status ownership and uses the normal remote client's inspect scope, TLS pins,
revocation and selected HTTPS/SSH transport. The browser projection contains only
event sequence, kind and timestamp, plus page state/cursors. Raw runtime data
(prompt messages, tool arguments and configuration) is not forwarded; final text
remains available from task status. Failed or malformed pages clear displayed
progress and require an explicit refresh. **Follow progress** explicitly starts
cursor-based reads every five seconds, with at most one request in flight and
one latest page displayed. It drains committed terminal pages before stopping;
completion does not automatically load result text. Following stops on a failed
or malformed response, regressing event head, Stop, a hidden/removed view, or
30 minutes. Restarting requires another explicit click. Late responses after
Stop are ignored. These are bounded reads over the selected HTTPS/SSH transport,
not a push stream, inference dispatch, cancellation or automatic failure retry.
The parent task state is labelled last loaded, independently of event progress.


### Explicit browser dispatch

An operator can opt in with `web_ui.remote_dispatch_directory`, a dedicated
private absolute path for durable routing bindings. This requires remote client
credentials and `remote_task_controls: true`; defaults remain disabled. Startup
opens the private RouteStore before exposing dispatch. The browser cannot choose
its path or a network endpoint. All authenticated browser operators share the
configured remote certificate and its paired scopes.

Peers with dispatch permission expose a form for an explicitly chosen model,
optional external harness, task domain/profile, context, cost and privacy. The
user reviews the exact request before sending. A new random request ID and peer
are put in the page URL fragment before sending; these are recovery references, never
authority. Fragments are not sent in HTTP requests; the production Settings route keeps
rejecting query strings. No prompt, credential or authoritative task state enters
browser storage or the URL. Bookmark/copy this recovery URL before closing the page if
it is needed later. Changing to a new request replaces the recovery reference
in the current page; it does not cancel or retry earlier work.

The BFF uses DispatchRecorded: the exact task digest, destination and caller
certificate are durably bound before any request to the peer. Changed reuse of
a request ID is rejected. The browser sends once, then exposes status controls,
including after an uncertain response. Reloading the recovery URL performs no
dispatch. Starting another independent request is an explicit user action and
can create duplicate work if used as a replacement before inspecting the prior
request. BFF failures after dispatch invocation are conservatively reported as
unconfirmed. Admission, model/harness permissions, private/local requirements,
resource limits and revocation remain enforced by the normal remote client and
destination. This form is explicit routing; automatic accuracy-first selection
and advisory evaluation remain separate CLI workflows, not automatic UI effects.

### Inspect a saved request without its prompt

`nexus remote recorded-status --trust /private/nexus/peers.json --cert
/private/nexus/client.pem --key /private/nexus/client-key.pem --ca
/private/nexus/ca.pem --routes /private/nexus/routes --request REQUEST_ID`
reads the existing caller-side route binding and inspects that destination over
its configured HTTPS or SSH transport. It takes no stdin or prompt and rejects
explicit destination/model/harness/context overrides. The original caller
certificate remains required; current trust, inspect permission and revocation
still apply. A missing or unsafe route directory fails without creating it.

This works for explicit and automatic dispatch after a route binding exists.
It does not discover candidates, select a new destination, repair uncertain
intake, resubmit work, cancel, or apply quality feedback. A status lookup cannot
prove that a newly supplied task matches the original intent: outcome and review
operations retain their existing exact-request checks. Missing or uncertain
remote status remains an error, never evidence of successful completion. Browser
integration with automatic routing remains separate work.

### Automatic browser routing (opt-in)

`web_ui.remote_automatic_evidence_directory` is empty by default. Enabling it
requires the existing remote dispatch directory, task controls, client credentials
and trust registry. The daemon preflights the private evidence root at startup;
each automatic dispatch rechecks its durable write capability before selection.
Browser operators share the configured caller certificate and scopes.

Authenticated, CSRF-protected POST `/api/v1/remote-auto-dispatch` accepts version,
request_id, prompt, domain, profile, difficulty, context_tokens, max_cost, private
and capabilities. No destination, candidate scores, evidence paths, credentials
or exploration options are accepted. The server uses the existing discovered
accuracy-first selection with its default policy and exploration disabled.
Capacity, capability, identity and authorization checks remain authoritative;
remote advertised quality is not imported as trusted evidence. Chosen destination
and intent remain durable before dispatch. The bounded request timeout is not
evidence that execution stopped: any unconfirmed response must be inspected.

POST `/api/v1/remote-recorded-status` accepts only version and request_id. It
recovers the destination from the route record and performs read-only status
inspection using the original caller identity. Responses project task metadata;
private config digests and transport diagnostics are excluded. Neither endpoint
automatically retries, changes destination after uncertainty, or grades results.
The existing exact-intent outcome/evaluation workflow remains necessary to add
quality evidence. Settings exposes an automatic request form only when this opt-in is enabled.
Review the task requirements, then confirm one selection and dispatch. Before
sending, the page saves only the request ID in a URL fragment; prompts and
credentials are not stored in browser persistence. Reload never resends. Use
Find saved destination and status to recover the bound peer, then the existing
status/result, lifecycle progress and cancellation controls. Starting another
independent request replaces the recovery link and may duplicate unresolved work;
preserve earlier IDs and inspect them first. Browser quality review is a separate opt-in described below. No live configuration is enabled by this change.

### Browser advisory reviews (opt-in)

A separate `web_ui.remote_review` opt-in sets `model` and an explicit `max_cost`
(including zero). It requires automatic evidence storage and the configured
evaluation judge. Startup validates reviewer policy without invoking inference.
Private requests require a local reviewer; each invocation uses the daemon's
existing shared resource coordinator and configured evaluator admission. Review
time is bounded to one minute, inside a 75-second browser request limit.

Authenticated, CSRF-protected POST `/api/v1/remote-auto-review` accepts `action`
(`evaluate` or `status`) and `request` containing the exact original automatic
browser-dispatch fields. The original prompt and routing intent must match the
immutable selection and route binding. No reviewer, cost override, verdict,
method or receipt supplied by the browser is accepted as authority. The service
authenticates completed output and its actual model/harness provenance before
using the existing durable one-attempt evaluator. Incomplete tasks are not
reviewed. Uncertain evaluator calls are not automatically repeated.

Evaluation responses report attempt status and whether a review was applied;
they do not assert that its verdict remains the current head. The `status`
action performs no evaluation or ledger creation and projects the current
classification, method and verdict after authenticating original completion.
AI reviews remain advisory. Existing operator heads are not automatically
replaced. No prompt is stored by the browser API for reload recovery: callers
must supply the original request again. Settings exposes review controls only after an explicit saved-status lookup
reports success and the review opt-in is enabled. Original requirements remain
in page memory after dispatch and are prefilled; reload clears them and requires
re-entry. Changed requirements are rejected by the saved intent binding. Review
the requirements and confirm one AI evaluation; editing invalidates confirmation.
An uncertain response disables another attempt in that form while preserving
read-only current-review checks. Finding an existing review head also disables
evaluation. Server receipt protection remains authoritative across reloads and
concurrent sessions. Status shows advisory/confirmed classification and method,
without representing AI evidence as human verification. Automatic background
supervision and browser-to-provider qualification remain separate work.

### Persistent review queue and worker

An explicit CLI workflow can retain a recorded task's requirements for review
without keeping the browser open. Unlike routing bindings, the dedicated
owner-private queue **contains original prompts**. Keep it local and out of
Git, diagnostics and shared artifacts. No queue is enabled or populated by
ordinary dispatch or browser review configuration.

`enqueue-review` accepts the original explicit `Task` JSON on stdin;
`enqueue-auto-review` accepts the original `AutomaticRequest` and resolves its
saved choice. Supply the normal trust/cert/key/CA flags plus `--routes`,
`--evidence`, `--review-queue`, `--request`, `--config`, `--reviewer`, explicit
`--review-max-cost`, and `--review-deadline` as an absolute RFC3339 timestamp
at most 24 hours ahead. Keep the same timestamp on an exact retry. Enrollment
checks current authenticated ownership, original intent, private/local policy
and evidence storage; it does not dispatch or evaluate. The saved job binds
caller/destination/task, storage scope, evaluator provenance, locality, timeout
and deadline. Changed enrollment is a conflict, not an extension or new attempt.

`run-review-jobs` uses the same connection, queue, route/evidence and reviewer
configuration, plus an explicit `--review-wait` of at most 24 hours. It processes
pending jobs serially and checks again every 15 seconds while work remains.
It exits when all observed jobs are terminal or the worker wait expires; an
operator-owned supervisor may run it again. Each pass has one OS-locked worker
on macOS/Linux. Other platforms fail closed pending lock qualification. Process
exit releases the lock; missing terminal queue receipts may be reconciled from
the durable evaluator attempt without another inference. Shutdown preserves
pending intent. A failed/uncertain evaluator, access failure or policy change
produces `attention`, with no automatic reattempt. Failed/canceled tasks and
expired jobs are terminal without quality feedback.

`review-job-status --review-queue /private/nexus/review-jobs --request REQUEST_ID`
reads only job metadata and never creates a missing queue. A `completed` receipt
means review orchestration completed, not that its verdict passed or remains the
current head. Use the existing authenticated current-review lookup for that.
Terminal job records are retained, not silently recycled; queue size is bounded
and archival requires separate explicit administration. Automatic enrollment is available through the configured browser/daemon path
described below. Service installation, queue-management UI and real second-host
qualification remain separate work. These commands
neither create a service nor reconfigure a running daemon.

For durable enrollment before submission, `auto-dispatch-review-job` takes the
original `AutomaticRequest` on stdin and the same queue/reviewer/deadline flags.
It saves that exact request, caller identity, storage scope and reviewer policy
before discovery, selection or inference. The queue's worker lock remains held
through dispatch; a worker cannot race a half-submitted request. The command
returns `review_queued` only after confirmed dispatch. An unconfirmed dispatch
keeps its saved intent and reports `dispatch_failed_or_unknown`; inspect the
original request, never start another key to work around uncertainty.

A later `run-review-jobs` resolves the immutable automatic choice and original
caller before reading completion. It never selects a destination, repairs a
missing binding, submits a task or retries execution. A job without a published
choice stays pending until its deadline; merely restoring an available peer is
not permission for the review worker to dispatch. Completed/attention jobs do
not authorize another combined dispatch. Exact request/deadline/policy retries
cannot overwrite the private intent. The configured browser/daemon path can use the same enrollment operation; this
command also provides an explicit operator-owned workflow without installing or
enabling a service.

Embedding hosts may use `Client.RunReviewJobs` to supervise an existing private
queue. It performs an immediate pass, then waits fifteen seconds between passes.
Only `ErrReviewQueueBusy` is retried; queue integrity/storage failures stop the
loop for operator attention. Individual terminal job receipts remain durable.
Cancel and join the call before closing the evaluator, shared resource coordinator
or evidence databases. This library lifecycle does not enable daemon enrollment
or install a background service.

The stock daemon can now own that worker when `web_ui.remote_review.queue_directory`
is set to a separate private absolute directory and `wait` is an explicit
positive duration at most `24h` (for example `1h`). The existing remote reviewer
model/cost, dispatch directory, automatic evidence directory, client credentials
and trust registry are required. Omit `queue_directory` to retain manual review
only. The worker starts with the dispatcher, resumes existing jobs, and is
cancelled and joined before shared storage/resource cleanup. Queue-level failures
set the `remote_review` health component to `supervisor_error` and make daemon
readiness unhealthy; no private error text is returned. Health describes the
worker, not individual job verdicts. Inspect terminal receipts and current review
heads for those. This setting also enrolls browser automatic dispatches before discovery or
submission. Original requirements and the absolute deadline survive lost
responses and exact retries; retries never extend the deadline. A conflicting
concurrent enrollment fails closed. The Settings form discloses private prompt
retention and advisory AI review before confirmation. Existing explicit CLI queue
commands remain available with matching reviewer/storage policy.
No live configuration or service installation is changed by this implementation.

For queued browser requests, **Check background review** reads the saved job
without requiring the original prompt after reload. The session/CSRF-protected
`remote-review-job` endpoint rechecks the immutable request binding, caller
ownership and current remote inspect permission/revocation before returning
only request ID, status and the job's applied flag. Missing, unbound or revoked
requests remain unavailable; the read does not create or repair state. `pending`
means no terminal queue receipt yet; `attention` or `expired` requires inspection.
A `completed` receipt is not a current quality verdict. This control never
evaluates, dispatches, retries or returns prompt/output/paths.

### Unpaired discovery record contract (transport integration pending)

The DNS-SD record format follows the PTR/SRV/TXT separation described in
[RFC 6763](https://www.rfc-editor.org/rfc/rfc6763). The proposed service type is
`_nexusrouter._tcp.local.`. `ParseDiscoveryCandidate` accepts a complete instance
record with a concrete private IPv4 or IPv6 address and service port. Version-1
TXT fields are `v=1`, `id=INSTANCE`, `name=TLS_SERVER_NAME`, `pin=SHA256`, and an
optional `ssh=PORT`. The instance and ID must match. Names are bounded lowercase
DNS labels; keys compare case-insensitively and duplicates/unknown fields reject.
Public, loopback, mapped or scoped/link-local addresses are excluded.

The result is explicitly unverified metadata, never a Peer or a grant of
permissions. Even its certificate fingerprint is a claim requiring independent
verification. No models, harnesses, quality scores, tasks, credentials or policy
grants are advertised. The receiver timestamps observations and caps their
lifetime at 120 seconds; zero-TTL goodbye records do not become candidates.
The parser caps TXT strings/count/total bytes before projection and performs no
network or registry operation. Interface binding, bounded DNS packet collection,
advertising lifecycle, candidate conflicts and Settings integration remain to
be implemented. This contract alone does not discover devices on the network.
