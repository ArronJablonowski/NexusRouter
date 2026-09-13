# Install a DarwinRouter release

These instructions install one already verified DarwinRouter release archive.
Verify it first with an independently trusted DarwinRouter verifier and release
public key. A checksum or key obtained only beside the archive does not establish
authenticity.

Choose the archive matching the host. `Darwin` and `Linux` map to `darwin` and
`linux`. `x86_64` or `amd64` maps to `amd64`; `arm64` or `aarch64` maps to
`arm64`. Stop on any other value rather than guessing.

After verification, replace every uppercase placeholder below. The destination
version directory must not already exist.

```sh
set -eu
release_dir=/ABSOLUTE/VERIFIED/RELEASE_DIRECTORY
release_version=RELEASE_VERSION
install_root=/ABSOLUTE/USER_CONTROLLED/DARWINROUTER
install_prefix="$install_root/releases/$release_version"
stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/darwinrouter-install.XXXXXX")
trap 'rm -rf -- "$stage_dir"' EXIT HUP INT TERM

case "$(uname -s)" in
  Darwin) release_os=darwin ;;
  Linux) release_os=linux ;;
  *) echo "unsupported operating system" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) release_arch=amd64 ;;
  arm64|aarch64) release_arch=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

archive="$release_dir/DarwinRouter_${release_version}_${release_os}_${release_arch}.tar.gz"
test "$(tar -tzf "$archive")" = "INSTALL.md
LICENSE
RELEASE_NOTES.md
SBOM.spdx.json
THIRD_PARTY_NOTICES.txt
config.example.yaml
darwin"
tar -xzf "$archive" -C "$stage_dir"
test -f "$stage_dir/darwin" && test ! -L "$stage_dir/darwin"
test "$("$stage_dir/darwin" version)" = "darwin $release_version"
mkdir -p "$install_root/releases"
mkdir "$install_prefix"
mkdir "$install_prefix/bin"
install -m 0755 "$stage_dir/darwin" "$install_prefix/bin/darwin"
for file in INSTALL.md LICENSE RELEASE_NOTES.md SBOM.spdx.json THIRD_PARTY_NOTICES.txt; do
  install -m 0644 "$stage_dir/$file" "$install_prefix/$file"
done
install -m 0600 "$stage_dir/config.example.yaml" "$install_prefix/config.example.yaml"
test "$("$install_prefix/bin/darwin" version)" = "darwin $release_version"
```

The example configuration is intentionally not execution-ready. Copy it to a
private configuration directory and replace its model identifier, zero context
limit, zero RAM estimate, cost estimate, and relative database path with reviewed
deployment-specific values. Use a stable absolute database path whose parent
already exists. Validate the result before starting the daemon:

```sh
"$install_prefix/bin/darwin" config validate --config /ABSOLUTE/PRIVATE/config.yaml
```

Keep the previous versioned installation and its matching pre-upgrade database
backup for rollback. Stop old writers before starting a binary that may migrate
the database. Do not use a database upgraded by a newer binary with an older
binary unless the release notes explicitly qualify that downgrade.

The release binary contains the versioned Web UI assets and Workboard schema;
no separate frontend installation is required. Before enabling browser access
or Workboard scheduling, follow the authentication, origin/CSP, approval,
backup, restore, and rollback procedures in the
[Web UI and Workboard operator guide](workboard-operator-guide.md).

`SBOM.spdx.json` is the target-specific canonical SPDX 2.3 inventory covered by
the authenticated archive and manifest checksums. It includes the binary hash,
module-level Go dependency/toolchain inventory, and hashes for the first-party
Web UI sources embedded in the binary. It is not a vulnerability report, build
provenance, legal conclusion, or proof that every dependency license was
reviewed. `NOASSERTION` marks dependency license expressions that still require
operator review; retain and review `THIRD_PARTY_NOTICES.txt` separately.
