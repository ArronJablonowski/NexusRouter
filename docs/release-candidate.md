# Release candidate contract

Status: local release-control tooling. This process does not approve, sign,
tag, publish, or install a release.

Before qualification or production signing, freeze a proposed version and the
full reviewed commit into an external canonical record:

```sh
candidate_parent=/ABSOLUTE/OPERATOR_CONTROLLED/DIRECTORY
candidate_commit=FULL_LOWERCASE_40_CHARACTER_COMMIT

go run ./cmd/release-candidate freeze \
  --version 1.0.0 \
  --commit "$candidate_commit" \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter \
  --out "$candidate_parent/DarwinRouter_1.0.0_candidate.json"
```

The destination must not already exist, must be outside the source checkout,
and must have an existing nonsymlink parent directory controlled by the
operator. The command requires a clean checkout
whose `HEAD` is exactly the supplied commit, materializes that commit through
the release snapshot rules, and records:

- the semantic version and full source commit;
- the schema-2 release manifest contract;
- the exact ordered Darwin/Linux, amd64/arm64 target set;
- the exact six-member archive order, modes, size ceilings and cross-target
  sharing rules; and
- SHA-256 hashes of installation instructions, project license, release notes
  and conservative example configuration from the immutable snapshot.

All four target decisions and every operator-controlled gate are recorded as
`unapproved`. This is intentional: generating a contract cannot authorize a
platform claim, accept dependency-license obligations, provision or authorize
a signing identity, approve a version, or authorize publication. Record those
decisions with evidence in the release operator checklist.

Re-verify the record against an independently obtained clean checkout before
using it as candidate evidence:

```sh
go run ./cmd/release-candidate verify \
  --record "$candidate_parent/DarwinRouter_1.0.0_candidate.json" \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter
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
