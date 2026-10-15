# Commander remote-task attachments — 2026-10-15

## Implementation

The Web UI's explicit paired-host chat control now forwards selected text/code/data, PDF and Word files to a bounded preparation step on the commander. The existing document readers and structured configured-credential redaction produce an untrusted text frame for the remote task. Original bytes stay in a private commander store for authenticated request-scoped downloads; remote protocol/body/context limits and model/private/zero-cost policies are unchanged. Remote images are explicitly rejected until vision transport is qualified.

Original intent includes all file bytes, task fields, host and request identity, bound before any network dispatch. Changes to or omission of originals cannot reuse a request key, including when different originals would extract to the same text. Successful receipts clear only the submitted file draft; definite preparation rejection retains it. Unknown outcomes retain the saved request identity, without automatic replay. The host/request URL fragment restores status/results/original downloads after refresh. Downloads never execute as application-origin documents.

## Source validation

- Full race packages: webuiapp 9.177s, chatfiles 1.542s, attachments 1.240s, remote 54.983s.
- Scoped app/CLI race checks passed: app 28.575s, CLI 6.288s; upload/redaction, submission/recovery, reader/process, serving and remote boundaries.
- Initial browser gate caught positional cancellation-control breakage when inserting the new file panel. Preserved existing control ordering; complete Web UI race suite then passed in 33.689s. Authority assertions were retained. Extended the real Chrome host fixture for file payloads, draft retention on definitive rejection, accepted draft clearing and uncertain no-replay behavior.
- mTLS backend fixture verifies request-bound downloads, unauthorized/wrong-request denial, unchanged-byte retrieval, changed/omitted file rejection and fresh trust revocation. Concurrent original-byte binding test admits only one of two changed-byte requests under a shared identity, even with identical extracted text. Structured credential scrubbing and unsupported image/malformed DOC rejection tested separately.
- Embedded asset manifest reviewed: `d0938f7e34cb077a2bedc99452661c381015beee70511b3d12f188602928b0bc`.
- Source formatting/size, scoped vet and native/Linux ARM64 builds passed. Prior successful backend checks are reused after the browser-only ordering correction. These are scoped checks, not `make check` or MVP qualification.

Commands: `go test -race ./internal/webuiapp ./internal/chatfiles ./attachments ./remote ./webui -count=1 -timeout=5m`; final browser rerun `go test -race ./webui -count=1 -timeout=3m`; scoped app/CLI `-run 'Test(RemoteUpload|Upload|BrowserUploads|DocumentProcess|DocumentExtract|BrowserRemote|Remote|Serve|Submission|ResumeSubmission)'`; `go run ./cmd/check`; `go vet ./internal/app ./internal/webuiapp ./internal/cli ./webui`; native and Linux ARM64 `go build -trimpath ./cmd/nexus`.

Live commander-to-Spark verification and deployment follow this source checkpoint. Synthetic fixtures, credentials, operation identities and raw logs remain outside Git. Linear authentication remains unavailable. Storage cleanup, remote image inputs and resumable remote chat histories remain outside this feature's scope.

## Candidate live verification and layout correction

A private isolated commander candidate dispatched exactly one Qwen 3.8 27B request to the actual paired Spark. It returned all expected scanned/mixed PDF and binary DOC references; all three original browser downloads matched bytes and the saved remote request recovered after reload. A separate malformed DOC request returned 422, retained its draft and did not dispatch a remote task. Original operations were never resent.

Visual QA then found that the legacy remote receipt's three-rem composer scroll box made answers and the new downloads difficult to use. Moved the receipt and its New remote task control into the scrollable conversation area, preserving pinned input/Send. Added a Chrome placement assertion; the complete Web UI race suite passed again in 33.318s. The browser observer's cleanup also needed a fresh CSRF grant after normal status polling evicted its original bounded grant; that private observer was corrected without retrying inference. Candidate shutdown revokes its remaining session. Installed verification follows.

Final input-boundary review enforces the encoded remote-envelope ceiling for both uploaded and ordinary tasks; JSON escaping cannot admit an oversized transport body. Added a regression for that boundary; the complete webuiapp race suite passed again in 7.079s. Static checks and both builds passed on this final source. Reopened the original Qwen candidate request with the corrected result placement and re-downloaded all originals without another dispatch; normal fresh-grant logout succeeded.
