# Post-publication verification

DarwinRouter's post-publication verifier is an independent, read-only check of
one externally authorized GitHub release. It does not create or modify a tag,
release, asset, or approval.

The verifier first reruns the offline publication preflight against the
operator-supplied authorization record and the original signed set. It then:

1. Reads the release, annotated-tag reference, and annotated tag object through
   GitHub's GET APIs. The tag object must point directly to the exact authorized
   commit and reproduce the authorization-bound tag message, tagger name,
   email, and whole-second UTC date; lightweight tags, nested tags, and
   mismatched targets are rejected. The receipt retains the tag-object SHA.
2. Requires an immutable, non-draft release with the exact authorized tag,
   commit, title, notes, prerelease decision, and seven-asset set.
3. Uses an explicitly selected, digest-pinned GitHub CLI to verify GitHub's
   release attestation for the same repository, tag, annotated-tag object,
   release ID, and exact seven assets before any remote asset is written.
4. Creates a new private download directory outside both the source checkout
   and original signed directory.
5. Downloads each asset once with bounded sizes and strict GitHub-only HTTPS
   redirect handling.
6. Requires the deterministic content type (`application/gzip` for archives,
   `application/octet-stream` otherwise), GitHub's server digest, the freshly
   calculated local digest, size, and the publication authorization to agree.
7. Reruns approval-bound signature, checksum, candidate, license-evidence, and
   trust verification over the freshly downloaded bytes.

A successful check can be encoded as a canonical schema-versioned
`PostPublicationReceipt`. The receipt binds the observed immutable release,
asset IDs and URLs, server and local hashes, observation timestamps, the required
independent verifier identity, the fixed versioned verifier policy, and the
complete approval-verification identity. Schema 2 also binds the normalized
GitHub CLI version and executable digest, hashes of the verified result and
bundle, certificate signer and issuer, predicate type, verified-timestamp
count, and annotated-tag subject digest. The verifier identity is an operator
assertion and must be reconciled with separately retained host and authentication
evidence. It is point-in-time evidence, not a
claim that GitHub will remain available or unchanged forever and not an
authorization to publish or roll back anything.

The verifier must not be the publication approver recorded in the canonical
publication authorization; that conflict is rejected before GitHub is queried.
Because the current authorization schema does not carry the credentialed
publication executor's identity, retain that identity separately and require a
different post-publication verifier as an operator gate.

The production API endpoint is fixed to `https://api.github.com`. Tests use an
injected transport and loopback HTTP servers; they never use GitHub credentials
or the public network.

## GitHub release-attestation verifier

Install an operator-reviewed, currently patched GitHub CLI release whose
version is at least 2.93.0. The minimum is a compatibility floor, not a claim
that every later security fix is optional. Resolve the executable to its final
absolute path, verify that it is a regular nonsymlink executable, and obtain its
SHA-256 through an independent software-distribution or workstation-management
channel. Do not calculate the expected digest from the same unchecked path at
the moment of verification.

Pass both identities to the production verifier:

```sh
go run ./cmd/verify-published-release \
  --dir /ABSOLUTE/ORIGINAL/SIGNED-RELEASE \
  --source /ABSOLUTE/INDEPENDENT/CLEAN/DarwinRouter \
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
  --release-notes /ABSOLUTE/INDEPENDENT/RELEASE_NOTES.md \
  --verifier-id idp:INDEPENDENT_RELEASE_VERIFIER \
  --download-dir /ABSOLUTE/EXISTING/PARENT/NEW-downloads \
  --out /ABSOLUTE/EVIDENCE/NEW-post-publication.json \
  --gh-binary /ABSOLUTE/RESOLVED/PATH/TO/gh \
  --gh-binary-sha256 sha256:INDEPENDENTLY_ESTABLISHED_GH_BINARY
```

The command-backed verifier probes a stable semantic GitHub CLI version, then
runs exactly
`gh release verify TAG -R github.com/OWNER/REPOSITORY --format json`. The
explicit host prevents local `GH_HOST` or account configuration from selecting
a different GitHub instance. It
requires the GitHub release attestation initiator `github`, in-toto predicate
`https://in-toto.io/attestation/release/v0.1`, the exact repository, tag and
release ID, certificate signer `https://dotcom.releases.github.com`, OIDC issuer
`https://token.actions.githubusercontent.com`, at least one verified timestamp,
and a tag subject
URI `pkg:github/OWNER/REPOSITORY@TAG` whose SHA-1 is the already observed
annotated-tag-object digest. Its remaining subjects must be exactly the seven
authorized asset names and SHA-256 values, without duplicates or extras. The
decoded DSSE statement must exactly match the CLI's verification result.

