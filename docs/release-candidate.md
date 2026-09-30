# Release candidate contract

Status: local release-control tooling. This process does not approve, sign,
tag, publish, or install a release.

Before qualification or production signing, freeze a proposed version and the
full reviewed commit into an external canonical record:

```sh
candidate_parent=/ABSOLUTE/OPERATOR_CONTROLLED/DIRECTORY
candidate_commit=FULL_LOWERCASE_40_CHARACTER_COMMIT

go run ./cmd/release-candidate freeze \
  --version 1.0.1 \
  --commit "$candidate_commit" \
  --source /ABSOLUTE/PATH/TO/CLEAN/NexusRouter \
  --out "$candidate_parent/NexusRouter_1.0.1_candidate.json"
```

The destination must not already exist, must be outside the source checkout,
and must have an existing nonsymlink parent directory controlled by the
operator. The command requires a clean checkout
whose `HEAD` is exactly the supplied commit, materializes that commit through
the release snapshot rules, and records:

- the semantic version and full source commit;
- the schema-3 release manifest contract;
- the exact ordered Darwin/Linux, amd64/arm64 target set;
- the exact seven-member archive order, modes, size ceilings and cross-target
  sharing rules; and
- SHA-256 hashes of installation instructions, project license, release notes
  and conservative example configuration from the immutable snapshot.

The canonical candidate record is schema 3. It binds both the immutable source
release-notes template and the exact derived final-notes digest. Generate the
reviewable final body without overwriting either input:

```sh
go run ./cmd/release-candidate notes \
  --record "$candidate_parent/NexusRouter_1.0.1_candidate.json" \
  --source /ABSOLUTE/PATH/TO/CLEAN/NexusRouter \
  --out "$candidate_parent/NexusRouter_1.0.1_RELEASE_NOTES.md"
```

The generated body contains the exact semantic version, full source commit,
commit-derived UTC date, fixed four-target contract, and explicit limitations.
Changing any identity or the source template changes its candidate-bound
digest. Generation is create-only, grants no approval, and requires the same
clean exact checkout as candidate verification.

The target-specific archive
contract includes `SBOM.spdx.json` as a non-shared member between release notes
and third-party notices. The SBOM is an SPDX 2.3 module-level inventory bound to
the target binary, Go dependency/toolchain closure, and exact first-party Web UI
source hashes. It is not vulnerability or build-provenance evidence and does not
make a legal determination; unreviewed dependency license expressions remain
`NOASSERTION` pending the separate operator review.
The record also freezes the release creation timestamp as the source commit's
committer time normalized to whole-second UTC. Manifest and SBOM timestamps must
match it exactly; production signing re-derives the value from the clean commit.

All four target decisions and every operator-controlled gate are recorded as
`unapproved`. This is intentional: generating a contract cannot authorize a
platform claim, accept dependency-license obligations, provision or authorize
a signing identity, approve a version, or authorize publication. Record those
decisions with evidence in the release operator checklist.

Re-verify the record against an independently obtained clean checkout before
using it as candidate evidence:

```sh
go run ./cmd/release-candidate verify \
  --record "$candidate_parent/NexusRouter_1.0.1_candidate.json" \
  --source /ABSOLUTE/PATH/TO/CLEAN/NexusRouter
```

Verification rejects noncanonical JSON, unknown or reordered contract data,
changed bounds, fabricated approvals, a dirty checkout, a different `HEAD`, or
source collateral that no longer matches the recorded commit. The record is
not signed and therefore is not provenance on its own. Retain it with the
reviewed commit, independently authenticated source, completed checklist,
candidate-bound [license evidence](dependency-license-inventory.md), CI run
links, native target evidence, signatures, and publication evidence.

The current packager always emits all four targets. An operator decision that
any target is unsupported blocks the candidate; it does not authorize silently
removing that archive. Any desired change to targets, archive members, bounds,
or manifest schema is a new reviewed source commit and requires a newly frozen
candidate record.
