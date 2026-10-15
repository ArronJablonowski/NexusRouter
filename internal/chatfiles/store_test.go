package chatfiles

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

func TestChatFilesImmutableScopedAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "files")
	s, e := Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	f := webui.ChatFile{ID: ID("chat", "source"), TaskID: "task", Name: "report.md", MediaType: "text/plain", Size: 5}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.Put(ctx, "chat", f, []byte("hello")); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if s.Put(ctx, "chat", f, []byte("other")) == nil {
		t.Fatal("snapshot overwritten")
	}
	if _, _, e = s.Get(ctx, "other-chat", f.ID); e == nil {
		t.Fatal("cross chat read")
	}
	s.Close()
	s, e = Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	items, e := s.List(ctx, "chat")
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	_, b, e := s.Get(ctx, "chat", f.ID)
	if e != nil || string(b) != "hello" {
		t.Fatal(e)
	}
	for _, name := range []string{"../secret", "bad\nheader", "bad\\path"} {
		bad := f
		bad.Name = name
		if s.Put(ctx, "chat", bad, []byte("hello")) == nil {
			t.Fatal("unsafe name")
		}
	}
}
func TestChatFilesPrivateStoreAndQuota(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.Mkdir(target, 0700)
	alias := filepath.Join(root, "alias")
	os.Symlink(target, alias)
	if s, e := Open(ctx, alias); e == nil {
		s.Close()
		t.Fatal("symlink store")
	}
	s, e := Open(ctx, filepath.Join(root, "private"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := 0; i < 101; i++ {
		f := webui.ChatFile{ID: ID("chat", string(rune(i))), TaskID: "task", Name: "file.txt", MediaType: "text/plain", Size: 1}
		e = s.Put(ctx, "chat", f, []byte("x"))
		if (i == 100) != (e != nil) {
			t.Fatal(i, e)
		}
	}
	files, e := s.List(ctx, "chat")
	if e != nil || len(files) != 100 {
		t.Fatal("quota rollback", len(files), e)
	}
}

func TestInputBindingCannotBeReassigned(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "files")
	s, e := Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.BindInputs(ctx, "submission", "operation-a"); e != nil {
		t.Fatal(e)
	}
	if e = s.BindInputs(ctx, "submission", "operation-a"); e != nil {
		t.Fatal("idempotent binding", e)
	}
	if e = s.BindInputs(ctx, "submission", "operation-b"); e == nil {
		t.Fatal("submission reassigned")
	}
	operation, e := s.InputOperation(ctx, "submission")
	if e != nil || operation != "operation-a" {
		t.Fatal("binding changed", operation, e)
	}
}
