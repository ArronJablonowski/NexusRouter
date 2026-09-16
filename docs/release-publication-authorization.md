# GitHub publication authorization and offline preflight

Status: local release-control tooling for DAR-58. This procedure does not create
an approval, Git tag, GitHub draft, upload, release, signature, or network
request. Publication remains blocked until an authorized operator supplies and
independently identifies the exact canonical record described below.

## Authority boundary

Signing authorization deliberately leaves publication unapproved. After all
candidate, licensing, platform, signing, and independent-verification gates are
complete, the publication approver may author a separate canonical schema-1
record outside the repository and release directory. DarwinRouter provides a
strict parser and offline preflight, but no command that generates this record.

The record binds one `github.com` owner/repository, exact version and full source
commit, the exact `vVERSION` tag, fixed release title, exact release-notes body
digest, the exact annotated-tag message and tagger identity/time, every public
release-control digest, and the seven exact signed release
files. Its controls require draft-first, create-only publication into a repository
with immutable releases enabled. Those fields express approval requirements;
offline verification cannot prove the GitHub setting or remote state.

The following template is intentionally invalid until every placeholder is
replaced. JSON uses two-space indentation, the exact field order shown, and one
final LF. Assets are sorted by bytewise name. Sizes are decimal bytes and every
digest is `sha256:` plus 64 lowercase hexadecimal characters.

```json
{
  "schema_version": 1,
  "project": "DarwinRouter",
  "scope": "darwinrouter-github-publication-authorization",
  "github_host": "github.com",
  "repository": "OWNER/REPOSITORY",
  "release_version": "1.0.1",
  "source_commit": "REPLACE_WITH_40_LOWERCASE_HEX_CHARACTERS",
  "tag": "v1.0.1",
  "release_title": "DarwinRouter v1.0.1",
  "tag_message": "DarwinRouter release v1.0.1",
  "tagger": {
    "name": "REPLACE_WITH_APPROVED_TAGGER_NAME",
    "email": "REPLACE_WITH_APPROVED_TAGGER_EMAIL",
    "date": "YYYY-MM-DDTHH:MM:SSZ"
  },
  "prerelease": false,
  "make_latest": true,
  "release_notes_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "candidate_record_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "license_evidence_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "sha256sums_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "signature_file_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "trust_record_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "signing_authorization_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "assets": [
    {
      "name": "DarwinRouter_1.0.1_darwin_amd64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.1_darwin_arm64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.1_linux_amd64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.1_linux_arm64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "SHA256SUMS",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "SHA256SUMS.sig",
      "size": 129,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "manifest.json",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    }
  ],
  "controls": {
    "draft_first": true,
    "create_only": true,
    "immutable_required": true
  },
  "gates": [
    {
      "name": "signed_artifacts",
      "status": "approved"
    },
    {
      "name": "independent_verification",
      "status": "approved"
    },
    {
      "name": "publication",
      "status": "approved"
    }
  ],
  "approver_id": "idp:publication-approver",
  "publication_policy_url": "https://REPLACE_WITH_REVIEWED_PUBLICATION_POLICY",
  "approved_at": "YYYY-MM-DDTHH:MM:SSZ"
}
```

## Offline preflight

Obtain every expected digest and repository identity through the recorded
independent evidence channel. Do not calculate an expected value from the same
path being checked. Run from a trusted source checkout with network access
disabled if desired:

```sh
go run ./cmd/verify-publication-authorization \
  --dir /ABSOLUTE/SIGNED_RELEASE_DIRECTORY \
  --source /ABSOLUTE/CLEAN/TRUSTED/DarwinRouter \
  --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
  --candidate-record-sha256 sha256:EXPECTED_CANDIDATE \
  --license-evidence /ABSOLUTE/INDEPENDENT/LICENSE_EVIDENCE.json \
  --license-evidence-sha256 sha256:EXPECTED_LICENSE_EVIDENCE \
  --expected-sums-sha256 sha256:EXPECTED_SHA256SUMS \
  --trust-record /ABSOLUTE/INDEPENDENT/TRUST_RECORD.json \
  --trust-record-sha256 sha256:EXPECTED_TRUST_RECORD \
  --key-id EXPECTED_RELEASE_KEY_ID \
  --key-fingerprint sha256:EXPECTED_PUBLIC_KEY \
  --signing-authorization /ABSOLUTE/INDEPENDENT/SIGNING_AUTHORIZATION.json \
  --signing-authorization-sha256 sha256:EXPECTED_SIGNING_AUTHORIZATION \
  --publication-authorization /ABSOLUTE/INDEPENDENT/PUBLICATION_AUTHORIZATION.json \
  --publication-authorization-sha256 sha256:EXPECTED_PUBLICATION_AUTHORIZATION \
  --repository ArronJablonowski/DarwinRouter \
  --release-notes /ABSOLUTE/APPROVED/RELEASE_NOTES.md
```

`RELEASE_NOTES.md` above must be the create-only candidate-bound body produced
by `release-candidate notes`, not the committed unreleased template. Preflight
re-renders it from the independently verified candidate and clean source, then
requires exact bytes and the schema-3 candidate digest before accepting the
publication authorization.

Success returns one JSON line containing only public identities and the seven
observed files. Preflight performs the complete approval-bound signature and
source verification twice around exact asset observation. It rejects altered or
symlinked authority inputs, noncanonical JSON, missing or extra assets, target,
notes, repository, tag, commit, digest, control, and gate drift. It never opens a
credential source and contains no HTTP or Git mutation path.

