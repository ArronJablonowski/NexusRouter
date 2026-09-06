package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPublishGenerationIdempotentAcrossRestartAndRace(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	stores := []*FileStore{openTest(t, path), openTest(t, path)}
	a := generationAttemptFixture("drafted")
	var versions [2]Version
	var errs [2]error
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) { defer wg.Done(); versions[i], errs[i] = stores[i].PublishGeneration(ctx, a, false) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(versions[0], versions[1]) {
		t.Fatal(versions, errs)
	}
	before := activationFiles(t, path)
	restarted := openTest(t, path)
	retry, err := restarted.PublishGeneration(ctx, a, false)
	if err != nil || !reflect.DeepEqual(retry, versions[0]) || !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal("retry changed publication", retry, err)
	}
	var c catalog
	if err := restarted.read("catalog.json", &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Skills[a.Key.index()].Versions) != 1 || len(c.Publications) != 1 {
		t.Fatal("duplicate publication")
	}
	if _, err := restarted.Load(ctx, a.Key, versions[0].ID); err != nil {
		t.Fatal("receipt has no durable body", err)
	}
	a.ID = "second-generation"
	v2, err := restarted.PublishGeneration(ctx, a, false)
	if err != nil || v2.ID == versions[0].ID {
		t.Fatal("distinct attempts collapsed", v2, err)
	}
}

func TestPublishGenerationConflictingAttemptCannotMutate(t *testing.T) {
	for _, mode := range []string{"payload", "key", "model", "provider", "digest"} {
		t.Run(mode, func(t *testing.T) {
			path := testPath(t)
			s := openTest(t, path)
			a := generationAttemptFixture("drafted")
			if _, err := s.PublishGeneration(context.Background(), a, false); err != nil {
				t.Fatal(err)
			}
			before := activationFiles(t, path)
			switch mode {
			case "payload":
				a.Result.Draft.Description = "Changed description"
			case "key":
				a.Key.Name = "other"
				a.Result.Draft.Key = a.Key
			case "model":
				a.Model = "other"
				a.Result.Model = a.Model
			case "provider":
				a.Provider = "other"
			case "digest":
				a.InputDigest = strings.Repeat("b", 64)
			}
			if _, err := s.PublishGeneration(context.Background(), a, false); !errors.Is(err, ErrConflict) {
				t.Fatal("changed attempt accepted", err)
			}
			if !reflect.DeepEqual(before, activationFiles(t, path)) {
				t.Fatal("conflict changed files")
			}
		})
	}
}

func TestPublishGenerationAdmissionAndInactiveState(t *testing.T) {
	for _, mode := range []string{"automatic-off", "readonly", "nil-context", "failed", "started", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			path := testPath(t)
			s := openTest(t, path)
			a := generationAttemptFixture("drafted")
			ctx := context.Background()
			automatic := false
			switch mode {
			case "automatic-off":
				automatic = true
			case "readonly":
				if _, err := s.Draft(ctx, sample(), false); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = OpenReadOnly(path, []string{"project"})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
			case "nil-context":
				ctx = nil
			case "failed", "started":
				a = generationAttemptFixture(mode)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := activationFiles(t, path)
			if _, err := s.PublishGeneration(ctx, a, automatic); err == nil {
				t.Fatal("inadmissible publication accepted")
			}
			if !reflect.DeepEqual(before, activationFiles(t, path)) {
				t.Fatal("rejected publication mutated files")
			}
		})
	}
	s, path, key, _, _, before := activationRevisionFixture(t)
	s.SetAutomatic(true)
	a := generationAttemptFixture("drafted")
	v, err := s.PublishGeneration(context.Background(), a, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.ActivationState(context.Background(), key)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("publication activated or revised state", after, err)
	}
	if err := s.Activate(context.Background(), key, v.ID, before.Active, pass, false); err != nil {
		t.Fatal(err)
	}
	files := activationFiles(t, path)
	retry, err := s.PublishGeneration(context.Background(), a, false)
	if err != nil || !reflect.DeepEqual(v, retry) || !reflect.DeepEqual(files, activationFiles(t, path)) {
		t.Fatal("post-activation retry changed publication", retry, err)
	}
}

func TestPublishGenerationRejectsCorruptReceiptWithoutRepair(t *testing.T) {
	for _, mode := range []string{"digest", "missing-version", "wrong-key", "missing-body", "legacy-schema", "future-schema", "duplicate-version"} {
		t.Run(mode, func(t *testing.T) {
			path := testPath(t)
			s := openTest(t, path)
			a := generationAttemptFixture("drafted")
			v, err := s.PublishGeneration(context.Background(), a, false)
			if err != nil {
				t.Fatal(err)
			}
			var c catalog
			if err := s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			receipt := c.Publications[a.ID]
			switch mode {
			case "legacy-schema":
				c.Schema = 1
			case "future-schema":
				c.Schema = 4
			case "duplicate-version":
				c.Publications["other-generation"] = receipt
			case "digest":
				receipt.AttemptDigest = "invalid"
			case "missing-version":
				receipt.Version = strings.Repeat("f", 32)
			case "wrong-key":
				receipt.Key.Name = "other"
			case "missing-body":
				if err := os.Remove(filepath.Join(path, "version-"+v.ID+".json")); err != nil {
					t.Fatal(err)
				}
			}
			c.Publications[a.ID] = receipt
			body, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "catalog.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			before := activationFiles(t, path)
			if _, err := s.PublishGeneration(context.Background(), a, false); err == nil {
				t.Fatal("corrupt publication accepted")
			}
			if !reflect.DeepEqual(before, activationFiles(t, path)) {
				t.Fatal("corrupt receipt repaired")
			}
		})
	}
}

func TestPublishGenerationUpgradesCatalogOnlyOnPublication(t *testing.T) {
	path := testPath(t)
	s := openTest(t, path)
	if _, err := s.Draft(context.Background(), sample(), false); err != nil {
		t.Fatal(err)
	}
	var before catalog
	if err := s.read("catalog.json", &before); err != nil || before.Schema != 1 {
		t.Fatal("ordinary draft changed schema", before.Schema, err)
	}
	if _, err := s.PublishGeneration(context.Background(), generationAttemptFixture("drafted"), false); err != nil {
		t.Fatal(err)
	}
	files := activationFiles(t, path)
	ro, err := OpenReadOnly(path, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var after catalog
	if err := ro.read("catalog.json", &after); err != nil || after.Schema != 2 || len(after.Publications) != 1 {
		t.Fatal("publication schema upgrade missing", after.Schema, err)
	}
	if !reflect.DeepEqual(files, activationFiles(t, path)) {
		t.Fatal("read-only reopen mutated upgrade")
	}
}
