# Architecture and implementation boundaries

The PRD is the product authority. This document records the first sprint's implementation choices, without declaring later capabilities implemented.

## Foundation decisions

- Pin the module to the locally installed Go 1.27.1 toolchain.
- Use only the standard library for the initial command and gates.
- Keep terminal behavior testable through injected writers and explicit exit codes.
- Keep runtime version `dev` until packaging supplies a release version; the PRD's 1.0.0 is a target specification version, not a released binary.
- Count physical lines in every handwritten `.go` file, including tests and untracked files. Reject more than 1,000. Recognize standard generated-file headers through `go/ast`; exclude vendor and build output.
- CI uses the exact same `make check` entry point as local development. No hosted CI execution is claimed before a remote exists.

## Package growth

Add packages when their sprint supplies real behavior, rather than filling the tree with empty placeholders. Application services will live under `internal/`; deliberately public SDK contracts will be added only with their conformance tests.

The intended dependency direction is CLI/API/daemon → application services → runtime/routing/session interfaces → provider/storage adapters. Core code must not import CLI or HTTP presentation. Providers must not mutate authoritative workspaces. Durable effects and acceptance records must precede acknowledgements as required by the PRD.

Configuration and canonical events are independent foundations after DAR-5. SQLite migrations precede event persistence. The model/tool loop requires contracts, permissions, effect classifications, and durable events. In-process worker leases are scheduling controls, not an operating-system security sandbox.

## Verification record

DAR-5 acceptance covers module layout, a pinned Go version, formatting checks, vet, race tests, size gates, and CI configuration. Quality tests cover the exact 1,000-line boundary, unterminated last lines, generated exemptions, invalid syntax, formatting failures, and vendor exclusion. CLI tests cover commands, exit codes, output failures, and non-echoing diagnostics.
