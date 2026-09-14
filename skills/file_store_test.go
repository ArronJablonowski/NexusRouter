package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testPath(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Join(p, "skills")
}
func openTest(t *testing.T, p string) *FileStore {
	t.Helper()
	s, e := Open(p, []string{"project"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func sample() Draft {
	return Draft{Key: Key{"project", "test"}, Description: "Run relevant checks", Tags: []string{"go"}, SourceSessions: []string{"session-1"}, Steps: []string{"Run focused tests"}, ValidationCases: []string{"Fixture repository"}}
}

var pass = ValidatorFunc(func(context.Context, Version) (Evidence, error) {
	return Evidence{ID: "fixture-check", Passed: true, Deterministic: true}, nil
})

func TestDraftActivationRestartRollback(t *testing.T) {
	ctx := context.Background()
	p := testPath(t)
	s := openTest(t, p)
	d := sample()
	if _, err := s.Draft(ctx, d, true); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	s.SetAutomatic(true)
	v1, err := s.Draft(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Discover(ctx, "project", nil, 10)
	if err != nil || len(m) != 0 {
		t.Fatalf("draft discoverable: %v %v", m, err)
	}
	if err = s.Activate(ctx, d.Key, v1.ID, "", pass, true); err != nil {
		t.Fatal(err)
	}
	d.Steps = []string{"Run race tests"}
	v2, err := s.Draft(ctx, d, true)
	if err != nil || v2.Parent != v1.ID {
		t.Fatalf("revision: %v %v", v2, err)
	}
	if err = s.Activate(ctx, d.Key, v2.ID, v1.ID, pass, true); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, p)
	active, err := reopened.Load(ctx, d.Key, "")
	if err != nil || active.ID != v2.ID {
		t.Fatal(active, err)
	}
	if err = reopened.Rollback(ctx, d.Key, v2.ID, false); err != nil {
		t.Fatal(err)
	}
	active, err = s.Load(ctx, d.Key, "")
	if err != nil || active.ID != v1.ID || active.Draft.Steps[0] != "Run focused tests" {
		t.Fatal(active, err)
	}
	old, err := s.Load(ctx, d.Key, v2.ID)
	if err != nil || old.Draft.Steps[0] != "Run race tests" {
		t.Fatal(old, err)
	}
	if err = s.Rollback(ctx, d.Key, v1.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	m, err = s.Discover(ctx, "project", []string{"go"}, 10)
	if err != nil || len(m) != 1 || m[0].Version != v1.ID {
		t.Fatal(m, err)
	}
	m, err = s.Discover(ctx, "project", []string{"python"}, 10)
	if err != nil || len(m) != 0 {
		t.Fatal(m, err)
	}
}

func TestValidationAndKillSwitch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, testPath(t))
	v, err := s.Draft(ctx, sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []Evidence{{ID: "fail", Deterministic: true}, {ID: "self-judge", Passed: true}, {Passed: true, Deterministic: true}} {
		validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) { return proof, nil })
		if err = s.Activate(ctx, v.Draft.Key, v.ID, "", validator, false); !errors.Is(err, ErrValidation) {
			t.Fatal(err)
		}
	}
	s.SetAutomatic(true)
	validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) { s.SetAutomatic(false); return pass.Validate(ctx, v) })
	if err = s.Activate(ctx, v.Draft.Key, v.ID, "", validator, true); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, v.Draft.Key, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, v.Draft.Key, "", "", pass, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, v.Draft.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(ctx, v.Draft.Key, v.ID, true); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestPathAndScopeConfinement(t *testing.T) {
	ctx := context.Background()
	p := testPath(t)
	s := openTest(t, p)
	for _, k := range []Key{{"../project", "x"}, {"project", "../x"}, {"other", "x"}, {"project", "/absolute"}} {
		d := sample()
		d.Key = k
		if _, err := s.Draft(ctx, d, false); !errors.Is(err, ErrInvalid) {
			t.Fatal(k, err)
		}
	}
	v, err := s.Draft(ctx, sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, v.Draft.Key, "../../secret"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = os.Symlink(p, filepath.Join(filepath.Dir(p), "link")); err != nil {
		t.Fatal(err)
	}
	if bad, err := Open(filepath.Join(filepath.Dir(p), "link"), []string{"project"}); err == nil {
		bad.Close()
		t.Fatal("accepted symlink root")
	}
	name := filepath.Join(p, "version-"+v.ID+".json")
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(p, "catalog.json"), name); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, v.Draft.Key, v.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, name := range []string{p, filepath.Join(p, "catalog.json"), filepath.Join(p, "lock")} {
		st, err := os.Stat(name)
		if err != nil || st.Mode().Perm()&0077 != 0 {
			t.Fatal(name, st, err)
		}
	}
}

