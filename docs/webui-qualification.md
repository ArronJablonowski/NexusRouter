# Web UI and Kanban qualification

DAR-86 has one deterministic source-tree gate:

```sh
make qualify-webui
```

The gate uses loopback provider fixtures, disposable browser profiles and
disposable SQLite databases. It does not contact configured model providers,
consume signed-in account usage, or modify an operator database. The target
sets `DARWIN_REQUIRE_CHROME=1`, so missing Chrome or a compatible Node runtime
fails instead of silently producing incomplete browser evidence.

## Acceptance evidence

| Requirement | Deterministic evidence |
| --- | --- |
| Local, cloud and hybrid | Production application tests exercise local-only Ollama-shaped execution, cloud-only OpenAI-compatible execution, and a cloud coordinator delegating to a bounded local worker. The browser BFF and presentation contract are mode-independent. Local-only egress tests cover execution, auxiliary review, skills, telemetry and transport construction. |
| Authenticated chat and streaming | Browser-session tests cover one-time approval, same-origin cookies, bounded history, provisional streaming, committed catch-up, disconnect/reconnect without gaps or duplicates, steering, cancellation, feedback, approvals and ambiguous-operation reconciliation. |
| Inspection and Kanban | Contract, BFF and real-Chrome tests cover route/resource/health inspection, board reads, filters, keyboard navigation, mutation receipts, conflict refetch, lifecycle controls, durable supervision, acceptance evidence and restart-safe operation lookup. |
| Security | Every browser route is authenticated before parsing protected input. Host, Origin, Fetch Metadata and CSRF violations fail closed. Content is rendered through text nodes under a self-only CSP. Adversarial real-browser fixtures cover script, event-handler, URL and markup injection. Secret and raw tool/provider payloads are excluded from browser projections. |
| Idempotency and recovery | Browser operations use durable idempotency journals and exact receipt validation. Definitive rejection is refetched; ambiguous effects are reconciled and never automatically replayed. Process tests kill and restart the daemon during streams and Workboard ownership. |
| Accessibility | Static checks verify unique IDs, valid ARIA references, programmatic labels, natural tab order, visible focus, reduced motion and WCAG 2.2 AA text contrast. Chrome's accessibility tree must expose named interactive controls and the required landmarks. Keyboard tests cover navigation, modal focus trapping/restoration and focus preservation across authoritative refresh. |

## Supported browser matrix

NexusRouter serves only standards-based, vendored HTML, CSS, JavaScript,
`fetch`, `EventSource` and same-origin cookies. Support claims are intentionally
narrower than standards compatibility:

New browser chats use adaptive routing by default. An operator may set
`web_ui.default_model` to a configured model alias when a supervised surface
must use one explicit coordinator. Follow-ups preserve that model through the
durable session lineage. Configuration validation rejects unknown aliases, and
disabling the Web UI requires clearing the override.

| Browser | Status for 1.0 | Evidence |
| --- | --- | --- |
| Current stable Google Chrome on macOS | Qualified when the real-Chrome tests run without skips | Headless Chrome DevTools Protocol navigation, keyboard, accessibility-tree, content-injection, CRUD and conflict fixtures. |
| Current stable Chromium on Linux | CI-capable; qualified only by a recorded no-skip run on the release candidate | The same fixture discovers `chromium`/`chromium-browser`; hosted release jobs do not currently install it. |
| Safari | Compatible target, not yet qualified | Manual smoke testing is required before claiming support. |
| Firefox | Compatible target, not yet qualified | Manual smoke testing is required before claiming support. |
| Mobile browsers | Unsupported for 1.0 | Responsive layout is defensive behavior, not a mobile support claim. |

Set `DARWIN_CHROME` to an absolute Chrome/Chromium executable to bind a run to
an explicitly reviewed browser binary. Node must provide its built-in
`WebSocket`. Ordinary `go test` runs may skip these optional dependencies;
`make qualify-webui` requires them.

## Latency and resource budgets

These budgets exclude model/provider inference and operator approval time:

- Deterministic route selection remains below 150 ms, as measured by the
  separate `make qualify-performance` selector benchmark.
- A loopback browser API read or mutation should complete within 500 ms when
  its application callback is immediately available. Tests deliberately use
  longer bounded waits only for process startup, SQLite polling, simulated
  conflicts and browser scheduling; those waits are not latency measurements.
- Initial real-browser fixture readiness and authoritative refresh each have a
  six-second hard test deadline. This is a qualification timeout, not a normal
  user-facing target.
- Browser state is bounded to 100 chats, 500 rendered messages, 100 boards and
  10,000 cards. Server-side BFF concurrency, SSE streams, request bodies,
  history pages, operation pages and event catch-up are independently capped.
- The UI loads no CDN assets, remote fonts, analytics, service workers or
  browser storage. Fully local mode therefore adds no browser-originated egress.

Record the commit, OS/architecture, Chrome version, Node version, complete
command output, skipped tests, elapsed time and peak process RSS for release
evidence. The gate itself does not turn an unrecorded local run into a
multi-browser or sustained-load claim.

## Known limits

This gate does not prove external provider availability, model quality,
arbitrary browser extensions, non-loopback reverse proxies, mobile support,
multi-day soak behavior or Safari/Firefox compatibility. Manual or hosted
evidence for those environments must be recorded separately and must not be
inferred from a Chrome pass.
