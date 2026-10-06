package app

import (
	"context"
	"testing"
)

func TestBrowserDependenciesMetadata(t *testing.T) {
	s := &Service{}
	p, err := s.BrowserDependencies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) < 8 || p.Items[0].Status != "Bundled" {
		t.Fatal("missing runtime inventory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.BrowserDependencies(ctx); err == nil {
		t.Fatal("ignored cancellation")
	}
}
