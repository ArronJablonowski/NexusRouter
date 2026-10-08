# Chat rename and pin controls — 2026-10-08

Base: `d4576ec094ebad7c6c176102b73d599000e60ea8` on `main`. Results apply to the source committed with this report. Requirements came from the user's requested three-dot chat menu and the repository PRD/authentication boundaries. This is scoped validation, not a full `make check` or exhaustive qualification.

## Implemented behavior

- Each sidebar chat has an accessible three-dot menu with Rename and Pin/Unpin. Menu arrow navigation and Escape restore focus; rename uses a labeled modal with Save/Cancel and bounded input.
- Titles and pins are stored in `<telemetry.database>.chat-preferences.json`, with private file permissions, a cooperative cross-process lock, per-chat optimistic revisions, atomic rename and file/directory sync. Bounds are 1 MiB, 1,000 customized chats, and 100 Unicode code points per title (browser input is additionally bounded by its native length constraint).
- Updates require an existing durable chat and pass the application's credential-redaction checks. Browser writes retain session, same-origin and CSRF authorization. Reads and writes have bounded handler concurrency and deadlines; writes are never retried automatically. A changed revision returns conflict instead of replacing an unseen edit.
- Server pagination sorts pinned chats first, then the original recency order within each group. Cursor ordering is bound to the pin set; a changed pin set invalidates an old cursor, and the UI reloads the first page rather than silently skipping chats. Metadata does not change event history or task execution state.
- Saved names update the list and heading, including direct chat URLs. Polling preserves open drafts, stale preference revisions cannot restore old metadata, and a rename uses the revision captured when editing began. Other browsers' changes reconcile through list refresh.
- Updated the reviewed embedded asset digest. No database-schema migration is required. Backups must include the preferences sidecar along with the task database.

## Passing validation

All Go checks used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. Storage/application runs used fresh temporary `DARWIN_PROCESS_OWNER_DIR` directories. Browser runs required Chrome via `DARWIN_REQUIRE_CHROME=1`.

- Full race suites for `sessions`, `internal/config`, `internal/browserauth`, `internal/webuiapp`, and `webui`. The final Web UI run (`go test -race -timeout=5m ./webui`) passed in 32.653 seconds. Final browser-handler run passed in 5.676 seconds. Successful unrelated suites were reused after later browser-only changes.
- `go test -race ./internal/telemetry ./internal/app -run '^Test(Chats|ChatHistory|ChatPreference|TaskList|ListTasks|ListSessionTasks)' -count=1`: passed (14.872 seconds telemetry, 19.697 seconds application).
- New persistence/concurrency checks cover file reopening, private permissions, concurrent writers, stale revisions, invalid names and corrupt preferences. Application checks reject unknown chats and verify list/read projections after saving.
- Pagination checks cover an old pinned chat before newer unpinned chats, several pinned chats across one-item pages, recency within groups, insertion high-water fencing, rename-only cursor reuse, and rejection after pin ordering changes.
- Browser-handler checks cover unauthorized requests, missing CSRF, invalid/ambiguous updates, stale-write conflicts and exhausted mutation capacity.
- Real-Chrome `TestChromeChatRenameAndPinSurviveRefresh` covers Rename, Pin, Unpin, reload persistence, list and heading reconciliation, menu focus/Escape, draft retention during refresh, and a concurrent rename conflict after newer metadata arrives. Exactly three intended writes are accepted. Mobile 390×844 and desktop 1440×1000 layouts were inspected from synthetic-fixture screenshots; there was no horizontal overflow.
- Existing real-Chrome routing, refresh, approvals, settings, inventory, Workboard and embedded shell checks also passed in the full Web UI suite.
- `go run ./cmd/check`, scoped `go vet` across affected packages and CLI wiring, `go build ./...`, and `git diff --check`: passed. Static checks/build were repeated after final production edits.

## Failures investigated during verification

The expanded chat script initially exceeded the Web UI's strict below-1,000-lines guard. Removed an unused formatting helper and excess whitespace; retained the guard unchanged and verified the final embedded sources pass.

An existing model-inventory Chrome test intermittently skipped its recovery state because its synthetic server advanced on every request, including automatic polling. Replaced request-count state transitions with explicit fixture phases while preserving every presentation, recovery, privacy and empty-state assertion. The full Web UI suite then passed. This was a test-fixture correction; model-inventory production behavior was not changed.

## Limits and deployment

No live router calls, real model inference, private runtime database/log reads, service changes or deployment were performed. Tests use temporary stores and synthetic HTTP endpoints. Linux runtime and other browsers/devices were not qualified. The previously documented unrelated broad API admission-fixture failures were not re-run for this presentation feature; this report does not claim the full repository/API suite passes.

The running daemon and already open browser document do not acquire this feature from a Git push. Deployment still needs applicable backup/idle checks, the updated binary, and a browser reload. Missing preference sidecars intentionally restore default presentation; pair database and sidecar backups to preserve custom names and pins.
