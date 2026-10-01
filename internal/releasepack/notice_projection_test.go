package releasepack

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// Compare actual Go output before and after field projection. The production
// bound stays 1 MiB; only this test's reference capture permits a larger document.
func TestNoticeProjectionPreservesCompleteClosure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	source, err := command(ctx, ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := newGoReconstruction(environment(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspace.close(); err != nil {
			t.Error(err)
		}
	}()
	env := append(append([]string{}, workspace.env...), "GOOS=darwin", "GOARCH=amd64")
	toolchain, _, err := goToolchainAttribution(ctx, source, workspace.env)
	if err != nil {
		t.Fatal(err)
	}
	full := exec.CommandContext(ctx, workspace.goExecutable.path, "list", "-mod=readonly", "-deps", "-json", "./cmd/nexus")
	full.Dir = source
	full.Env = env
	full.WaitDelay = 2 * time.Second
	var reference noticeReferenceOutput
	full.Stdout = &reference
	full.Stderr = io.Discard
	if err := full.Run(); err != nil {
		t.Fatal("full reference", err)
	}
	packages, err := decodeListedPackages(reference.String())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := targetClosureFromPackages(packages, toolchain, workspace.moduleCache.path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := targetNoticeClosure(ctx, source, "darwin", "amd64", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("projection changed package count, dependency graph, modules or license evidence")
	}
	t.Logf("full reference %d bytes; preserved %d packages and %d legal modules", reference.Len(), actual.PackageCount, len(actual.Modules))
}

type noticeReferenceOutput struct{ bytes.Buffer }

func (b *noticeReferenceOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, ErrInvalid
	}
	return b.Buffer.Write(p)
}
