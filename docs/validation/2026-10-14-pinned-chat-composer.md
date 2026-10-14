# Pinned compact chat composer — 2026-10-14

The chat input previously followed messages, generated files, approvals and outcome controls inside a scrolling panel, allowing it to disappear below the viewport. The chat shell now occupies the dynamic viewport; a separate keyboard-focusable conversation region scrolls above a persistent bottom composer. The chat index and navigation retain their own scrolling, including a compact mobile chat index. Non-chat pages keep their existing document layout.

The composer uses a rounded input surface, a compact upward-arrow Send control with an explicit accessible name, a message placeholder and quieter secondary controls. The original associated label, character count and keyboard help remain available to assistive technology. Enter/Shift+Enter submission logic and existing disabled/uncertain-operation safeguards are unchanged. Model suggestions open above the composer; routing receipts and mutation notices have bounded scrolling so they do not displace input. Safe-area padding and dynamic viewport units support smaller browser viewports.

Message reconciliation preserves the new conversation region's reading anchor, or follows the bottom when already there, without replacing the textarea or its draft. File cards, previews, downloads, controls and approvals stay within the conversation region. Existing hostile-content rendering remains inert.

## Validation

- Real Chrome production assets: generated-file preview/download/reload, stable refresh/focus, empty-chat ownership clearing, and long messages with task/outcome controls.
- Composer geometry: input and Send remain inside 1440×1000, 1024×700, 700×700, 390×844 and 390×480 viewports at both scroll ends, without horizontal/page overflow. A short mobile viewport also keeps them visible with suggestions and saved routing receipts open.
- A direct live-message reconciliation retains a non-bottom reading position, draft and keyboard focus. Real Chrome hostile-content checks use the new conversation viewport for the minimum usable reading-area assertion; injection and overflow checks are retained.
- Reviewed embedded asset manifest updated to `6db2d6897519025eba657f9b0e97159999bdf68937b4bcc1dd6fdaa1b72da469`.
- `DARWIN_REQUIRE_CHROME=1 go test -race ./webui -count=1` ran all browser/shell checks. An old minimum-height assertion needed to target the new scroll region. The final broad run passed the remaining checks but exposed a fixture reconnect/viewport-resize race in the extended chat-file test, which replaced its synthetic messages during its scroll assertion. The fixture now suppresses unrelated EventSource reconnects and awaits resize layout before the explicit update. The affected test passed with `DARWIN_REQUIRE_CHROME=1 go test -race ./webui -run '^TestChromeChatFilesPreviewDownloadAndReload$' -count=1` (3.172s); unrelated successful checks were reused. This is not a claim that the final full-suite command passed or that full `make check` ran.
- `go vet ./webui`, `go run ./cmd/check`, native and Linux ARM64 application builds passed. Final diff/static checks recorded with the checkpoint.

Private screenshots and qualification artifacts are outside Git under `/Users/aj_lab/.NexusRouter/resources/chat-output-live-2026-10-14`. Physical mobile keyboard/browser chrome and OS screen readers were not exercised; short dynamic viewport geometry, accessible labels and real browser focus/keyboard interactions were checked. No provider, task-dispatch or approval policy changed. Deployment is pending the clean checkpoint, idle check and rollback backup.