Offline success is not publication and does not prove that GitHub immutable
releases are enabled, that a tag/release name is unused, or that remote bytes
match. DAR-56 still requires a separately authorized create-only draft, upload,
readback, and publish ceremony. DAR-57 must independently re-download and verify
the immutable release and its exact tag and assets afterward.

## Authorized one-shot publication

Only after every preceding gate is approved, run `publish-release` with the same
inputs used by the offline preflight. It re-runs that preflight twice and derives
the repository, tag, commit, notes, and exact seven uploaded files exclusively
from the authorization. GitHub API origins are fixed in the binary; redirects,
custom endpoints, replacement, and retry after uncertain mutation are rejected.

The credential must arrive through a pipe, socket, or private regular file on an
already-open file descriptor, never a command argument or interactive terminal.
Standard input is descriptor `0`; a different descriptor can be named with
`--credential-fd`. A regular credential file is accepted only when its mode has
no group or other permissions. The source is single-use and is not read until the
closing offline preflight succeeds. For example, append the following flags to
the complete preflight arguments shown above:

```sh
YOUR_TRUSTED_SECRET_COMMAND | \
  go run ./cmd/publish-release \
    --dir /ABSOLUTE/SIGNED_RELEASE_DIRECTORY \
    --source /ABSOLUTE/CLEAN/TRUSTED/DarwinRouter \
    --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
    --candidate-record-sha256 sha256:EXPECTED_CANDIDATE \
    --license-evidence /ABSOLUTE/INDEPENDENT/LICENSE_EVIDENCE.json \
    --license-evidence-sha256 sha256:EXPECTED_LICENSE_EVIDENCE \
    --expected-sums-sha256 sha256:EXPECTED_SHA256SUMS \
    --trust-record /ABSOLUTE/INDEPENDENT/TRUST_RECORD.json \
    --trust-record-sha256 sha256:EXPECTED_TRUST_RECORD \
    --key-id EXPECTED_RELEASE_KEY_ID \
    --key-fingerprint sha256:EXPECTED_PUBLIC_KEY \
    --signing-authorization /ABSOLUTE/INDEPENDENT/SIGNING_AUTHORIZATION.json \
    --signing-authorization-sha256 sha256:EXPECTED_SIGNING_AUTHORIZATION \
    --publication-authorization /ABSOLUTE/INDEPENDENT/PUBLICATION_AUTHORIZATION.json \
    --publication-authorization-sha256 sha256:EXPECTED_PUBLICATION_AUTHORIZATION \
    --repository ArronJablonowski/DarwinRouter \
    --release-notes /ABSOLUTE/APPROVED/RELEASE_NOTES.md \
    --journal /ABSOLUTE/PRIVATE/NEW-publication-operation.jsonl \
    --credential-fd 0
```

The token requires repository Contents write and Administration read access.
Success emits public publication evidence. A failure after mutation may also emit
public uncertain-state evidence and always leaves the durable journal for manual,
read-only reconciliation. Never retry merely because the command returned an
error; first reconcile the exact authorization, repository, and tag.

## Independent post-publication verification

After publication, a different operator should run
`verify-published-release` with every evidence flag from the offline-preflight
example above, plus the verifier identity and two new paths:

```sh
go run ./cmd/verify-published-release \
  [ALL OFFLINE-PREFLIGHT FLAGS FROM THE EXACT APPROVED RELEASE] \
  --verifier-id idp:INDEPENDENT_RELEASE_VERIFIER \
  --download-dir /ABSOLUTE/EXISTING/PARENT/NEW-verified-downloads \
  --out /ABSOLUTE/EXISTING/PARENT/NEW-post-publication-receipt.json
```

The command uses a fixed `https://api.github.com` origin and a proxy-free,
credential-free transport. It requires an immutable non-draft release and an
annotated tag that peels to the authorized commit, downloads the exact seven
assets into a new directory, compares both GitHub's and local digests, and runs
the approval-bound release verifier again against those downloaded bytes. It
then writes one canonical receipt with create-only semantics and prints that
receipt's `sha256:` digest. The receipt path must be outside the source,
approved signed-release, and download directories. `--verifier-id` is required,
is recorded in the receipt, and must identify the independent operator who ran
the check. The receipt also binds the code-fixed, versioned verifier policy
`darwinrouter-github-post-publication-verification/v1`; it cannot be selected by
the operator. The verifier identity must differ from the publication approver
bound into the canonical publication authorization; matching identities are
rejected before any GitHub request. The current authorization schema does not
encode the credentialed publication executor, so operators must separately
retain that identity and confirm the verifier is also independent of it.

Missing or mismatched remote state creates no receipt. A late local persistence
failure can leave a receipt file whose durability is uncertain; inspect it and
verify its canonical bytes before deciding whether any retry is safe. This
read-only observation is required evidence for DAR-57/DAR-60 but is not itself
publication approval, rollback authorization, or proof that future remote bytes
will remain unchanged.

The independent verifier must then run the host-matching archive directly from
a fresh directory containing the exact receipt-bound downloaded byte set with
`verify-published-install`, following
[post-publication verification](release-post-publication-verification.md). Its
create-only canonical record binds the actual installed binary and version
output to this receipt and the final native install/migration rehearsal. A
digest-only comparison without execution is not published-byte installation
evidence.
