package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type purposeTestFactory func(context.Context, Connection) (Provider, error)

func (f purposeTestFactory) Build(ctx context.Context, connection Connection) (Provider, error) {
	return f(ctx, connection)
}

type purposeTestProvider struct{}

func (purposeTestProvider) Models(context.Context) ([]string, error) { return []string{"fixture"}, nil }
func (purposeTestProvider) Stream(_ context.Context, _ Request, emit func(Chunk) error) error {
	return emit(Chunk{Text: "ok", Done: true, FinishReason: "stop"})
}

func purposeTestConnection(purpose Purpose) Connection {
	return Connection{Version: 1, ID: "fixture", Endpoint: "http://127.0.0.1:1", Kind: "ollama", Purpose: purpose, Transport: http.DefaultTransport}
}

func TestFactoryPurposeDefaultsToExecutionAndAdmitsKnownValues(t *testing.T) {
	for _, purpose := range []Purpose{"", PurposeExecution, PurposeDiscovery, PurposeHealth, PurposeAuxiliary} {
		t.Run(string(purpose), func(t *testing.T) {
			calls := 0
			seen := Purpose("")
			factory := purposeTestFactory(func(_ context.Context, connection Connection) (Provider, error) {
				calls++
				seen = connection.Purpose
				return purposeTestProvider{}, nil
			})
			provider, err := Build(context.Background(), factory, purposeTestConnection(purpose))
			if err != nil || provider == nil || calls != 1 {
				t.Fatalf("known purpose rejected: purpose=%q calls=%d err=%v", purpose, calls, err)
			}
			want := purpose
			if want == "" {
				want = PurposeExecution
			}
			if seen != want {
				t.Fatalf("factory saw purpose %q, want %q", seen, want)
			}
		})
	}
}

func TestFactoryPurposeRejectsUnknownValueBeforeConstruction(t *testing.T) {
	calls := 0
	factory := purposeTestFactory(func(context.Context, Connection) (Provider, error) {
		calls++
		return purposeTestProvider{}, nil
	})
	provider, err := Build(context.Background(), factory, purposeTestConnection(Purpose("private-unknown-purpose")))
	var failure *Failure
	if provider != nil || !errors.As(err, &failure) || failure == nil || failure.Code != "adapter_failure" || calls != 0 {
		t.Fatalf("unknown purpose reached factory: provider=%T calls=%d err=%v", provider, calls, err)
	}
}
