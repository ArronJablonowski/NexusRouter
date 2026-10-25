# Optional Telegram text chat

Telegram support is **disabled by default**. This implementation has not been
connected to a Telegram account, bot, or live chat. No credentials were installed.
Enabling it later requires a separate operator decision and configuration.

## Implemented behavior

The stock daemon can run a Telegram Bot API long-polling connector. No public
listener, inbound port, webhook, account login, or automatic bot discovery is
introduced. Only private messages from explicitly allowed numeric user IDs are
accepted. Sender and private-chat IDs must match; groups, channels, bot senders,
unknown users and unsupported update types cannot invoke a model or get a reply.

Text messages use one explicitly configured, zero-estimated-cost **local chat
model**, with local-only admission and fallback disabled. The configured provider
can address Spark, or the connector can run on Spark itself. A model advertised
only through the command grid is not automatically imported or selected. Cloud
models require future explicit support; unavailable models never silently switch.
Existing resource admission, memory reserves and host-wide reservations still
apply.

`/start` and `/help` explain the connector. `/new` starts a fresh conversation.
Other slash commands and non-text messages receive a short help response. Text
input is limited to 16 KiB. Replies are plain text, split below Telegram's message
limit with Unicode-safe boundaries, with at most eight messages. Longer answers
are retained in the private task record and the Telegram reply says it was
truncated. Link previews are disabled and content protection is requested.

Conversation context survives daemon restarts. A separate private task database
keeps Telegram from continuing or exposing the commander's existing chats,
uploads, memory or skills. Host tools, ComfyUI, file creation, coding harnesses,
delegation and auto-approval are not exposed through this text connector. It does
not yet offer files, images, voice, streaming, approval buttons, Telegram history
in the Web UI, or concurrent conversations. One request is processed at a time;
new messages wait while its model task is active.

Local model inference does not make Telegram private from Telegram: the service
receives the text sent and returned through its Bot API. This is not an encrypted
secret-chat integration.

## Future operator configuration (not applied)

The following is an **inactive example**, not account setup or permission to
connect. Settings loading checks configuration without resolving a credential or
contacting Telegram.

```yaml
telegram:
  enabled: false
  token_env: NEXUS_TELEGRAM_BOT_TOKEN
  allowed_users: []
  model_id: "" # Existing local chat model ID, with estimated_cost: 0.
  state_directory: /absolute/private/path/telegram
```

When connection is separately authorized, choose a bot token supplied through the
named environment variable, add the intended numeric user IDs, select the model,
and use a dedicated directory owned by the service account with mode `0700`.
Never put a token in YAML, a URL, a chat message, a command argument or Git.
Explicitly enabling the connector and restarting the daemon starts polling.
There is currently no live Settings-page toggle. A missing or malformed token,
invalid configuration, nonprivate state directory, unavailable isolation lock,
or incompatible saved state prevents initialization. Existing webhooks are never
removed; Telegram's polling/webhook conflict is reported as an unavailable API.
No setup action calls `deleteWebhook`, changes account settings or drops updates.

Production transport is fixed to `https://api.telegram.org`, uses bounded TLS
HTTP requests, does not follow redirects or use inherited proxy variables, and
never returns Telegram descriptions or credential-bearing transport errors to
logs. Tests substitute a loopback simulation internally, not a configurable
production endpoint.

## Recovery and evidence

The private directory contains `state.json`, a stable `worker.lock`, and
`tasks.db` with SQLite sidecars. These are sensitive runtime state, never Git
artifacts. Preserve them when upgrading. Do not share the directory between bots
or delete it to resolve a problem: that can lose conversation identity and replay
unacknowledged updates. Back up the directory only at an idle, stopped boundary.

Intake is persisted before task admission. Its stable submission key and exact
previous-task identity allow acknowledgement-loss recovery without creating a
second model task. Telegram's update offset advances only after processing or
an explicit authorization/type rejection. The polling call retrieves one update
at a time; there is no unbounded in-memory inbox. Telegram retains updates for a
limited period, so prolonged outages can still leave an upstream coverage gap.

Outbound replies have a stricter rule: **save send intent before dispatch; never
automatically resend uncertain delivery**. A crash or error between that write
and the confirmed local receipt stops this connector and emits a generic daemon
log diagnostic. `state.json` retains `pending.delivery_uncertain: true`, the
submission identity, reply chunks and last confirmed chunk position. A crash
before actual delivery may therefore omit a reply; exactly-once Telegram delivery
is not promised. Even a definite API rejection stops sending conservatively.

An operator must inspect the private record and actual Telegram chat before any
manual reconciliation. No automatic reset/resend command is provided. Do not
edit receipts or advance offsets while the worker is running. Model failures
produce a failure reply, never an automatic inference retry by this bridge.
Temporary API/read failures back off to one minute and preserve saved work;
NexusRouter's existing durable dispatcher recovery policy still governs model
execution. Revoked users cannot resume pending dispatch or receive saved replies.
Configuration/model identity drift or corrupt state fails closed for inspection.
Token rotation for the same bot and environment reference preserves identity.

Reference: [Telegram Bot API](https://core.telegram.org/bots/api), specifically
`getUpdates`, `sendMessage`, and the update-confirmation and text-size contracts.
Verification covers a simulated API and actual local durable task integration;
live Telegram permissions, account setup, delivery and mobile behavior remain
unverified intentionally.

When enabled, the existing daemon health/Status view also reports a supplemental
`telegram` check: starting, available, degraded after an API failure, or stopped.
It contains no user, bot, chat or token identity. A Telegram outage degrades the
health report without making otherwise healthy computer-side chat unavailable.
With Telegram disabled the existing health report is unchanged.