func TestConcurrentActivationCAS(t *testing.T) {
	ctx := context.Background()
	p := testPath(t)
	a := openTest(t, p)
	b := openTest(t, p)
	v1, e := a.Draft(ctx, sample(), false)
	if e != nil {
		t.Fatal(e)
	}
	v2, e := b.Draft(ctx, sample(), false)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*FileStore{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := []Version{v1, v2}[i]
			results <- s.Activate(ctx, v.Draft.Key, v.ID, "", pass, false)
		}()
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for e := range results {
		if e == nil {
			won++
		} else if errors.Is(e, ErrConflict) {
			lost++
		} else {
			t.Fatal(e)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal(won, lost)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = a.Discover(ctx, "project", nil, 10); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestDiscoveryOwnsPrivacyMetadata(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, testPath(t))
	d := sample()
	d.Privacy = PrivacyPublic
	v, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, d.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	metadata, err := s.Discover(ctx, d.Key.Scope, d.Tags, 1)
	if err != nil || len(metadata) != 1 || metadata[0].Privacy != PrivacyPublic {
		t.Fatal(metadata, err)
	}
	metadata[0].Privacy = PrivacyLocalOnly
	metadata[0].Tags[0] = "mutated"
	again, err := s.Discover(ctx, d.Key.Scope, d.Tags, 1)
	if err != nil || len(again) != 1 || again[0].Privacy != PrivacyPublic || again[0].Tags[0] != d.Tags[0] {
		t.Fatal("caller mutated owned metadata", again, err)
	}
}

func TestCorruptionAndDirectoryPermissions(t *testing.T) {
	for _, p := range []string{"", "/"} {
		if s, e := Open(p, []string{"project"}); e == nil {
			s.Close()
			t.Fatal("accepted broad path")
		}
	}
	p := testPath(t)
	if e := os.Mkdir(p, 0755); e != nil {
		t.Fatal(e)
	}
	// Mkdir applies the process umask. Force the deliberately unsafe mode so
	// this fixture remains nonprivate when the test suite runs under umask 077.
	if e := os.Chmod(p, 0755); e != nil {
		t.Fatal(e)
	}
	if s, e := Open(p, []string{"project"}); e == nil {
		s.Close()
		t.Fatal("accepted nonprivate directory")
	}
	st, e := os.Stat(p)
	if e != nil || st.Mode().Perm() != 0755 {
		t.Fatal("changed existing permissions", e)
	}
	ctx := context.Background()
	s := openTest(t, testPath(t))
	v, e := s.Draft(ctx, sample(), false)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Activate(ctx, v.Draft.Key, v.ID, "", pass, false); e != nil {
		t.Fatal(e)
	}
	v.Draft.Steps = []string{"corrupted valid JSON"}
	b, _ := json.Marshal(v)
	f, e := s.root.OpenFile("version-"+v.ID+".json", os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write(b)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(ctx, v.Draft.Key, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	// Discovery intentionally does not read bodies, preserving progressive loading.
	if m, e := s.Discover(ctx, "project", nil, 10); e != nil || len(m) != 1 {
		t.Fatal(m, e)
	}
}
