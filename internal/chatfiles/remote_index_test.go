package chatfiles

import (
	"context"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"path/filepath"
	"testing"
)

func TestRemoteIndexGroupsRootsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "files")
	s, e := Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 28; i++ {
		root := fmt.Sprintf("request-root-%04d", i)
		scope := "conversation_" + root
		body := []byte("snapshot")
		f := webui.ChatFile{ID: ID(scope, "history"), TaskID: scope, Name: "history.json", MediaType: "application/octet-stream", Size: len(body)}
		if e = s.Put(ctx, scope, f, body); e != nil {
			t.Fatal(e)
		}
		if e = s.IndexConversation(ctx, scope, RemoteIndex{Instance: "spark", RequestID: root, Root: root, Title: root, Model: "model"}); e != nil {
			t.Fatal(e)
		}
	}
	first, more, e := s.RemoteChats(ctx, 0, 25)
	if e != nil || !more || len(first) != 25 {
		t.Fatal(first, more, e)
	}
	second, more, e := s.RemoteChats(ctx, first[24].Sequence, 25)
	if e != nil || more || len(second) != 3 {
		t.Fatal(second, more, e)
	}
	root := first[5].Root
	scope := "conversation_child"
	b := []byte("child")
	if e = s.Put(ctx, scope, webui.ChatFile{ID: ID(scope, "history"), TaskID: scope, Name: "history.json", MediaType: "application/octet-stream", Size: len(b)}, b); e != nil {
		t.Fatal(e)
	}
	if e = s.IndexConversation(ctx, scope, RemoteIndex{Instance: "spark", RequestID: "request-child-001", Root: root, Title: "same root", Model: "model"}); e != nil {
		t.Fatal(e)
	}
	if e = s.CacheRemoteObservation(ctx, scope, []byte("original terminal")); e != nil {
		t.Fatal(e)
	}
	if s.CacheRemoteObservation(ctx, scope, []byte("changed terminal")) == nil {
		t.Fatal("changed terminal receipt accepted")
	}
	s.Close()
	s, e = Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	page, _, e := s.RemoteChats(ctx, 0, 100)
	if e != nil || len(page) != 28 || page[0].RequestID != "request-child-001" {
		t.Fatal(page, e)
	}
	saved, e := s.RemoteObservation(ctx, scope)
	if e != nil || string(saved) != "original terminal" {
		t.Fatal(string(saved), e)
	}
}
