package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOutcomeRollbackCandidateReturnsExactSnapshotWithoutMutation(t *testing.T) {
	s, path, key, predecessor, currentVersion, current := regressionFixture(t)
	before := activationFiles(t, path)
	candidate, err := s.OutcomeRollbackCandidate(context.Background(), key)
	if err != nil || candidate.Validate() != nil || candidate.Version != 1 || candidate.Current != current || candidate.Current.Active != currentVersion || candidate.Predecessor != predecessor {
		t.Fatal(candidate, err)
	}
	if !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal("candidate read changed store")
	}

	readOnly, err := OpenReadOnly(path, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	restarted, err := readOnly.OutcomeRollbackCandidate(context.Background(), key)
	if err != nil || restarted != candidate || !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal(restarted, err)
	}
}

func TestOutcomeRollbackCandidateRejectsIneligibleHistoryWithoutMutation(t *testing.T) {
	for _, mode := range []string{"first-activation", "reactivated", "rolled-back-current"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, _, next, state := activationRevisionFixture(t)
			ctx := context.Background()
			switch mode {
			case "reactivated":
				if err := s.ActivateAt(ctx, state, next, pass, false); err != nil {
					t.Fatal(err)
				}
				state, _ = s.ActivationState(ctx, key)
				if err := s.RollbackAt(ctx, state, false); err != nil {
					t.Fatal(err)
				}
				state, _ = s.ActivationState(ctx, key)
				if err := s.ActivateAt(ctx, state, next, pass, false); err != nil {
					t.Fatal(err)
				}
			case "rolled-back-current":
				if err := s.ActivateAt(ctx, state, next, pass, false); err != nil {
					t.Fatal(err)
				}
				state, _ = s.ActivationState(ctx, key)
				if err := s.RollbackAt(ctx, state, false); err != nil {
					t.Fatal(err)
				}
			}
			before := activationFiles(t, path)
			candidate, err := s.OutcomeRollbackCandidate(ctx, key)
			if !errors.Is(err, ErrConflict) || candidate != (OutcomeRollbackCandidate{}) {
				t.Fatal(candidate, err)
			}
			if !reflect.DeepEqual(before, activationFiles(t, path)) {
				t.Fatal("rejected candidate read changed store")
			}
		})
	}
}

func TestOutcomeRollbackCandidateRejectsInvalidHistoryWithoutRepair(t *testing.T) {
	s, path, key, _, _, _ := regressionFixture(t)
	var c catalog
	if err := s.read("catalog.json", &c); err != nil {
		t.Fatal(err)
	}
	e := c.Skills[key.index()]
	e.Activations[len(e.Activations)-1].From = "invented"
	c.Skills[key.index()] = e
	if err := s.write("catalog.json", c, false); err != nil {
		t.Fatal(err)
	}
	before := activationFiles(t, path)
	candidate, err := s.OutcomeRollbackCandidate(context.Background(), key)
	if !errors.Is(err, ErrInvalid) || candidate != (OutcomeRollbackCandidate{}) {
		t.Fatal(candidate, err)
	}
	if !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal("invalid history was repaired")
	}
}

func TestOutcomeRollbackCandidateAdmission(t *testing.T) {
	s, path, key, _, _, _ := regressionFixture(t)
	before := activationFiles(t, path)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name  string
		store *FileStore
		ctx   context.Context
		key   Key
		want  error
	}{
		{"nil-store", nil, context.Background(), key, ErrInvalid},
		{"nil-context", s, nil, key, ErrInvalid},
		{"invalid-key", s, context.Background(), Key{}, ErrInvalid},
		{"unpermitted", s, context.Background(), Key{Scope: "other", Name: key.Name}, ErrInvalid},
		{"missing", s, context.Background(), Key{Scope: key.Scope, Name: "missing"}, ErrNotFound},
		{"canceled", s, canceled, key, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, err := tt.store.OutcomeRollbackCandidate(tt.ctx, tt.key)
			if !errors.Is(err, tt.want) || candidate != (OutcomeRollbackCandidate{}) {
				t.Fatal(candidate, err)
			}
		})
	}
	if !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal("rejected candidate call changed store")
	}
}

func TestOutcomeRollbackCandidateValidate(t *testing.T) {
	current := ActivationState{Version: 1, Key: Key{Scope: "project", Name: "default"}, Active: strings.Repeat("a", 32), Revision: strings.Repeat("b", 64)}
	valid := OutcomeRollbackCandidate{Version: 1, Current: current, Predecessor: strings.Repeat("c", 32)}
	if valid.Validate() != nil {
		t.Fatal("valid candidate rejected")
	}
	for _, candidate := range []OutcomeRollbackCandidate{
		{},
		{Version: 2, Current: current, Predecessor: valid.Predecessor},
		{Version: 1, Current: ActivationState{}, Predecessor: valid.Predecessor},
		{Version: 1, Current: current, Predecessor: "invalid"},
		{Version: 1, Current: current, Predecessor: current.Active},
	} {
		if candidate.Validate() == nil {
			t.Fatal("invalid candidate accepted", candidate)
		}
	}
}
