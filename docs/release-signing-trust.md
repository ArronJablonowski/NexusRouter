# Release-signing identity and trust policy

Status: operator runbook for DAR-53. Repository tooling validates public records
and signed bytes; it does not provision, discover or control a production private
key. Completing this document is not release authorization.

## Trust boundary

DarwinRouter releases use a dedicated Ed25519 identity. Never reuse the Git SSH
key, an account-login key or an employee's general-purpose identity. The 32-byte
private seed stays outside the source checkout, release directory, CI logs and
shell history in operator-controlled secret storage. At least two named operators
must be able to complete recovery or revocation without copying the seed into the
repository.

The public trust record does not authenticate itself. Publish it through an
authenticated channel independent of the release artifact, record its exact URL
and exact-byte SHA-256 digest in the release checklist, and have a second operator
retrieve it through that channel. A trust record shipped only beside replaceable
release bytes provides no independent trust anchor.

## Canonical public trust record

The record is canonical schema-1 JSON: two-space indentation, fields in the order
below and one final LF. `public_key` is the 32 raw public-key bytes as 64 lowercase
hex characters. `public_key_sha256` is `sha256:` followed by the lowercase SHA-256
hex digest of those decoded bytes. `published_at` is UTC with whole seconds. Both
URLs must be authenticated HTTPS locations without credentials or fragments.

```json
{
  "schema_version": 1,
  "project": "DarwinRouter",
  "scope": "darwinrouter-release-signing",
  "key_id": "release-YYYY-NN",
  "algorithm": "Ed25519",
  "public_key": "REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "public_key_sha256": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
  "status": "active",
  "published_at": "YYYY-MM-DDTHH:MM:SSZ",
  "release_policy_url": "https://REPLACE_WITH_REVIEWED_POLICY_LOCATION",
  "rotation_revocation_url": "https://REPLACE_WITH_REVIEWED_STATUS_LOCATION"
}
```

The template is intentionally invalid until every placeholder is replaced. Key
IDs use only lowercase letters, digits, period, underscore and hyphen, start and
end alphanumerically, and contain at most 64 characters. Parsers reject unknown,
duplicate, reordered or noncanonical fields. A record with `status: revoked` is
valid archival data but cannot verify a release.

## Provisioning ceremony

1. Approve the custodian, backup custodian, signing environment, secret-storage
   mechanism, key ID, release policy and incident owner in a private operational
   record. Do not paste private material into the checklist.
2. Generate a new release-only Ed25519 identity in the approved secret-storage
   environment. This repository deliberately provides no production key generator.
3. Export only the raw 32-byte public key. Two operators independently derive and
   compare its SHA-256 fingerprint, the canonical trust record and the SHA-256
   digest of those exact record bytes.
4. Publish the record at the approved independent channel. Retrieve it afresh and
   validate its exact-byte digest before approving that channel as the trust
   anchor. Obtain the expected record digest, key ID and key fingerprint through
   a channel separate from release artifacts and record where each came from.
5. Conduct a disposable candidate signing rehearsal. The signer receives an
   already approved, quiescent artifact directory; it does not build or publish.
6. A different operator verifies the full approval-bound release with the
   independently retrieved candidate, authorization and trust records, every
   exact expected digest, and an independently trusted clean source checkout:

   ```sh
   go run ./cmd/verify-approved-release \
     --dir /ABSOLUTE/QUIESCENT/RELEASE_DIRECTORY \
     --source /ABSOLUTE/PATH/TO/INDEPENDENT/CLEAN/DarwinRouter \
     --candidate-record /ABSOLUTE/INDEPENDENT/CANDIDATE.json \
     --candidate-record-sha256 sha256:REPLACE_WITH_CANDIDATE_SHA256 \
     --expected-sums-sha256 sha256:REPLACE_WITH_SHA256SUMS_SHA256 \
     --trust-record /ABSOLUTE/INDEPENDENT/TRUST_RECORD.json \
     --trust-record-sha256 sha256:REPLACE_WITH_RECORD_SHA256 \
     --key-id release-YYYY-NN \
     --key-fingerprint sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS \
     --authorization-record /ABSOLUTE/INDEPENDENT/SIGNING_AUTHORIZATION.json \
     --authorization-record-sha256 sha256:REPLACE_WITH_AUTHORIZATION_SHA256
   ```

7. Record only public identifiers, fingerprints, URLs, UTC times, operator names
   and results in `docs/release-checklist.md`.

Raw `verify-release --public-key` verification remains available for qualification
and emergency diagnosis. Production verification should use the approval-bound
command above so its machine-readable result binds the exact candidate, checksum,
authorization, trust identity, status, policy and signature-file digest.

## Signing ceremony

The release approver and signer must be distinct roles where staffing permits.
The signer confirms the version, full commit, independently supplied candidate
record digest, exact `SHA256SUMS` digest, trust-record identity, completed
prerequisite gates and the independently digested canonical
[signing authorization](release-signing-authorization.md) before unlocking the
seed. Run the full
production `sign-release` command documented in
[release packaging](release-packaging.md) once against a private, quiescent
directory. It exclusively creates `SHA256SUMS.sig`; never delete or overwrite a
partial signature merely to retry. Move an uncertain candidate aside and repeat
from newly packaged approved inputs.

The verifier must use a separately retrieved active trust record, exact expected
record digest, key ID, key fingerprint and an independently trusted source
checkout. Verification is point-in-time; protect verified bytes from later
mutation through publication.

## Rotation and revocation

Routine rotation creates a new key ID and independently published active record.
Do not rewrite the old record: change its status to `revoked` only through a new,
reviewed canonical record at the documented status channel, preserving the prior
record and incident timeline. Never sign new releases with an old key after the
new key becomes authoritative.

On suspected compromise, stop signing and publication, preserve evidence, mark
affected candidates withdrawn, publish revocation through the predeclared channel,
rotate to a new independently provisioned identity, and require fresh two-operator
verification. Determine affected historical releases explicitly; this schema has
no automatic network revocation lookup and makes no assertion about historical
validity. Consumers must consult the recorded status channel before trusting a
download.

## Remaining operator gates

- Choose and provision the real secret-storage/signing environment and recovery
  mechanism.
- Name custodians, approver, independent verifier and incident owner.
- Approve and publish the release policy and rotation/revocation locations.
- Generate the production release-only identity outside this repository.
- Publish the exact active trust record through an authenticated independent
  channel and complete a two-operator rehearsal.
- Decide whether platform-native signing/notarization is also required; Ed25519
  artifact signing does not replace Apple notarization or another platform trust
  system.
