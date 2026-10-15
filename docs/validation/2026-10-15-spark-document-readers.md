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

## Pending qualification and deployment

Scanned/mixed PDF OCR, real antiword extraction, page/output limits with installed dependencies and post-install service verification remain pending package installation. The candidate is staged privately; it is **not deployed**. Installation must preserve current configuration/trust/model services, retain private rollback/state backups and recheck an idle boundary before replacing the router. No full-suite, live OCR, Linux DOC or completed-deployment claim is made.
