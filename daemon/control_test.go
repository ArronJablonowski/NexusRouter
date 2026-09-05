package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestControllerBoundAndIdempotent(t *testing.T) {
	var calls atomic.Int32
	id := NewID()
	c, err := New(id, func() { calls.Add(1) })
	if err != nil || !ValidID(id) {
		t.Fatal(err)
	}
	initial, err := c.Current(context.Background())
	if err != nil || initial.Validate() != nil || initial.State != "ready" {
		t.Fatal(initial, err)
	}
	if _, err := c.Stop(context.Background(), strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Stop(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("denied stop invoked callback")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := c.Stop(context.Background(), id)
			if err != nil || s.State != "stopping" || s.InstanceID != id || !s.StartedAt.Equal(initial.StartedAt) {
				t.Error(s, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestControllerValidation(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := New(id, func() {}); err == nil {
			t.Fatal("invalid id")
		}
	}
	if _, err := New(NewID(), nil); err == nil {
		t.Fatal("nil callback")
	}
	c, _ := New(NewID(), func() {})
	if _, err := c.Current(nil); err == nil {
		t.Fatal("nil context")
	}
	if _, err := c.Stop(nil, c.status.InstanceID); err == nil {
		t.Fatal("nil context")
	}
}
