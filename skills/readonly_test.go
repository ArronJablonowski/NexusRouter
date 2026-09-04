package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyNeverCreatesAndRejectsMutation(t *testing.T) {
	p := testPath(t)
	if s, err := OpenReadOnly(p, []string{"project"}); err == nil {
		s.Close()
		t.Fatal("opened missing store")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenReadOnly(p, []string{"project"}); err == nil {
		s.Close()
		t.Fatal("opened missing catalog")
	}
	if _, err := os.Stat(filepath.Join(p, "lock")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	w := openTest(t, p)
	ctx := context.Background()
	v, err := w.Draft(ctx, sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Activate(ctx, v.Draft.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p, "lock")); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(p, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := os.Stat(filepath.Join(p, "lock")); !os.IsNotExist(err) {
		t.Fatal("created lock", err)
	}
	if got, err := r.Load(ctx, v.Draft.Key, ""); err != nil || got.ID != v.ID {
		t.Fatal(got, err)
	}
	if h, err := r.History(ctx, v.Draft.Key); err != nil || h.Active != v.ID || len(h.Versions) != 1 {
		t.Fatal(h, err)
	}
	if _, err := r.History(ctx, Key{Scope: "other", Name: "test"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := r.Draft(ctx, sample(), false); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := r.Rollback(ctx, v.Draft.Key, v.ID, false); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := r.Activate(ctx, v.Draft.Key, v.ID, v.ID, pass, false); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestReadOnlyRefusesSymlinksAndCorruptVersions(t *testing.T) {
	p := testPath(t)
	w := openTest(t, p)
	v, err := w.Draft(context.Background(), sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(p), "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenReadOnly(link, []string{"project"}); err == nil {
		r.Close()
		t.Fatal("accepted symlink")
	}
	r, err := OpenReadOnly(p, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.WriteFile(filepath.Join(p, "version-"+v.ID+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(context.Background(), v.Draft.Key, v.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := r.History(context.Background(), v.Draft.Key); err != nil {
		t.Fatal("history read body", err)
	}
}
