# Files and images in chat

Generated outputs appear in the chat conversation as image previews and file cards with **Download** buttons. Image cards also open a larger preview. The list updates while the chat is open, survives refresh/reopening, and clears when changing chats. Refresh preserves existing preview elements and keyboard focus.

The authenticated Web UI collects only confirmed `create_file`, `replace_file` and `comfy_submit` results belonging to that chat. Text files use the exact new content retained in the confirmed tool-call journal; old replacement preimages and recovery paths are never exposed. ComfyUI jobs must match the original owned submission key and prompt ID. Collection observes existing work; it never submits, retries, cancels or approves a job. Assistant-written paths, Markdown links, arbitrary local files and job inspection results grant no attachment authority.

Outputs are copied into a private, immutable SQLite snapshot store at `<telemetry.database>.chat-files/files.db`, beside the task database. Opaque file IDs are bound to chat and source; filenames are display/download names, never retrieval paths. Text outputs containing known configured credentials are withheld. Saved snapshots remain downloadable when ComfyUI is unavailable or disabled. Include this directory in normal consistent state backups.

## Formats and limits

PNG, JPEG and WebP have inline previews. Other currently generated files (approved UTF-8 creation/revision, including Markdown, CSV, JSON and HTML) appear as downloadable file cards. Documents are forced downloads with `nosniff` and sandbox headers; HTML/SVG never execute inside the application. Original generated content is retained, rather than a later read of a mutable workspace file.

Collection is bounded to100 saved files and256 MiB of payload per chat,32 MiB per attachment and512 MiB total payload; the source task scan covers the latest100 tasks. At limits or while an upstream source is unavailable, already saved files remain usable; collection may remain incomplete. No automatic deletion/GC, upload control, arbitrary filesystem publishing, inline PDF/document rendering, or audio/video player is included. General binary file-generation tools are not implemented by this increment.

ComfyUI output history must be available for the first successful collection. Closing the chat before collection and losing upstream history can leave an uncaptured output unavailable; no uncertain job is replayed to recover it. CLI-created files/jobs outside a chat are not silently imported. Existing credential redaction can prevent exact recovery of source text; known-secret outputs are withheld. Private runtime envelopes, prompts and hidden tool results are not attachment sources except the narrowly allowlisted confirmed creation records.

## Browser interface

Signed-in reads use `/app/api/v1/chats/{chat}/files`, and each listed ID has `/files/{file}/preview` and `/files/{file}/download` under that chat route. All reads retain existing host/session checks and reject query-selected paths, invalid IDs, unsupported previews and cross-chat retrieval. Files are private to the existing single-operator browser authority; this does not introduce multi-user sharing or public links.
