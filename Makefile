.PHONY: build check test fmt qualify-linux-cgroup qualify-release qualify-codex-repair

build:
	go build -trimpath -o bin/darwin ./cmd/darwin

check:
	go run ./cmd/check
	go vet ./...
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race ./...
	go build ./...

test:
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race ./...

fmt:
	gofmt -w cmd internal runtime providers tools routing policy evaluation sessions workers resources memory skills health metrics submissions approvals contextengine sdk examples scripts/qualify-cgroup

qualify-linux-cgroup:
	sh scripts/qualify-linux-cgroup.sh

# Explicit supervised signed-in cloud inference with controlled local results.
# Uses account usage; never included in check/test or ordinary CI.
qualify-codex-repair:
	DARWIN_CODEX_LIVE_FAILURE_REPAIR=1 go test -race ./internal/codexbridge -run '^TestLiveCodexRecoverableToolProtocol$$' -count=1 -v
	DARWIN_CODEX_LIVE_REPAIR=1 go test -race ./internal/app -run '^TestLiveCodexDelegationRepair$$' -count=1 -v

# Requires a clean committed checkout. Uses only disposable test signing keys.
qualify-release:
	DARWIN_RELEASE_QUALIFY=1 go test -count=1 -timeout=45m -run '^TestReleaseQualification$$' -v ./internal/releasepack
