# Chat activity indicator — 2026-10-22

Source: parent `a2a6452f` plus the associated committed changes. This is a browser presentation change; task dispatch, model permissions, persisted chat data and provider services are unchanged.

## Behavior

- A compact pulsing dot and status label stay just above the composer, outside the conversation scroller. States include Sending, Waiting to start, Working, Responding, waiting for approval, and non-animated reconnecting/unavailable states.
- Local state comes from existing submission receipts/status, validated task controls, lifecycle events, streamed text and approval observations. Completion, failure and cancellation clear the indicator, including cancellation of an unlinked queued submission. Task-specific terminal updates do not clear activity for a different selected task.
- Spark state uses the existing remote status observer. A request-identity and selected-chat guard prevents detached/older remote cards from reactivating activity in a new chat, including while another request is being sent. Cached nonterminal state is presented as unavailable, without claiming inference is active.
- New chats reset activity. Reload reconstructs it from existing status; there is no new persistence, polling loop, inference request, automatic retry, timer-based assertion of liveness or exposure of internal reasoning. Terminal-state disconnects/late output cannot restart the animation.
- Polite atomic live-region announcements change only when the label changes. The pulse honors reduced motion. The new asset is explicitly served and bound into the reviewed embedded manifest (`b11cd5cee73e6855923e943c439a4d4dcc71925e2aeb8b99b1bcc7ca63723cc7`).

## Validation

- Deterministic lifecycle/ownership tests cover queue, response streaming, approvals, reconnects, terminal states, late updates, control-based restoration, remote/local isolation, unavailable states and resets.
- A real-browser test with loopback HTTP/SSE fixtures exercises local running/streaming, completion/failure/cancellation, reload, new chat, exactly one queued submission and cancellation, Spark queue/running/completion, and polling an older Spark card after starting a new task. The test is part of both the Chrome suite and the Firefox matrix. Browser reload checks wait for the new document; Chrome closes gracefully to avoid profile-cleanup races.
- Layout checks keep both activity and the textbox visible at 1440×900, 390×844, 320×568 and 844×390. Reduced-motion behavior is asserted. Desktop and narrow screenshots were inspected in Chrome; Firefox screenshots were also retained privately.
- Full `./webui ./internal/webuiapp` race regression passed before the final request-ownership/terminal-control refinements (92.230s / 8.083s). A later full run found only the new Firefox activity test clicking New chat before startup authority checks enabled it; the test now waits for the enabled control, preserving the safety gate. Other passing, unchanged browser scenarios were reused. Final-source Chrome lifecycle/behavior/asset race tests passed (2.625s), Firefox lifecycle race test passed (3.790s), and the complete application web-handler race suite passed again. Earlier final-ownership Chrome/Firefox checks also covered host mentions, remote sidebar reload and CSRF-bound remote controls.
- `go run ./cmd/check`, scoped `go vet`, `go build ./...`, embedded JavaScript syntax checks and `git diff --check` passed. The accidental standalone intermediate build was moved out of the source tree under the maintenance lock and registered as a disposable QA binary; it is not a release or deployment artifact.

## Limits

Tests use local fixtures, not fresh real-model/Spark inference. Physical phone keyboards, Safari and assistive-technology sessions were not exercised. No full `make check`, production restart or installed-service verification is claimed; the previously recorded broad baseline gaps remain. The installed application is unchanged in this development turn.
