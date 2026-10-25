# Shared Web UI polish — 2026-10-15

Applied the remote-chat control treatment to shared buttons, secondary/destructive actions, file pickers/downloads, navigation, form fields, cards, dialogs, badges and headings. Reduced grid/glow decoration on the main shell while retaining routing visualization and warning distinctions. Header/action groups wrap on smaller screens. Existing page routes, DOM identities and application operations remain intact. The send control retains its accent treatment.

Cross-page verification found an existing hidden upload-list container could acquire a layout box from its display rule. A shared `[hidden]` rule now preserves application-hidden state regardless of layout styling.

Validation on the changed source:

- `go test -race ./webui -count=1 -timeout=3m`: passed, 39.971s. Includes populated browser interaction coverage for models, settings, ComfyUI, workboards, approvals, uploads, downloads, remote chat and refresh/recovery boundaries.
- New Chrome test visits all 15 navigation pages at 1440, 768 and 390px with unavailable API fixtures. Checks overflow, page selection, hidden controls, button names/dimensions, keyboard focus, reduced motion and enabled opaque button text contrast (4.5:1). Refresh/reload coverage remains in the full Web UI suite. This is bounded accessibility coverage, not a complete accessibility audit.
- Private desktop/narrow fixture screenshots inspected for settings, models, ComfyUI and stats.
- `go run ./cmd/check`, `go vet ./webui`, native `go build -trimpath -buildvcs=false` and `git diff --check`: passed.
- Embedded asset manifest reviewed: `4e8334690aa01d5739269187d14ab2b73008bf0238ec9130f1cf923c8a998813`.
- Native build SHA256: `9e7232ad7f92e721cfc7db202910a02c754edb075133b213deeedba9a6d5ac71`.

Source `8606ca82` deployed to the commander at an idle boundary with private binary/config/state rollback backup. Installed binary SHA256 matches the verified candidate. Authenticated read-only Chrome checks passed all 15 installed pages at 1440 and 390px, plus settings reload. A separate check waited for populated Models inventory and passed both widths; private installed screenshots for models, settings and routing were inspected. No settings saves, model selection changes, inference dispatch or runner start/stop; owned browser sessions logged out.

The first session challenge timed out during the already-known large-store startup delay; service readiness was confirmed before a new verification session. This startup performance gap remains. The installed request monitor initially classified read-only model-use and runner-status POSTs as unexpected; source/body inspection confirmed those read paths, and the monitor was corrected to permit only version-only model-use reads and explicit runner status requests. The final checks passed. No full application-suite, Firefox-specific or new inference claim. Linear requirements could not be retrieved because the connector requires reauthentication; local PRD and explicit user scope used. Private runtime/configuration and screenshots are excluded from Git.
