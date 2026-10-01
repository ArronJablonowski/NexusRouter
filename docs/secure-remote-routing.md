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

## Protocol and durability

| Method and path | Scope | Result |
| --- | --- | --- |
| GET `/v1/remote/info` | info | versioned allowed model catalog and availability |
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
access are scoped to that caller. There is no remote database-wide task listing,
filesystem endpoint, runtime configuration mutation, or cross-caller cancellation.
`info` advertises configured capabilities, not a live provider capability probe
or a promise of free capacity; dispatch independently checks actual admission.

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
service packaging; device discovery/pairing UI; current resource/model probes;
caller inventory UI; automatic remote destination selection integrated with
DAR-132's versioned, accuracy-first model–harness evidence; and operational
rate/retention policy. The endpoint currently takes an explicit model and peer.
It does not choose a winner based on idle capacity or claim remote evidence is
integrated into the joint ranker. A full repository check is required before push.

NVIDIA PAIR is product inspiration for explicit pairing and separate-task private
compute routing. Its product page does not establish this protocol's security or
provide acceptance evidence for NexusRouter.
