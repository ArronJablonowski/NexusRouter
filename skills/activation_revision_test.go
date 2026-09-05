package skills

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func activationRevisionFixture(t *testing.T) (*FileStore, string, Key, string, string, ActivationState) {
	t.Helper()
	ctx := context.Background()
	path := testPath(t)
	s := openTest(t, path)
	d := sample()
	a, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, d.Key, a.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	d.Steps = []string{"Run revision-aware checks"}
	b, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.ActivationState(ctx, d.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.Key != d.Key || state.Active != a.ID || len(state.Revision) != 64 || strings.ContainsAny(state.Revision, "ABCDEF") {
		t.Fatal(state)
	}
	return s, path, d.Key, a.ID, b.ID, state
}

func activationCatalogBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func assertActivationCatalogUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	if !bytes.Equal(before, activationCatalogBytes(t, path)) {
		t.Fatal("rejected activation control mutated catalog")
	}
}

func TestActivationRevisionRejectsABAWithSameActiveVersion(t *testing.T) {
	s, path, key, a, b, stale := activationRevisionFixture(t)
	ctx := context.Background()
	if err := s.Activate(ctx, key, b, a, pass, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(ctx, key, a, b, pass, false); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ActivationState(ctx, key)
	if err != nil || fresh.Active != stale.Active || fresh.Revision == stale.Revision {
		t.Fatal(fresh, stale, err)
	}
	before := activationCatalogBytes(t, path)
	if err = s.RollbackAt(ctx, stale, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale rollback crossed ABA", err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	if err = s.ActivateAt(ctx, stale, b, pass, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale activation crossed ABA", err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	expectActiveVersion(t, s, key, a)
}

func TestActivationRevisionRejectsNilContext(t *testing.T) {
	s, path, key, _, b, state := activationRevisionFixture(t)
	before := activationCatalogBytes(t, path)
	if err := s.ActivateAt(nil, state, b, pass, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.RollbackAt(nil, state, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if got, err := s.ActivationState(nil, key); !errors.Is(err, ErrInvalid) || got != (ActivationState{}) {
		t.Fatal(got, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestActivationRevisionRejectsABADuringValidation(t *testing.T) {
	s, path, key, a, b, state := activationRevisionFixture(t)
	other := openTest(t, path)
	ctx := context.Background()
	var afterABA []byte
	validator := ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		if err := other.Activate(ctx, key, b, a, pass, false); err != nil {
			t.Fatal(err)
		}
		if err := other.Activate(ctx, key, a, b, pass, false); err != nil {
			t.Fatal(err)
		}
		afterABA = activationCatalogBytes(t, path)
		return pass.Validate(ctx, v)
	})
	if err := s.ActivateAt(ctx, state, b, validator, false); !errors.Is(err, ErrConflict) {
		t.Fatal("validation ABA committed", err)
	}
	if afterABA == nil {
		t.Fatal("validator did not execute")
	}
	assertActivationCatalogUnchanged(t, path, afterABA)
	expectActiveVersion(t, s, key, a)
}

func TestActivationRevisionFreshControlsRestartAndDraftStability(t *testing.T) {
	s, path, key, a, b, state := activationRevisionFixture(t)
	ctx := context.Background()
	draft := sample()
	draft.Steps = []string{"Unactivated draft-only change"}
	if _, err := s.Draft(ctx, draft, false); err != nil {
		t.Fatal(err)
	}
	withDraft, err := s.ActivationState(ctx, key)
	if err != nil || !reflect.DeepEqual(state, withDraft) {
		t.Fatal("draft changed activation revision", state, withDraft, err)
	}
	if err = s.ActivateAt(ctx, state, b, pass, false); err != nil {
		t.Fatal(err)
	}
	activeB, err := s.ActivationState(ctx, key)
	if err != nil || activeB.Active != b || activeB.Revision == state.Revision {
		t.Fatal(activeB, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	restarted, err := s.ActivationState(ctx, key)
	if err != nil || !reflect.DeepEqual(activeB, restarted) {
		t.Fatal("restart changed state token", restarted, activeB, err)
	}
	if err = s.RollbackAt(ctx, restarted, false); err != nil {
		t.Fatal(err)
	}
	activeA, err := s.ActivationState(ctx, key)
	if err != nil || activeA.Active != a || activeA.Revision == state.Revision || activeA.Revision == activeB.Revision {
		t.Fatal(activeA, err)
	}
	before := activationCatalogBytes(t, path)
	if err = s.RollbackAt(ctx, activeA, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("exhausted rollback succeeded", err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	unchanged, err := s.ActivationState(ctx, key)
	if err != nil || !reflect.DeepEqual(activeA, unchanged) {
		t.Fatal(unchanged, err)
	}
}

func TestActivationRevisionInvalidBindingsDoNotMutate(t *testing.T) {
	for _, kind := range []string{"scope", "key", "revision", "revision_case", "version", "active", "valid_wrong_revision"} {
		t.Run(kind, func(t *testing.T) {
			s, path, _, _, b, state := activationRevisionFixture(t)
			want := ErrInvalid
			switch kind {
			case "scope":
				state.Key.Scope = "other"
			case "key":
				state.Key.Name = "../bad"
			case "revision":
				state.Revision = "bad"
			case "revision_case":
				state.Revision = strings.Repeat("A", 64)
			case "version":
				state.Version = 2
			case "active":
				state.Active = "invalid-version"
			case "valid_wrong_revision":
				state.Revision = strings.Repeat("f", 64)
				want = ErrConflict
			}
			before := activationCatalogBytes(t, path)
			if err := s.ActivateAt(context.Background(), state, b, pass, false); !errors.Is(err, want) {
				t.Fatal(kind, err, want)
			}
			assertActivationCatalogUnchanged(t, path, before)
			if err := s.RollbackAt(context.Background(), state, false); !errors.Is(err, want) {
				t.Fatal(kind, err, want)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestActivationRevisionKillSwitchAndReadOnly(t *testing.T) {
	s, path, key, _, b, state := activationRevisionFixture(t)
	ctx := context.Background()
	before := activationCatalogBytes(t, path)
	if err := s.ActivateAt(ctx, state, b, pass, true); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := s.RollbackAt(ctx, state, true); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	s.SetAutomatic(true)
	validator := ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		s.SetAutomatic(false)
		return pass.Validate(ctx, v)
	})
	if err := s.ActivateAt(ctx, state, b, validator, true); !errors.Is(err, ErrDisabled) {
		t.Fatal("kill switch ignored after validation", err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	ro, err := OpenReadOnly(path, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	inspected, err := ro.ActivationState(ctx, key)
	if err != nil || !reflect.DeepEqual(state, inspected) {
		t.Fatal(inspected, err)
	}
	if err = ro.ActivateAt(ctx, state, b, pass, false); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err = ro.RollbackAt(ctx, state, false); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}
