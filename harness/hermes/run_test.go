package hermes

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeRunner(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("native qualification opt-in")
	}
	for _, good := range []bool{true, false} {
		name := "success"
		if !good {
			name = "truncated"
		}
		t.Run(name, func(t *testing.T) { nativeGatewayFixture(t, good, true) })
	}
}

func TestRunnerAdmissionArtifactAndIdentity(t *testing.T) {
	c := Config{Executable: "/bin/sh", ExecutableSHA256: strings.Repeat("a", 64), SourceDir: t.TempDir(), RuntimeSHA256: strings.Repeat("d", 64), Provider: "fixture", Model: "fixture", ModelRevision: "r1", BaseURL: "http://127.0.0.1:1/v1", TransportPolicySHA256: strings.Repeat("b", 64), ContextTokens: 32768, MaxOutputTokens: 128, Timeout: time.Second, Prices: &Prices{}, Transport: http.DefaultTransport, Admit: func(context.Context) (func(), error) { return func() {}, nil }}
	first, err := c.Identity()
	if err != nil {
		t.Fatal(err)
	}
	c.APIKey = "rotate"
	same, err := c.Identity()
	if err != nil || same != first {
		t.Fatal("credential enters identity")
	}
	c.TransportPolicySHA256 = strings.Repeat("c", 64)
	other, _ := c.Identity()
	if other == first {
		t.Fatal("policy absent from identity")
	}
	denied := errors.New("admission denied")
	c.Admit = func(context.Context) (func(), error) { return nil, denied }
	if _, err := Run(context.Background(), c, "answer"); !errors.Is(err, denied) {
		t.Fatal("admission bypass", err)
	}
	released := false
	c.Admit = func(context.Context) (func(), error) { return func() { released = true }, nil }
	if result, err := Run(context.Background(), c, "answer"); err == nil || result != (Result{}) || !released {
		t.Fatal("wrong artifact accepted or reservation leaked")
	}
}

func TestSourceAndRuntimeBinding(t *testing.T) {
	c := Config{Executable: "/bin/sh", ExecutableSHA256: strings.Repeat("a", 64), RuntimeSHA256: strings.Repeat("b", 64), SourceDir: t.TempDir(), Provider: "fixture", Model: "fixture", ModelRevision: "r1", BaseURL: "http://127.0.0.1:1/v1", TransportPolicySHA256: strings.Repeat("c", 64), ContextTokens: 32768, MaxOutputTokens: 128, Timeout: time.Second, Prices: &Prices{}, Transport: http.DefaultTransport, Admit: func(context.Context) (func(), error) { return func() {}, nil }}
	before, err := c.Identity()
	if err != nil {
		t.Fatal(err)
	}
	c.RuntimeSHA256 = strings.Repeat("d", 64)
	after, err := c.Identity()
	if err != nil || before == after {
		t.Fatal("runtime manifest absent from evidence identity")
	}
	b, err := os.ReadFile(c.Executable)
	if err != nil {
		t.Fatal(err)
	}
	c.ExecutableSHA256 = fmt.Sprintf("%x", sha256.Sum256(b))
	if result, err := Run(context.Background(), c, "answer"); err == nil || result != (Result{}) {
		t.Fatal("unverified source accepted")
	}
}
