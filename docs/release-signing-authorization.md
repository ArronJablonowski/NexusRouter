# Release signing authorization record

Status: verifier contract for an external operator record. NexusRouter does
not generate this record because software must not fabricate approval.

Before a production signing operation, an authorized operator must create a
canonical schema-2 JSON record outside the repository and release directory.
The record binds the exact candidate-record, candidate license-evidence,
`SHA256SUMS`, and public trust-record SHA-256 digests; key ID and fingerprint;
all four supported target decisions; approved project-license,
third-party-notice and production-signing gates; approver identity; the
authenticated HTTPS release-policy reference; and a UTC whole-second approval
time. Its publication gate must remain `unapproved`: authorization to sign does
not authorize tagging, uploading, or publishing.

The record uses two-space JSON indentation, the field order implemented by
`SigningAuthorization`, and one final LF. Digests use `sha256:` followed by 64
lowercase hexadecimal characters. Targets appear exactly in this order:
`darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`; every decision is
`supported`. Gates appear exactly as `project_license: approved`,
`third_party_notices: approved`, `production_signing: approved`, and
`publication: unapproved`.
The approver identifier is 3-128 characters, begins and ends in a lowercase
letter or digit, and otherwise uses only lowercase letters, digits, `.`, `_`,
`:`, `@`, `/`, or `-`. It identifies the external approval principal; it is not
free-form display text.

Use this field order and replace every placeholder. The template is
intentionally invalid until an authorized operator supplies the reviewed
digests, principal, policy and time:

```json
{
  "schema_version": 2,
  "project": "NexusRouter",
  "scope": "nexusrouter-release-signing-authorization",
  "candidate_record_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "license_evidence_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "sha256sums_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "trust_record_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "key_id": "release-YYYY-NN",
  "key_fingerprint": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "targets": [
    {
      "os": "darwin",
      "arch": "amd64",
      "decision": "supported"
    },
    {
      "os": "darwin",
      "arch": "arm64",
      "decision": "supported"
    },
    {
      "os": "linux",
      "arch": "amd64",
      "decision": "supported"
    },
    {
      "os": "linux",
      "arch": "arm64",
      "decision": "supported"
    }
  ],
  "gates": [
    {
      "name": "project_license",
      "status": "approved"
    },
    {
      "name": "third_party_notices",
      "status": "approved"
    },
    {
      "name": "production_signing",
      "status": "approved"
    },
    {
      "name": "publication",
      "status": "unapproved"
    }
  ],
  "approver_id": "idp:release-approver",
  "release_policy_url": "https://REPLACE_WITH_REVIEWED_POLICY_LOCATION",
  "approved_at": "YYYY-MM-DDTHH:MM:SSZ"
}
```

The signing ceremony must supply the record's exact SHA-256 digest independently
of the record itself, along with independently established expected values for
the candidate digest, license-evidence digest, checksum digest, trust-record
digest, key ID, and public-key fingerprint. `ReadSigningAuthorization` rejects
a missing or mismatched input, noncanonical or unknown data,
duplicate/reordered fields, non-HTTPS policy locations, fractional/local
timestamps, symlinks, replacements, incomplete target decisions, unapproved
license gates, or any attempt to grant publication authority.

This parser does not prove that the named approver was authorized, that the
policy URL was retrieved, or that an approval process occurred. Those are
operator and organizational controls. Retain the canonical record, its digest,
identity-provider audit evidence, policy snapshot, candidate/checksum/trust
records, and final ceremony log in the private release evidence set. Do not put
private signing material or credentials in the record.

The production signer additionally requires this record's `release_policy_url`
to equal the independently trusted record's `release_policy_url` exactly before
it opens the private seed. A syntactically valid authorization pointing at a
different policy is not signing authority.
