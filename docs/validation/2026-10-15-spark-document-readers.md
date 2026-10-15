# Spark document reader verification — 2026-10-15

## Confirmed deployment gap

On paired `dgx-spark` (`spark-9c8a`, Linux ARM64), Poppler is installed (`poppler-utils` 24.02.0-1ubuntu9.9), but `/usr/bin/tesseract`, `/usr/bin/antiword` and English Tesseract language data are absent. The existing remote router remains active with zero automatic restarts. This is a dependency provisioning gap; no parser-code defect has yet been established.

The Spark operator account cannot run package installation without a sudo password. A user action was requested, without requesting or reading that password:

```sh
sudo apt-get install --no-install-recommends tesseract-ocr tesseract-ocr-eng antiword
```

Do not work around this by adding document-controlled executable paths or weakening helper discovery. After installation, run the candidate through the complete synthetic reader corpus before replacing the serving executable.

## Completed verification

Clean source `5ca30c0b28cb5c4b8fb79e43008afaa69c34aba6` was cross-built with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath`. Build metadata records `vcs.modified=false`. The transferred candidate's SHA-256 is `73f93f9eefdd8291f8822e991903eb7753f53130594ba76e7434177098ecaae1`; it validates Spark's existing runtime configuration. Previously recorded unchanged source checks from the upload QA report are reused, not claimed as newly run Linux race checks.

Actual Spark execution of cross-compiled test executables:

- Complete documenttext test binary: passed, including DOCX bounds/line breaks, native PDF rejection, mixed native/image preservation and fuzz seeds. Its macOS-only native DOC generator test explicitly skipped; this does not establish Linux DOC extraction.
- App `^TestDocumentProcess` tests: passed owned-group cancellation and cleanup after parser crash (including descendant termination). These are actual Linux executions without race instrumentation.
- Candidate `internal-file-text pdf` on a synthetic native PDF: exit 0, expected reference, 37 bytes, no leftover temporary entries.
- Candidate on a scanned PDF: exit 2 (dependency unavailable), zero output and no leftover temporary entries.
- Candidate on a genuine binary DOC: exit 2 (dependency unavailable), zero output and no leftover temporary entries.

Synthetic fixtures, executables and private logs remain outside Git in the task's local private QA directory and Spark's `reader-qa-2026-10-15` directory. No private runtime document content was read. Aggregate maintenance state showed zero nonterminal submissions, queued/running task heads and unreleased leases. Existing router, Ollama and other services were not stopped or changed.

## Initial qualification gap (resolved below)

Scanned/mixed PDF OCR, real antiword extraction, page/output limits with installed dependencies and post-install service verification remain pending package installation. The candidate is staged privately; it is **not deployed**. Installation must preserve current configuration/trust/model services, retain private rollback/state backups and recheck an idle boundary before replacing the router. No full-suite, live OCR, Linux DOC or completed-deployment claim is made.

## Completed Linux qualification and deployment

The operator installed the requested system packages. Fixed-path discovery now finds `/usr/bin/tesseract`, `/usr/bin/antiword` and `/usr/bin/pdftoppm`; Tesseract 5.3.4 / Leptonica 1.82.0 reports English and orientation data available. No application workaround or weakened discovery was necessary. The confirmed issue was missing dependencies, not a new parser defect.

The candidate passed all **13/13** synthetic Linux reader cases, then the installed canonical executable passed all **13/13** again:

- Native PDF and 40-page native PDF accepted; 41 pages rejected.
- Scanned invoice, 10-page scan and mixed searchable/scanned PDF returned the expected undisclosed references and OCR uncertainty labels. Mixed output retained both searchable and recognized text. The installed 10-page scan took 2.505s.
- 11 OCR pages rejected in 2.506s with no partial output.
- Genuine binary Word DOC accepted with the expected reference; text renamed DOC and corrupt OLE rejected.
- Encrypted, blank and truncated PDFs rejected.
- Every case left its owned temporary directory empty. Every expected failure emitted zero bytes.

At a verified idle boundary (zero nonterminal submissions, active task heads and unreleased leases), stopped only `nexusrouter-remote.service`. Retained its previous executable, configuration, service definition and private state; copied SQLite databases passed `quick_check`. Backup: `/home/arron_spark/.local/share/nexusrouter/deployment-backups/20261015T145131Z-5ca30c0b-readers`. Atomically installed the qualified clean `5ca30c0b` build with the SHA-256 above, retaining the rollback path on failure.

The remote router is active with zero automatic restarts and new PID 3030862. Existing configuration hashes are unchanged; Ollama retained PID 2187152. No model service, trust, credential or automatic-approval setting changed. A paired authenticated `remote info` request initially failed while the router was settling; a subsequent read-only request succeeded with `hostname=spark-9c8a`, `available=true`, targeting/conversation version 1. No task was dispatched or replayed for this verification.

Linux reader execution and deployment are now qualified for this bounded English synthetic corpus. These checks exercised the installed reader subprocess and Linux owned-process cleanup, not a fresh Spark-hosted Web UI upload/LLM session. The commander's full browser upload/download flow is separately verified in the upload QA report. Non-English/handwriting accuracy, universal hostile-document safety and full-suite qualification remain outside the claim. Private result journals and deployment receipts remain outside Git.
