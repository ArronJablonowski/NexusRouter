package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthCacheExpiryIdentityAndErrors(t *testing.T) {
	c := newHealthCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	calls := 0
	fetch := func(context.Context) ([]string, error) { calls++; return []string{"model"}, nil }
	names, err := c.models(context.Background(), "key", fetch)
	if err != nil {
		t.Fatal(err)
	}
	names[0] = "mutated"
	names, err = c.models(context.Background(), "key", fetch)
	if err != nil || names[0] != "model" || calls != 1 {
		t.Fatal(names, err, calls)
	}
	c.models(context.Background(), "other-key", fetch)
	if calls != 2 {
		t.Fatal("identity not isolated")
	}
	now = now.Add(5 * time.Second)
	c.models(context.Background(), "key", fetch)
	if calls != 3 {
		t.Fatal("expired cache reused")
	}
	for i := 0; i < 2; i++ {
		_, err = c.models(context.Background(), "error", func(context.Context) ([]string, error) { calls++; return nil, errors.New("failure") })
		if err == nil {
			t.Fatal("error disappeared")
		}
	}
	if calls != 5 {
		t.Fatal("negative result cached")
	}
	for i := 0; i < 2; i++ {
		if _, err = c.models(context.Background(), "panic", func(context.Context) ([]string, error) { panic("private") }); err == nil {
			t.Fatal("panic accepted")
		}
	}
}

func TestConcurrentDiscoveryCoalescesAndCancellationDoesNotPoison(t *testing.T) {
	c := newHealthCache()
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	fetch := func(context.Context) ([]string, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []string{"m"}, nil
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := c.models(context.Background(), "same", fetch); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.models(ctx, "same", fetch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate concurrent discovery", calls.Load())
	}
}

func TestInvalidationDuringFetchPreventsStaleRepopulation(t *testing.T) {
	c := newHealthCache()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.models(context.Background(), "key", func(context.Context) ([]string, error) { close(entered); <-release; return []string{"old"}, nil })
	}()
	<-entered
	c.clear()
	close(release)
	<-done
	names, err := c.models(context.Background(), "key", func(context.Context) ([]string, error) { return []string{"fresh"}, nil })
	if err != nil || names[0] != "fresh" {
		t.Fatal(names, err)
	}
}

func TestAutomaticTasksReuseProviderDiscovery(t *testing.T) {
	svc, _ := autoFixture(t)
	var discovered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			discovered.Add(1)
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	for i := 0; i < 2; i++ {
		if _, err := svc.Run(context.Background(), Request{Prompt: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	if discovered.Load() != 1 {
		t.Fatalf("discovery repeated across pool/tasks: %d", discovered.Load())
	}
}

func BenchmarkWarmDiscovery(b *testing.B) {
	c := newHealthCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	fetch := func(context.Context) ([]string, error) { return []string{"model-a", "model-b"}, nil }
	c.models(context.Background(), "key", fetch)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.models(context.Background(), "key", fetch); err != nil {
			b.Fatal(err)
		}
	}
}
