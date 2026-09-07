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
  "release_version": "1.0.0",
  "source_commit": "REPLACE_WITH_40_LOWERCASE_HEX_CHARACTERS",
  "tag": "v1.0.0",
  "release_title": "DarwinRouter v1.0.0",
  "tag_message": "DarwinRouter release v1.0.0",
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
      "name": "DarwinRouter_1.0.0_darwin_amd64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.0_darwin_arm64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.0_linux_amd64.tar.gz",
      "size": 1,
      "sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"
    },
    {
      "name": "DarwinRouter_1.0.0_linux_arm64.tar.gz",
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