GitHub's ordinary release metadata and asset downloads remain unauthenticated
public GET requests. The GitHub CLI may require its own authenticated GitHub
session depending on repository visibility, service behavior, and local CLI
configuration; establish that session outside DarwinRouter and never place its
credentials in command arguments, the receipt, or retained logs. DarwinRouter
does not print GitHub CLI stderr or underlying command errors and retains no raw
statement, bundle, authentication configuration, executable path, or GitHub
credential. Retain separate operator evidence for the reviewed CLI provenance,
authentication context, host, and transcript as policy requires.

This check proves that GitHub's verification response binds the observed tag
and asset identities. It does not independently validate GitHub's certificate
root policy, prove the physical host or workflow provenance, show that an
authorized human approved publication, scan the assets, or make the release
immutable forever. It is additive to the local checksum/signature and approval
verification, not a replacement for those controls.

## Published native install evidence

After retaining the post-publication receipt, run the host-matching archive from
a fresh directory containing the exact receipt-bound downloaded byte set rather
than copying or rebuilding a binary:

```sh
go run ./cmd/verify-published-install \
  --publication-receipt /ABSOLUTE/EVIDENCE/post-publication.json \
  --publication-receipt-sha256 sha256:EXPECTED_RECEIPT \
  --install-evidence /ABSOLUTE/EVIDENCE/final-native-install.json \
  --install-evidence-sha256 sha256:EXPECTED_INSTALL_EVIDENCE \
  --backup-sha256 sha256:EXPECTED_BACKUP \
  --target-os darwin --target-arch arm64 \
  --verifier-id idp:INDEPENDENT_VERIFIER \
  --download-dir /ABSOLUTE/VERIFIED/DOWNLOADS \
  --install-root /ABSOLUTE/EXISTING/PARENT/NEW-published-install \
  --out /ABSOLUTE/EVIDENCE/NEW-published-install.json
```

Run this command only on a disposable, low-privilege verification host with no
production credentials and with outbound network access denied. The minimal
child environment, private directory modes, timeout, and bounded output are
defense in depth; they are not a sandbox and do not restrict the downloaded
program's filesystem, process, or network authority beyond that of the operator.

The command accepts only the actual runtime OS and architecture. Before native
execution, it validates and durably creates an empty, mode-0600, create-only
reservation at `--out`; an unsafe, unwritable, overlapping, or existing output
fails before the binary runs. It then rechecks
the closed signed release set and the receipt-bound manifest, checksum,
signature, and whole-archive digests; extracts the authenticated binary; creates
a new mode-0700 install root and mode-0755 binary through pinned directory
handles; and runs only `darwin version` with bounded output, time, and
environment. It revalidates the path and pinned inode chain immediately before
and after execution, rereads the installed binary and release inputs, and only
then records a whole-second UTC completion time and commits the canonical
record into the reservation. The record binds the observed binary digest and
version output to both the post-publication receipt and the separately retained
full installation/migration/backup/rollback evidence for the same archive.

Directory pinning and private modes close parent-replacement and other-user
races, but they cannot isolate a hostile process already running as the same OS
user. The disposable-host requirement remains mandatory. Retain the command
transcript, host identity/provenance, record digest, and independent digest
delivery channel with the release evidence; the canonical record is an
operator/mechanical assertion, not a signed attestation.

This proves a native version smoke of the freshly downloaded bytes. It does not
repeat the full schema rehearsal, approve the earlier rehearsal record, qualify
another target, or authorize publication, installation, migration, or rollback.
Retain the printed record digest through the independent evidence channel and
verify the stored record with that exact digest:

```sh
go run ./cmd/verify-published-install-record \
  --record /ABSOLUTE/EVIDENCE/published-install.json \
  --record-sha256 sha256:INDEPENDENTLY_OBTAINED_RECORD_DIGEST
```

On any failure after output reservation, do not delete the zero-length or
partial output and do not blindly retry: the install root may also contain
partial or executed state. Quarantine both paths, retain the command transcript,
classify whether execution began, and use entirely new output/install paths only
after an operator decides a retry is safe. A failure to print the digest after a
successful commit means the record may be complete; verify it independently
before taking further action.
