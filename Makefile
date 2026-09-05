.PHONY: build check test fmt qualify-linux-cgroup qualify-release

build:
	go build -trimpath -o bin/darwin ./cmd/darwin

check:
	go run ./cmd/check
	go vet ./...
	go test -race ./...
	go build ./...

test:
	go test -race ./...

fmt:
	gofmt -w cmd internal runtime providers tools routing policy evaluation sessions workers resources memory skills health metrics submissions approvals contextengine sdk examples scripts/qualify-cgroup

qualify-linux-cgroup:
	sh scripts/qualify-linux-cgroup.sh

# Requires a clean committed checkout. Uses only disposable test signing keys.
qualify-release:
	DARWIN_RELEASE_QUALIFY=1 go test -count=1 -timeout=45m -run '^TestReleaseQualification$$' -v ./internal/releasepack
