package skills

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func operationRecordFixture(t *testing.T) (*FileStore, string, ActivationOperation) {
	t.Helper()
	s, path, key, _, candidate, expected := activationRevisionFixture(t)
	proof := Evidence{ID: "operation-validator", Passed: true, Deterministic: true}
	record := ActivationRecord{From: expected.Active, To: candidate, At: time.Now().UTC(), OperationID: "activation-operation", BeforeRevision: expected.Revision, Evidence: &proof}
	if err := s.with(context.Background(), func(c *catalog) error {
		e := c.Skills[key.index()]
		e.Validated[candidate] = proof
		e.Activations = append(e.Activations, record)
		e.Active = candidate
		c.Skills[key.index()] = e
		c.Schema = 3
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	return s, path, ActivationOperation{Version: 1, OperationID: record.OperationID, Expected: expected, Candidate: candidate, Record: record}
}

func TestActivationOperationRecordValidation(t *testing.T) {
	_, _, valid := operationRecordFixture(t)
	if valid.Validate() != nil {
		t.Fatal("valid receipt rejected")
	}
	for name, mutate := range map[string]func(*ActivationOperation){
		"version": func(o *ActivationOperation) { o.Version = 2 }, "operation": func(o *ActivationOperation) { o.OperationID = "wrong" }, "missingop": func(o *ActivationOperation) { o.OperationID = "" },
		"expectedkey": func(o *ActivationOperation) { o.Expected.Key.Name = "" }, "candidate": func(o *ActivationOperation) { o.Candidate = o.Expected.Active }, "from": func(o *ActivationOperation) { o.Record.From = "" },
		"before": func(o *ActivationOperation) { o.Record.BeforeRevision = strings.Repeat("a", 64) }, "uppercase": func(o *ActivationOperation) { o.Record.BeforeRevision = strings.Repeat("A", 64) },
		"rollback": func(o *ActivationOperation) { o.Record.Rollback = true }, "evidence": func(o *ActivationOperation) { o.Record.Evidence = nil }, "failed": func(o *ActivationOperation) { o.Record.Evidence = &Evidence{ID: "validator", Deterministic: true} },
		"judge": func(o *ActivationOperation) { o.Record.Evidence = &Evidence{ID: "judge", Passed: true} }, "time": func(o *ActivationOperation) { o.Record.At = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			o := valid
			mutate(&o)
			if o.Validate() == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
	for _, a := range []ActivationRecord{{BeforeRevision: strings.Repeat("a", 64)}, {Evidence: &Evidence{ID: "proof", Passed: true, Deterministic: true}}} {
		if validActivationOperationFields(a) {
			t.Fatal("legacy orphan fields accepted")
		}
	}
}

func TestActivationOperationInspectionAndLegacyHashes(t *testing.T) {
	s, path, want := operationRecordFixture(t)
	ctx := context.Background()
	before := activationCatalogBytes(t, path)
	got, err := s.ActivationOperation(ctx, want.Expected.Key, want.OperationID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	if _, err := s.ActivationOperation(ctx, want.Expected.Key, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.ActivationOperation(nil, want.Expected.Key, want.OperationID); err == nil {
		t.Fatal("nilcontext")
	}
	assertActivationCatalogUnchanged(t, path, before)
	legacy := ActivationRecord{From: "", To: want.Expected.Active, At: want.Record.At}
	body, _ := json.Marshal(legacy)
	for _, field := range []string{"operation_id", "before_revision", "evidence"} {
		if strings.Contains(string(body), field) {
			t.Fatal("legacy hash material changed", string(body))
		}
	}
	for _, schema := range []int{1, 2} {
		old, oldPath, key, _, _, state := activationRevisionFixture(t)
		if err := old.with(ctx, func(c *catalog) error { c.Schema = schema; return nil }, true); err != nil {
			t.Fatal(err)
		}
		saved := activationCatalogBytes(t, oldPath)
		read, err := old.ActivationState(ctx, key)
		if err != nil || read != state {
			t.Fatal("legacy revision changed", read, err)
		}
		assertActivationCatalogUnchanged(t, oldPath, saved)
	}
}

func TestActivationOperationCatalogRejectsBindingAndPrefixCorruption(t *testing.T) {
	for _, mode := range []string{"schema", "duplicate", "prefix", "evidence", "orphan-fields"} {
		t.Run(mode, func(t *testing.T) {
			s, path, want := operationRecordFixture(t)
			var c catalog
			if err := s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			e := c.Skills[want.Expected.Key.index()]
			switch mode {
			case "schema":
				c.Schema = 2
			case "duplicate":
				body, _ := json.Marshal(e)
				var duplicate entry
				json.Unmarshal(body, &duplicate)
				duplicate.Key.Name = "different"
				for i := range duplicate.Versions {
					duplicate.Versions[i].Key = duplicate.Key
				}
				c.Skills[duplicate.Key.index()] = duplicate
			case "prefix":
				e.Activations[0].At = e.Activations[0].At.Add(time.Nanosecond)
				c.Skills[e.Key.index()] = e
			case "evidence":
				e.Activations[1].Evidence = &Evidence{ID: "failed", Deterministic: true}
				c.Skills[e.Key.index()] = e
			case "orphan-fields":
				e.Activations[0].BeforeRevision = strings.Repeat("a", 64)
				c.Skills[e.Key.index()] = e
			}
			if err := s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			if _, err := s.ActivationOperation(context.Background(), want.Expected.Key, want.OperationID); err == nil {
				t.Fatal("corrupt receipt accepted")
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestActivationOperationPublicationDoesNotDowngrade(t *testing.T) {
	s, _, want := operationRecordFixture(t)
	if _, err := s.PublishGeneration(context.Background(), generationAttemptFixture("drafted"), false); err != nil {
		t.Fatal(err)
	}
	var c catalog
	if err := s.read("catalog.json", &c); err != nil || c.Schema != 3 {
		t.Fatal("publication downgraded", err, c.Schema)
	}
	got, err := lookupActivationOperation(&c, want.Expected.Key, want.OperationID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	other := want.Expected.Key
	other.Name = "other"
	if _, err := lookupActivationOperation(&c, other, want.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("operation rebound acrossskill", err)
	}
}
