package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func regressionHistoryFixture(t *testing.T) (*FileStore, string, Key) {
	t.Helper()
	s, path, key, a, b, _ := activationRevisionFixture(t)
	ctx := context.Background()
	if err := s.Activate(ctx, key, b, a, pass, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollback(ctx, key, b, false); err != nil {
		t.Fatal(err)
	}
	return s, path, key
}

func changeRegressionHistory(t *testing.T, path string, key Key, index int, regression any) {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(activationCatalogBytes(t, path), &manifest); err != nil {
		t.Fatal(err)
	}
	entries := manifest["skills"].(map[string]any)
	record := entries[key.index()].(map[string]any)
	transitions := record["activations"].([]any)
	transitions[index].(map[string]any)["regression"] = regression
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "catalog.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRegressionHistoryAllowsLegacyAndDeterministicFailure(t *testing.T) {
	for _, withEvidence := range []bool{false, true} {
		s, path, key := regressionHistoryFixture(t)
		before, err := s.ActivationState(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if withEvidence {
			changeRegressionHistory(t, path, key, 2, map[string]any{"id": "regression-fixture", "passed": false, "deterministic": true})
		}
		after, err := s.ActivationState(context.Background(), key)
		if err != nil || after.Active != before.Active {
			t.Fatal("valid rollback rejected", after, err)
		}
		if withEvidence && after.Revision == before.Revision {
			t.Fatal("regression evidence absent from revision binding")
		}
		if !withEvidence && after != before {
			t.Fatal("legacy history revision changed")
		}
		ro, err := OpenReadOnly(path, []string{key.Scope})
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := ro.ActivationState(context.Background(), key)
		ro.Close()
		if err != nil || reopened != after {
			t.Fatal("restart lost regression state", reopened, err)
		}
	}
}

func TestRegressionHistoryRejectsInvalidEvidenceWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
		proof any
	}{
		{"nonrollback", 1, map[string]any{"id": "regression", "passed": false, "deterministic": true}},
		{"initialactivation", 0, map[string]any{"id": "regression", "passed": false, "deterministic": true}},
		{"passing", 2, map[string]any{"id": "regression", "passed": true, "deterministic": true}},
		{"nondeterministic", 2, map[string]any{"id": "regression", "passed": false, "deterministic": false}},
		{"missingid", 2, map[string]any{"passed": false, "deterministic": true}},
		{"invalidid", 2, map[string]any{"id": "private\nunsafe", "passed": false, "deterministic": true}},
		{"longid", 2, map[string]any{"id": strings.Repeat("a", 65), "passed": false, "deterministic": true}},
		{"emptyproof", 2, map[string]any{}},
		{"invalidtype", 2, "private malformed evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, key := regressionHistoryFixture(t)
			state, err := s.ActivationState(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			changeRegressionHistory(t, path, key, tc.index, tc.proof)
			before := activationCatalogBytes(t, path)
			if _, err := s.ActivationState(context.Background(), key); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt regression admitted", err)
			}
			if err := s.RollbackAt(context.Background(), state, false); !errors.Is(err, ErrInvalid) {
				t.Fatal("mutation proceeded through corrupt regression", err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}
