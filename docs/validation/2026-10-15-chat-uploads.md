# Chat uploads validation — 2026-10-15

Implemented file selection/removal and drag/drop, a typed optional upload field on authenticated chat submit/resume, bounded text/PDF/DOCX/image processing, real Ollama/OpenAI-compatible image delivery, durable original-byte downloads and file-aware follow-ups preserving the source model. Upload files remain data, never new tool authority. See docs/chat-uploads.md for limits and recovery/storage details.

## Verified source checkpoint

- Race suites: attachments 1.254s; document extraction 1.156s; chat-file store 1.690s; complete provider adapters 2.784s; complete Web UI backend 6.509s. This covers body/CSRF/auth bounds, provider wire images and existing boundaries.
- Complete Web UI race suite passed after the strict source-size guard caught and corrected an over-limit composer file: 35.230s. Real Chrome upload tests exercised mixed file selection, removal, exact encoded bytes, definitive-rejection draft preservation, new request identity on explicit retry, hostile filenames/text, stable expandable previews and narrow viewport/pinned send controls. Embedded manifest reviewed: `a86b1399bdf493c5fac6c034cb3658b062822e129a95203c83a5c46a1c1b9be1`.
- Focused CLI race suite: browser/serve/approval/settings plus parser command, 13.358s.
- Broader app selection ran 154.628s. All selected tests except two dispatcher fixtures passed. `TestDispatcherQueuedRestartExecutesIdempotentSubmissionOnce` and `TestDispatcherQueuedFollowUpWaitsForRunningSource` failed before task creation (`admission_denied` / provider not started). Both reproduce identically against unmodified commit `582be09b` exported separately (20.537s); their fixture models omit context metadata. No full app/full-suite pass is claimed.
- Focused new upload/application tests plus immutable input binding passed under race detection. Final durable pre-queue binding/read-only submission-key changes: telemetry 2.991s, app 5.972s, chat-file store 1.296s. Tests cover original bytes, chat isolation, request replay/content changes, actual dispatcher execution, inherited follow-up model, persisted originals across new store handles, malformed files and rejecting image requests for unsupported/automatic model selection.
- Go formatting/source-size check, scoped vet, native macOS and Linux ARM64 builds passed. Built parser command read the synthetic PDF reference from stdin. The local Ollama model reports vision capability. Dependency versions and BSD license retained.
- The initial new app test stalled because its test harness queued work without starting a dispatcher; its stack was inspected, an owned test process stopped, and the fixture corrected with a dispatcher and explicit timeout. No production task was canceled or replayed.

## Pending live verification

The checkpoint does not yet claim installed-service upload or vision success. Planned live checks use only synthetic CSV/PDF/DOCX/PNG and Markdown follow-up content, verify facts not disclosed in the prompt, image color/shape recognition, original-byte browser downloads, chat reload and narrow layout. Existing production manual-approval timeout remains a separate known gap and is not exercised by this read-only inference test. Linear requirements remain unavailable pending reauthentication. Main task schema is unchanged; new artifact binding metadata is content-free and read-only at collection.
