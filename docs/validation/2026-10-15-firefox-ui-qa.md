# Firefox and responsive Web UI QA — 2026-10-15

## Reproduced defects and fixes

1. **Field boundaries lost contrast in the shared styling update.** The shared field border `#345e69` measured about 2.46:1 against the panel and 2.63:1 against the field background, below the 3:1 non-text contrast target. Changed text fields/selects to `#578390`. Updated the static checks to current colors and added computed browser checks for visible enabled field boundaries; disabled controls and borderless composer text are handled separately.
2. **Firefox could retain the previous stylesheet during live updates.** Detecting a new ETag and cloning a link with the same URL did not reliably apply the new CSS in Firefox. The replacement link now uses a bounded, encoded revision query on the same authenticated CSS endpoint. CSP and authorization are unchanged. Firefox reproduced the failure before the fix. The regression test first fails the new CSS download, checks that the old sheet/draft remain, then restores delivery and verifies the updated CSS, draft, focus and original document survive without reload.

## Repeatable browser coverage

Added an isolated Firefox WebDriver BiDi harness with no external automation dependency. It reuses 24 existing same-origin API fixture scenarios and assertions: all pages; uploads and rejection recovery; exact download bytes and inline images; sticky header/composer; ComfyUI uncertainty fences; late approvals; chat rename/pin; explicit cloud registration; dirty settings; live styles/stats; chat/workboard failure recovery; Spark history and CSRF; routing; jobs; inspector; Cron; collaboration; dependencies; logging; remote models/odometers; discovery and advertisement.

All 15 pages are checked at 1440, 1024, 768, 390 and 320 CSS pixels. Chat viewport checks additionally include 320×568, 390×480 and 844×390, with long messages, scrolling, retained drafts and routing details. Checks cover page overflow, hidden-state preservation, accessible button names, minimum control dimensions, keyboard focus, reduced motion, enabled opaque button text contrast and field boundaries. Private Firefox narrow-page screenshots were inspected.

Firefox 157.0.1 and Chrome 155.0.8059.85 were used. On this macOS host direct Firefox execution is denied access to its app-data directory; the harness launches a new instance through LaunchServices with an owned temporary profile and loopback-only debugging. Only test-profile focus/tab-navigation/reduced-motion preferences are set; the user's browser/profile/security configuration is untouched. A real content pointer action establishes page focus before Tab probes, preventing Firefox's URL bar from invalidating later focus probes. Unsupported automation operations fail rather than silently skipping assertions. Firefox download handling verifies actual bytes, and file uploads use BiDi file selection.

## Verification

- `NEXUS_REQUIRE_FIREFOX=1 DARWIN_REQUIRE_CHROME=1 NEXUS_UI_QA_SCREENSHOTS=/tmp/nexus-final-ui-qa go test -race ./webui -count=1 -timeout=5m`: passed, **89.482s**, including all 24 Firefox scenarios and the Chrome suite.
- `go test -race ./internal/webuiapp -count=1 -timeout=3m`: passed, **8.178s**.
- `go run ./cmd/check`, `go vet ./webui`, native `go build -trimpath -buildvcs=false`, and `git diff --check`: passed.
- Reviewed embedded asset manifest: `66b02b56510500142e319475215359ebe0d367da0d95e390d361bd4481b8a68e`.
- Native build SHA256: `e961a594c827db47de43a30a47653f611fdb32bee3fb7f76b6f4d5671e0c2ca9`.

Deployment and installed Firefox verification pending. No full `make check`, Safari, physical mobile/virtual-keyboard, formal accessibility certification, new OCR/model inference or live destructive-operation claim. Fixtures isolate mutations from real settings, model services and jobs. Browser protocol references: [Mozilla remote agent](https://firefox-source-docs.mozilla.org/remote/index.html), [BiDi connection](https://developer.mozilla.org/en-US/docs/Web/WebDriver/How_to/Create_BiDi_connection). Private profiles, session material, downloads and screenshots are excluded from Git.
