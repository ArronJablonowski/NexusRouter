# Model collaboration validation — 2026-10-09

Source: working tree based on `70708f7b71aa8b9229a077da32b2db190d1e0255`,
with the collaboration journal, task tools, provenance, settings and browser
changes committed together with this report. Linear project retrieval required
reauthentication; checked-in PRD and direct user requirements were used.

## Evidence

All tests used disposable stores, fake provider factories, loopback HTTP fixtures,
and fixture-backed Chrome. No live model inference, router contacts, credentials,
private runtime stores/logs, configuration changes or service changes were used.

- Full race suites: `go test -race -timeout=10m ./internal/agentchat ./internal/config ./internal/webuiapp ./webui ./tools ./runtime ./remote ./harness` with Chrome required, offline Go dependencies and live/native opt-ins disabled. Store/config/browser authority/tools/runtime/remote/harness passed; runtime took 226.256s and remote 57.816s. The first Web UI run failed `TestChromeLiveSettingsPreservesDirtyDraft`: the newly added optional false field made an old response appear dirty. Fixed comparison normalization; no assertion was weakened.
- Repeated affected full suites after the fix: `go test -race -timeout=10m ./webui ./internal/webuiapp ./internal/config ./internal/agentchat`: passed (30.903s / 5.432s / cached / 1.460s).
- Application boundaries: `go test -race ./internal/app -run '^(TestModelCollaboration.*|TestCollaboration.*|TestNativeToolsIdentityAndConstructorSnapshot|TestUnified.*|TestDirectInference.*|TestRemoteExecution.*)$' -count=1`: passed (18.299s). Real task/provider loops exchange an idea and reply across three independent tasks, with durable source attribution. Tests reject spoofed provenance/task IDs, duplicate JSON members, cancellation, and repeated-call content changes; known secrets are redacted. Native catalogue/identity snapshots include collaboration for all five adapters. This is not physical native execution evidence.
- Final provenance/runner tests passed in app and fixture Chrome (3.661s / 2.356s). The runner metadata validation fixture initially omitted a required endpoint; supplying a loopback endpoint corrected that fixture. The full configuration race suite then passed (13.210s), including atomic collaboration-toggle persistence and runner metadata bounds.
- Store checks include restart reads, exact idempotent receipts, timestamp preservation, addressed/broadcast privacy, bounded paging, concurrent quota enforcement across two connections, unsafe symlink rejection, and escaped text bounds that prevent an oversized record from poisoning bounded pages.
- Browser checks cover route selection without starting chat work, sender/host/harness-registration/runner/model/timestamp display, safe plain text, keyed row preservation, filter-draft preservation, older-page navigation, failure/staleness indication and desktop/mobile width. Screenshots at 1440 and 390 pixels were inspected. Time is stored in UTC and displayed in the browser's local timezone, with the UTC value retained on the time element.
- Settings request decoding tracks field presence separately from comparable projections. Older clients that omit collaboration retain its saved value; explicit false disables it. Unknown fields and null collaboration-toggle values are rejected.
- Reviewed embedded asset digest: `32e78189c66fb025135d077a2297f31f0749e8b5e2bbb7e105dfeadfc2702099`. The first new browser test exposed a missing served-asset registration; adding the script to the authenticated asset list fixed it.

## Scope and remaining checks

This is change-scoped verification, not a full `make check` pass or MVP/release
qualification. Earlier full-gate release/API/application/CLI failures are recorded
in the unified-routing report; this turn did not rerun the entire repository gate.
Cross-host transport, inherited worker mailbox authority, native installed-runtime
qualification, real-model willingness/correctness, and crash-injection at every
publication boundary remain outside these fixtures. No deployment was performed.
Final source formatting/1,000-line guard, `go vet ./...`, `go build ./...`, and
`git diff --check` passed after the runner metadata change. GitHub main was
`70708f7b` before the normal backup push; no remote changes were overwritten.
This report is committed with the tested source. The worktree contained only
source, tests and documentation; no journal databases, binaries, credentials or
fixture screenshots were staged.
