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
3. Creates a new private download directory outside both the source checkout
   and original signed directory.
4. Downloads each asset once with bounded sizes and strict GitHub-only HTTPS
   redirect handling.
5. Requires the deterministic content type (`application/gzip` for archives,
   `application/octet-stream` otherwise), GitHub's server digest, the freshly
   calculated local digest, size, and the publication authorization to agree.
6. Reruns approval-bound signature, checksum, candidate, license-evidence, and
   trust verification over the freshly downloaded bytes.

A successful check can be encoded as a canonical schema-versioned
`PostPublicationReceipt`. The receipt binds the observed immutable release,
asset IDs and URLs, server and local hashes, observation timestamps, the required
independent verifier identity, the fixed versioned verifier policy, and the
complete approval-verification identity. The verifier identity is an operator
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
