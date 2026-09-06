package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestActiveStatesPagesEntireScopeWithoutDraftLoading(t *testing.T) {
	s, path, key, _, _, _ := activationRevisionFixture(t)
	// Clone valid catalog metadata to qualify >100 entries without repeatedly
	// writing version files. Enumeration must not need any of those bodies.
	if err := s.with(context.Background(), func(c *catalog) error {
		template := c.Skills[key.index()]
		c.Skills = map[string]entry{}
		for i := 104; i >= 0; i-- {
			e := template
			e.Key = Key{Scope: "project", Name: fmt.Sprintf("skill-%03d", i)}
			e.Versions = append([]Metadata(nil), template.Versions...)
			for j := range e.Versions {
				e.Versions[j].Key = e.Key
			}
			c.Skills[e.Key.index()] = e
		}
		inactive := template
		inactive.Key = Key{Scope: "project", Name: "skill-000-inactive"}
		inactive.Active = ""
		inactive.Activations = nil
		inactive.Versions = append([]Metadata(nil), template.Versions...)
		for j := range inactive.Versions {
			inactive.Versions[j].Key = inactive.Key
		}
		c.Skills[inactive.Key.index()] = inactive
		foreign := template
		foreign.Key = Key{Scope: "other", Name: "skill-001"}
		foreign.Versions = append([]Metadata(nil), template.Versions...)
		for j := range foreign.Versions {
			foreign.Versions[j].Key = foreign.Key
		}
		c.Skills[foreign.Key.index()] = foreign
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	r, err := OpenReadOnly(path, []string{"project", "other"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	first, err := r.ActiveStates(context.Background(), "project", "", 100)
	if err != nil || len(first) != 100 {
		t.Fatal(len(first), err)
	}
	for i, state := range first {
		if state.Key.Name != fmt.Sprintf("skill-%03d", i) || state.Key.Scope != "project" || state.Validate() != nil || state.Active == "" {
			t.Fatal(state)
		}
	}
	last, err := r.ActiveStates(context.Background(), "project", first[99].Key.Name, 100)
	if err != nil || len(last) != 5 || last[4].Key.Name != "skill-104" {
		t.Fatal(last, err)
	}
	empty, err := r.ActiveStates(context.Background(), "project", last[4].Key.Name, 100)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	other, err := r.ActiveStates(context.Background(), "other", "", 100)
	if err != nil || len(other) != 1 || other[0].Key.Scope != "other" {
		t.Fatal(other, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestActiveStatesReturnsCurrentRevisionAfterActivation(t *testing.T) {
	s, _, key, _, candidate, before := activationRevisionFixture(t)
	ctx := context.Background()
	first, err := s.ActiveStates(ctx, key.Scope, "", 10)
	if err != nil || len(first) != 1 || first[0] != before {
		t.Fatal(first, err)
	}
	if err := s.ActivateAt(ctx, before, candidate, pass, false); err != nil {
		t.Fatal(err)
	}
	now, err := s.ActiveStates(ctx, key.Scope, "", 10)
	if err != nil || len(now) != 1 || now[0].Active != candidate || now[0].Revision == before.Revision {
		t.Fatal(now, err)
	}
	exact, err := s.ActivationState(ctx, key)
	if err != nil || now[0] != exact {
		t.Fatal("enumeration not exact state", err)
	}
	if first[0] != before {
		t.Fatal("earlier snapshot aliased")
	}
}

func TestActiveStatesInvalidInputsAndMissingStore(t *testing.T) {
	s, path, _, _, _, _ := activationRevisionFixture(t)
	before := activationCatalogBytes(t, path)
	for _, args := range []struct {
		scope, after string
		limit        int
	}{{"other", "", 1}, {"", "", 1}, {"project", "bad\n", 1}, {"project", strings.Repeat("a", 65), 1}, {"project", "", 0}, {"project", "", 101}} {
		out, err := s.ActiveStates(context.Background(), args.scope, args.after, args.limit)
		if err == nil || out != nil {
			t.Fatal(args, out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		out, err := s.ActiveStates(c, "project", "", 1)
		if err == nil || out != nil {
			t.Fatal(out, err)
		}
	}
	var absent *FileStore
	if out, err := absent.ActiveStates(context.Background(), "project", "", 1); err == nil || out != nil {
		t.Fatal(out, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	missing := filepath.Join(filepath.Dir(path), "missing-active-store")
	if _, err := OpenReadOnly(missing, []string{"project"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created missing store", err)
	}
}

func TestActiveStatesOnlyInactiveIsAllocatedEmpty(t *testing.T) {
	s := openTest(t, testPath(t))
	d := sample()
	if _, err := s.Draft(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveStates(context.Background(), d.Key.Scope, "", 1)
	if err != nil || !reflect.DeepEqual(got, []ActivationState{}) {
		t.Fatal(got, err)
	}
}
