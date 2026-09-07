# Distribution dependency license inventory

This is a historical mechanical inventory for release review, not legal advice
or approval to distribute DarwinRouter. It records the non-standard-library Go
modules in the `cmd/darwin` dependency closure at commit
`074dd303cb31d6ae875000716937dfc3c3a63cb7`. The release operator must review
the complete upstream files, decide which texts/notices must accompany each
distribution channel, and record approval in the [release
checklist](release-checklist.md).

The four target closures were enumerated with `CGO_ENABLED=0 go list -deps` for
darwin/amd64, darwin/arm64, linux/amd64 and linux/arm64. Both architectures for
an operating system produced the same module set. The Darwin closure has 13
modules; Linux has 12 because `github.com/ncruces/go-strftime` is Darwin-only.
Test-only and build-tool-only modules are intentionally excluded. A final
release review must repeat the inventory against the exact candidate commit and
toolchain because dependency or build-tag changes can alter the closure. This
table is a readable baseline; it is not the final candidate authority.

For a candidate, freeze the canonical schema-2 evidence outside the checkout:

```sh
go run ./cmd/license-evidence freeze \
  --commit FULL_LOWERCASE_40_CHARACTER_COMMIT \
  --source /ABSOLUTE/PATH/TO/CLEAN/DarwinRouter \
  --out /ABSOLUTE/OPERATOR_CONTROLLED/LICENSE_EVIDENCE.json
```

The command prints a `sha256:` digest. Obtain and approve that digest through
the independent release-evidence channel, then verify the exact record from a
clean checkout:

```sh
DARWIN_LICENSE_EVIDENCE_RECORD=/ABSOLUTE/OPERATOR_CONTROLLED/LICENSE_EVIDENCE.json \
DARWIN_LICENSE_EVIDENCE_SHA256=sha256:EXPECTED_64_LOWERCASE_HEX \
  make qualify-license-evidence
```

The record binds the source commit, exact Go runtime version and module
directive, root MIT license, four ordered target closures, every discovered
legal-file size/hash, explicit Go toolchain `LICENSE` and `PATENTS`, and each
rendered `THIRD_PARTY_NOTICES.txt` digest. Evidence and notice bytes derive from
the same captured closure. Freeze and verification fail closed on a dirty or
different checkout, noncanonical/tampered evidence, dependency drift, a local
module replacement, altered legal files, or an incompatible toolchain.

“Observed family” is a convenience classification from the checked module-cache
text. It is not an SPDX attestation and must not replace review of the complete
source file identified by the module version and digest.

| Module | Version | Targets | Observed family | Upstream file SHA-256 |
| --- | --- | --- | --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` | all | MIT-like | `LICENSE` — `a973b4498c13eb74baa2a8e5c351426a6826f2fcdd909916dbe53ee2e755fd71` |
| `github.com/google/uuid` | `v1.6.0` | all | BSD-3-Clause-like | `LICENSE` — `0a8d61ed3cbfd5312326e8126c31ce9c627a283adc99131b56896d29ada04b2d` |
| `github.com/mattn/go-isatty` | `v0.0.24` | all | MIT | `LICENSE` — `08eab1118c80885fa1fa6a6dd7303f65a379fcb3733e063d20d1bbc2c76e6fa1` |
| `github.com/ncruces/go-strftime` | `v1.0.0` | Darwin only | MIT | `LICENSE` — `38ae43959daf953a393a585b2988672cb65a5a541aca0d0be5e72595a0a16883` |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | all | BSD-3-Clause-like | `LICENSE` — `dd26a7abddd02e2d0aba97805b31f248ef7835d9e10da289b22e3b8ab78b324d` |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.3` | all | Apache-2.0 | `LICENSE` — `c8858a5a76440bbca484e134cf7df46385d090dd18b2c58e650f939258802e5b` |
| `go.yaml.in/yaml/v3` | `v3.0.4` | all | dual MIT/Apache-2.0 text | `LICENSE` — `d18f6323b71b0b768bb5e9616e36da390fbd39369a81807cca352de4e4e6aa0b`; `NOTICE` — `f6c2dd3a67b576eafb89b80200b8b1627230bf3821a0c14cb99a22ac19107d00` |
| `golang.org/x/sys` | `v0.47.0` | all | BSD-3-Clause-like plus patent grant | `LICENSE` — `911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`; `PATENTS` — `96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc` |
| `golang.org/x/text` | `v0.14.0` | all | BSD-3-Clause-like plus patent grant | `LICENSE` — `2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067`; `PATENTS` — `96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc` |
| `modernc.org/libc` | `v1.74.4` | all | BSD-3-Clause-like plus third-party texts | `LICENSE` — `95ff867eb55a56935fa7492406cfa953fb7c13ca73f4c0a86ae05756b4605600`; `LICENSE-3RD-PARTY.md` — `f597097efe3d97021f89170746bd3a0fb9a8b6fb26b82043ed68a4e0283bee6c` |
| `modernc.org/mathutil` | `v1.7.1` | all | BSD-3-Clause-like | `LICENSE` — `bfa9bf72a72ca009fd62a8f84fca3dca67e51d93af96352723646599898b6cf5` |
| `modernc.org/memory` | `v1.11.0` | all | BSD-3-Clause-like plus bundled Go/logo/mmap texts | `LICENSE` — `59895e669f48f168b6b858358f6005779cdf40a265f7828813061b56af67b496`; `LICENSE-GO` — `2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067`; `LICENSE-LOGO` — `5ae5bee3072a841376451b48d8cfcec7188e10543926d5870828d36c8a750dc5`; `LICENSE-MMAP-GO` — `c2eba69f20d05414538c3a5df7694dde392e065ff70882e1625e90f5d6659fff` |
| `modernc.org/sqlite` | `v1.57.0` | all | BSD-3-Clause-like plus bundled SQLite/vector texts | `LICENSE` — `c6fe05491a60ae13bcd223088d2705e36dede24e5587226231d2459ada5c4822`; `LICENSE-SQLITE` — `8438c9c89b849131ead81d5435cb97fcf052df5b0b286dda8a2d4c29e6cb3fd0`; `LICENSE-SQLITE_VEC` — `6ce72bbe12d975bd5286e5ab0a064c069693300c47bccbc57bec18485f1621ea` |

## Required operator follow-up

1. Confirm the repository's adopted MIT text is included in the candidate.
2. Freeze and independently verify the candidate-bound schema-2 license record.
3. Verify each upstream digest from a clean module download using the declared
   Go checksum database/proxy policy.
4. Review complete upstream license, notice and patent texts and determine the
   required attribution bundle for binary and source distribution.
5. Approve the exact license-evidence digest and bind it into the external
   signing authorization.
6. Include the reviewed bundle through every approved publication channel and
   record its digest, review scope, exceptions, reviewer and UTC decision time
   in the release checklist.

Until those steps are approved, this inventory is evidence for review only and
the license/notices release gate remains open.
