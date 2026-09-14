#!/bin/sh

# Compile and run the license-evidence command without consulting the invoking
# user's Go caches, home-directory configuration, private-module policy, or
# network proxy settings. The command itself creates a second fresh workspace
# for dependency reconstruction; this bootstrap isolates compilation first.
set -eu

umask 077

# Suppress every utility diagnostic. Only finish() may write to the caller's
# stderr, and successful canonical output is written explicitly to descriptor
# 3 after the private workspace has been removed.
exec 3>&1 4>&2
exec >/dev/null 2>&1
bootstrap_root=
bootstrap_succeeded=0

remove_workspace() {
  test -n "$bootstrap_root" || return 0
  test -d "$bootstrap_root" && test ! -L "$bootstrap_root" || return 1

  # Module downloads may leave read-only directories. Walk physical directory
  # entries only; unlike chmod -R, find's default traversal does not follow
  # symlinks encountered inside the workspace.
  find "$bootstrap_root" -depth -type d -exec chmod 0700 {} + >/dev/null 2>&1 || true
  rm -rf -- "$bootstrap_root" >/dev/null 2>&1 || return 1
  test ! -e "$bootstrap_root" && test ! -L "$bootstrap_root" || return 1
  bootstrap_root=
}

finish() {
  command_status=$?
  trap - EXIT
  cleanup_status=0
  remove_workspace || cleanup_status=1
  if test "$command_status" -ne 0 || test "$bootstrap_succeeded" -ne 1 || test "$cleanup_status" -ne 0; then
    printf '%s\n' 'license-evidence bootstrap failed' >&4
    exit 1
  fi
  exit 0
}
trap finish EXIT
trap 'exit 1' HUP INT TERM

case ${PATH-} in
  '') exit 1 ;;
esac

