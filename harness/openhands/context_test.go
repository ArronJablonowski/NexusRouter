package openhands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// SDK 1.50.1 checks this before its first model request. Reject at the host
// boundary too, before process launch or reservation callback side effects.
func TestNativeMinimumContextBeforeAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-executed-python")
	if err := os.WriteFile(path, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	c := runnerFixture(t, path)
	called := false
	c.Admit = func(context.Context) (func(), error) { called = true; return func() {}, nil }
	c.ContextTokens = 8192
	if c.validate() == nil {
		t.Fatal("unsupported SDK context accepted")
	}
	if _, err := Run(context.Background(), c, "fixture"); err != ErrProjection || called {
		t.Fatal(err, called)
	}
	c.ContextTokens = 16384
	if err := c.validate(); err != nil {
		t.Fatal("supported minimum rejected", err)
	}
}
