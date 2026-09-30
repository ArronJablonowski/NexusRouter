#!/bin/sh
# Optional live qualification; never pulls an image or changes the Docker daemon.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
command -v docker >/dev/null
command -v go >/dev/null
target_platform=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')
case "$target_platform" in
  linux/amd64) target_arch=amd64 ;;
  linux/arm64) target_arch=arm64 ;;
  *) printf '%s\n' 'An existing linux/amd64 or linux/arm64 alpine:3.22 image is required.' >&2; exit 1 ;;
esac
qualification_image=$(docker image inspect alpine:3.22 --format '{{.Id}}')
image_platform=$(docker image inspect "$qualification_image" --format '{{.Os}}/{{.Architecture}}')
if [ "$image_platform" != "$target_platform" ]; then
  printf '%s\n' 'The existing alpine:3.22 image must match the Docker server architecture.' >&2
  exit 1
fi
printf 'Docker server platform: %s\n' "$target_platform"
docker image inspect "$qualification_image" --format 'Qualification image: {{.Id}}; repository digests: {{json .RepoDigests}}'

qualification_dir=$(mktemp -d /tmp/darwin-cgroup-qualification.XXXXXXXX)
cleanup() {
  # Only remove the container whose ID Docker wrote for this invocation.
  if [ -f "$qualification_dir/container.id" ]; then
    container_id=$(cat "$qualification_dir/container.id")
    case "$container_id" in
      ''|*[!a-f0-9]*) ;;
      *) if [ "${#container_id}" -eq 64 ]; then
           docker rm -f "$container_id" >/dev/null 2>&1 || true
         fi ;;
    esac
  fi
  # Exact owned files only: no recursive cleanup of an unresolved directory.
  rm -f "$qualification_dir/darwin" "$qualification_dir/verify" \
    "$qualification_dir/report" "$qualification_dir/container.id"
  rmdir "$qualification_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Dependencies must already be cached. Build output never enters the repository.
host_os=$(GOTOOLCHAIN=local go env GOHOSTOS)
host_arch=$(GOTOOLCHAIN=local go env GOHOSTARCH)
CGO_ENABLED=0 GOOS="$host_os" GOARCH="$host_arch" GOTOOLCHAIN=local GOPROXY=off GONOPROXY=none GOSUMDB=off go build -trimpath \
  -o "$qualification_dir/verify" ./scripts/qualify-cgroup
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" GOTOOLCHAIN=local GOPROXY=off GONOPROXY=none GOSUMDB=off \
  go build -trimpath -o "$qualification_dir/darwin" ./cmd/nexus

docker run --rm --pull=never --platform "$image_platform" \
  --cidfile "$qualification_dir/container.id" \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --memory 512m --memory-swap 512m --cpus 1.5 --pids-limit 64 --stop-timeout 1 \
  --mount "type=bind,src=$qualification_dir/darwin,dst=/darwin,readonly" \
  "$qualification_image" sh -ec '
    test -f /sys/fs/cgroup/cgroup.controllers
    printf "cgroup-v2 %s %s %s %s\n" \
      "$(cat /sys/fs/cgroup/memory.max)" \
      "$(cat /sys/fs/cgroup/memory.current)" \
      "$(cat /sys/fs/cgroup/cpu.max)" \
      "$(cat /sys/fs/cgroup/memory.swap.max)"
    timeout 15 /darwin resources
  ' > "$qualification_dir/report"
"$qualification_dir/verify" "$qualification_dir/report"
