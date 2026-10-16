# Real Spark Web UI document chats — 2026-10-15

## Scope and result

Two real authenticated Chrome chat submissions passed using the installed Spark Linux ARM64 application (`5ca30c0b`), its installed Linux document readers and actual Spark Ollama models. Each uploaded a scanned invoice PDF, mixed searchable/scanned PDF and genuine binary Word `.doc` through the native file picker. No extracted reference values were supplied in the prompts.

| Explicit model | Verified answer | Browser downloads |
|---|---|---|
| Qwen `qwen3.8:27b` (`qwen3-8-27b`) | Correct scanned invoice reference, both mixed-document references and legacy Word reference | All 3 match original bytes |
| Muse Glimmer `muse-glimmer:30b-q4_K_M-dflash` (`muse-glimmer-30b-q4-k-m-dflash`) | All four requested references correct | All 3 match original bytes |

Saved Spark task events independently identify the exact producing models above and provider `spark-ollama`; both original submissions succeeded. Each was dispatched exactly once. No cloud provider, model substitution, upload-script execution or tool call was used. The model-cost display recorded $0.000000 USD as an estimate; electricity is not included.

Both chats retained their three file cards and expandable uploaded text after reload. Each passed 390 px viewport checks with no horizontal page overflow and visible pinned Send control. Desktop and narrow screenshots were inspected. Downloads were initiated through the actual authenticated Download controls and compared byte-for-byte, not merely checked for successful HTTP status. Synthetic fixtures, original operation identities, screenshots and download hashes remain private outside Git under `resources/spark-web-upload-qa-2026-10-15`.

## Host and isolation details

The commander's existing `@Spark` remote-task control explicitly blocks file attachments. This test did **not** enable that separate cross-host upload feature or claim it works. To exercise Linux extraction and inference together, ran the installed Spark executable's own authenticated Web UI on loopback port 17789 behind a private SSH tunnel. Chrome ran on the commanding Mac. Spark performed document preparation, persistence and model inference.

A temporary user service used a private QA database/configuration derived from Spark's existing model configuration, with scheduling, tools, memory and skill learning disabled for the test. It did not alter the serving router's configuration, trust or credentials and did not replace its model services. The normal browser challenge/CLI approval/session flow was used with a new temporary QA credential, never logged or committed. Temporary configuration and test scaffolding remain outside the source repository.

Both browser sessions were explicitly logged out and their temporary Chrome profiles removed. Once both QA submissions were terminal, stopped only the owned temporary Web UI unit and SSH forward and removed its API credential. Production `nexusrouter-remote.service` retained PID 3030862 with zero automatic restarts; Ollama retained PID 2187152. No production deployment/restart was needed because this test found no additional parser/download defect.

## Limits

This verifies two real Spark model chats over a bounded English synthetic corpus; it does not establish universal OCR/model accuracy, non-English or handwriting support, a fresh full-suite pass, or attachments through the commander's remote-task control. Linux reader failure, limit and cleanup cases are separately recorded in [reader qualification](2026-10-15-spark-document-readers.md); broader Web UI/security regression checks are in [upload QA](2026-10-15-upload-qa.md).

This checkpoint changes documentation only. Reviewed the diff and ran `git diff --check`; unchanged source/build validations were reused. No sensitive runtime state or generated binaries are included.