go_binary=$(command -v go) || exit 1
case $go_binary in
  /*) ;;
  *) exit 1 ;;
esac
test -f "$go_binary" && test -x "$go_binary" || exit 1

bootstrap_parent=${DARWIN_LICENSE_BOOTSTRAP_PARENT:-/tmp}
case $bootstrap_parent in
  /*) ;;
  *) exit 1 ;;
esac
bootstrap_parent=$(CDPATH= cd -- "$bootstrap_parent" 2>/dev/null && pwd -P) || exit 1
test -d "$bootstrap_parent" || exit 1

bootstrap_root=$(mktemp -d "$bootstrap_parent/darwin-license-evidence.XXXXXXXX") || exit 1
case $bootstrap_root in
  "$bootstrap_parent"/darwin-license-evidence.*) ;;
  *) exit 1 ;;
esac
test -d "$bootstrap_root" && test ! -L "$bootstrap_root" || exit 1
chmod 0700 "$bootstrap_root" || exit 1

bootstrap_home=$bootstrap_root/home
bootstrap_tmp=$bootstrap_root/tmp
bootstrap_module_cache=$bootstrap_root/modcache
bootstrap_build_cache=$bootstrap_root/buildcache
bootstrap_gopath=$bootstrap_root/gopath
mkdir -m 0700 "$bootstrap_home" "$bootstrap_tmp" "$bootstrap_module_cache" "$bootstrap_build_cache" "$bootstrap_gopath"

for empty_dir in "$bootstrap_home" "$bootstrap_tmp" "$bootstrap_module_cache" "$bootstrap_build_cache" "$bootstrap_gopath"; do
  first_entry=$(find "$empty_dir" ! -path "$empty_dir" -print -quit) || exit 1
  test -z "$first_entry" || exit 1
done

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P) || exit 1
case $bootstrap_root in
  "$repository_root"|"$repository_root"/*) exit 1 ;;
esac

bootstrap_binary=$bootstrap_root/license-evidence
build_stdout=$bootstrap_root/build-stdout
build_stderr=$bootstrap_root/build-stderr
command_stdout=$bootstrap_root/command-stdout
command_stderr=$bootstrap_root/command-stderr

cd "$repository_root"
if ! env -i \
  PATH="$PATH" \
  LANG=C \
  LC_ALL=C \
  TZ=UTC \
  HOME="$bootstrap_home" \
  TMPDIR="$bootstrap_tmp" \
  GOPATH="$bootstrap_gopath" \
  GOMODCACHE="$bootstrap_module_cache" \
  GOCACHE="$bootstrap_build_cache" \
  GOENV=off \
  GOFLAGS= \
  GOWORK=off \
  GOTOOLCHAIN=local \
  CGO_ENABLED=0 \
  GOPROXY=https://proxy.golang.org \
  GOSUMDB=sum.golang.org \
  GOPRIVATE= \
  GONOPROXY= \
  GONOSUMDB= \
  GOINSECURE= \
  GOAUTH=off \
  GOVCS='*:off' \
  GOTELEMETRY=off \
  "$go_binary" build -o "$bootstrap_binary" ./cmd/license-evidence \
  >"$build_stdout" 2>"$build_stderr"; then
  printf '%s\n' 'license-evidence bootstrap failed' >&2
  exit 1
fi

# Go may write ordinary module-download progress while populating a fresh cache.
# Suppress successful build diagnostics, but require an otherwise silent build
# and validate the exact private binary before it crosses the execution gate.
test ! -s "$build_stdout" || exit 1
case $bootstrap_binary in
  "$bootstrap_root"/license-evidence) ;;
  *) exit 1 ;;
esac
test -f "$bootstrap_binary" && test ! -L "$bootstrap_binary" && test -s "$bootstrap_binary" || exit 1
chmod 0500 "$bootstrap_binary" || exit 1
test -x "$bootstrap_binary" || exit 1

if ! env -i \
  PATH="$PATH" \
  LANG=C \
  LC_ALL=C \
  TZ=UTC \
  HOME="$bootstrap_home" \
  TMPDIR="$bootstrap_tmp" \
  GOPATH="$bootstrap_gopath" \
  GOMODCACHE="$bootstrap_module_cache" \
  GOCACHE="$bootstrap_build_cache" \
  GOENV=off \
  GOFLAGS= \
  GOWORK=off \
  GOTOOLCHAIN=local \
  CGO_ENABLED=0 \
  GOPROXY=https://proxy.golang.org \
  GOSUMDB=sum.golang.org \
  GOPRIVATE= \
  GONOPROXY= \
  GONOSUMDB= \
  GOINSECURE= \
  GOAUTH=off \
  GOVCS='*:off' \
  GOTELEMETRY=off \
  "$bootstrap_binary" "$@" \
  >"$command_stdout" 2>"$command_stderr"; then
  printf '%s\n' 'license-evidence bootstrap failed' >&2
  exit 1
fi

# Unlike compiler/download diagnostics, application stderr is part of the
# verifier contract. Any byte fails closed and remains private until cleanup.
if test -s "$command_stderr"; then
  printf '%s\n' 'license-evidence bootstrap failed' >&2
  exit 1
fi
case ${1-} in
  freeze)
    output_size=$(wc -c <"$command_stdout") || exit 1
    if test "$output_size" -ne 72; then
      printf '%s\n' 'license-evidence bootstrap failed' >&2
      exit 1
    fi
    grep -Eq '^sha256:[0-9a-f]{64}$' "$command_stdout" || exit 1
    IFS= read -r successful_digest <"$command_stdout" || exit 1
    ;;
  verify)
    if test -s "$command_stdout"; then
      printf '%s\n' 'license-evidence bootstrap failed' >&2
      exit 1
    fi
    ;;
  *)
    printf '%s\n' 'license-evidence bootstrap failed' >&2
    exit 1
    ;;
esac
remove_workspace || exit 1
case ${1-} in
  freeze) printf '%s\n' "$successful_digest" >&3 || exit 1 ;;
esac
bootstrap_succeeded=1
