package agentchat

import (
	"context"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func message() webui.CollaborationMessage {
	return webui.CollaborationMessage{Hostname: "qa-host", Harness: "pi", Runner: "pi host-tool adapter", Provider: "ollama", SenderID: "a", SenderModel: "qwen", Recipient: "z", Topic: "bug", TaskID: "task", SessionID: "session", Text: "check the parser", Private: true}
}
func TestJournalRestartIdempotencyPrivacyAndPaging(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "chat")
	s, e := Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	m := message()
	a, e := s.Append(ctx, strings.Repeat("a", 64), m)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Append(ctx, strings.Repeat("a", 64), m)
	if e != nil || a != b {
		t.Fatal("retry changed receipt", a, b, e)
	}
	m.Text = "changed"
	if _, e = s.Append(ctx, strings.Repeat("a", 64), m); e != ErrConflict {
		t.Fatal(e)
	}
	for i := 0; i < 54; i++ {
		m = message()
		m.Text = fmt.Sprint(i)
		if _, e = s.Append(ctx, fmt.Sprintf("%064x", i), m); e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	s, e = Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	page, e := s.Read(ctx, webui.CollaborationOptions{}, "z", true)
	if e != nil || len(page.Messages) != 50 || page.NextBefore == 0 || page.Messages[0].Hostname != "qa-host" || page.Messages[0].Harness != "pi" {
		t.Fatal(page, e)
	}
	older, e := s.Read(ctx, webui.CollaborationOptions{Before: page.NextBefore}, "z", true)
	if e != nil || len(older.Messages) != 5 {
		t.Fatal(older, e)
	}
	for _, pair := range []struct {
		id    string
		local bool
	}{{"other", true}, {"z", false}} {
		p, e := s.Read(ctx, webui.CollaborationOptions{}, pair.id, pair.local)
		if e != nil || len(p.Messages) != 0 {
			t.Fatal("private or addressed message leaked", p, e)
		}
	}
	m = message()
	m.Private = false
	m.Recipient = "*"
	m.TaskID = "public-task"
	if _, e = s.Append(ctx, strings.Repeat("f", 64), m); e != nil {
		t.Fatal(e)
	}
	p, e := s.Read(ctx, webui.CollaborationOptions{}, "cloud", false)
	if e != nil || len(p.Messages) != 1 || p.Messages[0].Private {
		t.Fatal(p, e)
	}
	st, e := os.Stat(filepath.Join(dir, "messages.db"))
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("journal not private", st, e)
	}
}
func TestConcurrentTaskQuotaAndUnsafeStorage(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "chat")
	a, e := Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 70)
	for i := 0; i < 70; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 0 {
				s = b
			}
			_, e := s.Append(ctx, fmt.Sprintf("%064x", i), message())
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, limited := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if e == ErrLimit {
			limited++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 64 || limited != 6 {
		t.Fatal(ok, limited)
	}
	unsafe := filepath.Join(t.TempDir(), "linked")
	if e = os.Symlink(dir, unsafe); e != nil {
		t.Fatal(e)
	}
	if s, e := Open(ctx, unsafe); e == nil {
		s.Close()
		t.Fatal("linked store opened")
	}
}
func TestEscapedMessageCannotExceedReadPageBound(t *testing.T) {
	s, e := Open(context.Background(), filepath.Join(t.TempDir(), "chat"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	m := message()
	m.Text = strings.Repeat("<", 8192)
	if _, e = s.Append(context.Background(), strings.Repeat("a", 64), m); e != ErrInvalid {
		t.Fatal("escaped body admitted above page bound", e)
	}
}
