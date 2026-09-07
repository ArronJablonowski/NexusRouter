# Post-publication verification

DarwinRouter's post-publication verifier is an independent, read-only check of
one externally authorized GitHub release. It does not create or modify a tag,
release, asset, or approval.

The verifier first reruns the offline publication preflight against the
operator-supplied authorization record and the original signed set. It then:

1. Reads the release, annotated-tag reference, and annotated tag object through
   GitHub's GET APIs. The tag object must point directly to the exact authorized
   commit; lightweight tags, nested tags, and mismatched targets are rejected.
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
asset IDs and URLs, server and local hashes, observation timestamps, and the
complete approval-verification identity. It is point-in-time evidence, not a
claim that GitHub will remain available or unchanged forever and not an
authorization to publish or roll back anything.

The production API endpoint is fixed to `https://api.github.com`. Tests use an
injected transport and loopback HTTP servers; they never use GitHub credentials
or the public network.
