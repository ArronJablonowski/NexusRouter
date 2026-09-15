package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func observedToolsValidatorFixture(t *testing.T) (*Service, skills.PublicationStore, skills.Version, []string, *atomic.Int32) {
	t.Helper()
	svc, tasks, generationCalls := groupedAppFixture(t)
	setValidatorSkillRoot(t, svc)
	key := skills.Key{Scope: "project", Name: "validated-workflow"}
	selection, err := svc.PlanGroupedWorkflowSelection(context.Background(), "a", key, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	version := publishValidatorAttempt(t, svc, attempt)
	store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return svc, store, version, tasks, generationCalls
}

func TestObservedToolsProvenanceValidatorEndToEndReadOnlyAndConcurrent(t *testing.T) {
	svc, store, version, _, calls := observedToolsValidatorFixture(t)
	validator := ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database, LocalOnly: svc.settings.Skills.LocalOnly}
	beforeCatalog := validatorFiles(t, svc.settings.Skills.Root)
	beforeAttempt, beforeSelection := validatorTelemetryRecords(t, svc, version)

	proof, err := validator.Validate(context.Background(), version)
	if err != nil || proof != (skills.Evidence{ID: ObservedToolsProvenanceValidatorID, Passed: true, Deterministic: true}) || calls.Load() != 1 {
		t.Fatal(proof, err, calls.Load())
	}
	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			p, err := validator.Validate(context.Background(), version)
			if err != nil || p.ID != ObservedToolsProvenanceValidatorID || !p.Passed || !p.Deterministic {
				errs <- errors.New("concurrent validation failed")
			}
		}()
	}
	wait.Wait()
	close(errs)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !reflect.DeepEqual(beforeCatalog, validatorFiles(t, svc.settings.Skills.Root)) {
		t.Fatal("validator dispatched generation or changed catalog", calls.Load())
	}
	afterAttempt, afterSelection := validatorTelemetryRecords(t, svc, version)
	if !reflect.DeepEqual(beforeAttempt, afterAttempt) || !reflect.DeepEqual(beforeSelection, afterSelection) {
		t.Fatal("validator changed telemetry")
	}
}

func TestObservedToolsProvenanceValidatorRejectsBindingMutationsAndPanics(t *testing.T) {
	svc, store, version, _, _ := observedToolsValidatorFixture(t)
	valid := ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}
	mutations := map[string]func(*skills.Version){
		"id":         func(v *skills.Version) { v.ID = strings.Repeat("a", 32) },
		"key":        func(v *skills.Version) { v.Draft.Key.Name = "other" },
		"parent":     func(v *skills.Version) { v.Parent = strings.Repeat("b", 32) },
		"created":    func(v *skills.Version) { v.CreatedAt = v.CreatedAt.Add(1) },
		"draft":      func(v *skills.Version) { v.Draft.Steps[0] = "forged step" },
		"validation": func(v *skills.Version) { v.Draft.ValidationCases[0] = "do not execute this text" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := version
			body, _ := json.Marshal(version)
			if json.Unmarshal(body, &candidate) != nil {
				t.Fatal("clone")
			}
			mutate(&candidate)
			if proof, err := valid.Validate(context.Background(), candidate); !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
				t.Fatal("mutated candidate accepted", proof, err)
			}
		})
	}
	for name, validator := range map[string]ObservedToolsProvenanceValidator{
		"nil-store":   {Database: svc.settings.Telemetry.Database},
		"typed-nil":   {Publications: (*skills.FileStore)(nil), Database: svc.settings.Telemetry.Database},
		"missing-db":  {Publications: store, Database: filepath.Join(t.TempDir(), "missing.db")},
		"panic-store": {Publications: panicPublicationStore{PublicationStore: store}, Database: svc.settings.Telemetry.Database},
	} {
		t.Run(name, func(t *testing.T) {
			if proof, err := validator.Validate(context.Background(), version); !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
				t.Fatal("invalid dependency accepted", proof, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled} {
		if proof, err := valid.Validate(ctx, version); !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
			t.Fatal("canceled validation accepted", proof, err)
		}
	}
	drift := &driftingPublicationStore{PublicationStore: store}
	if proof, err := (ObservedToolsProvenanceValidator{Publications: drift, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version); !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
		t.Fatal("concurrent publication revision accepted", proof, err)
	}
}

func TestObservedToolsProvenanceValidatorRejectsStaleEvidenceAndUnobservedRequiredTool(t *testing.T) {
	t.Run("stale-evidence", func(t *testing.T) {
		svc, store, version, tasks, _ := observedToolsValidatorFixture(t)
		history, err := FeedbackHistory(context.Background(), svc.settings.Telemetry.Database, tasks[0])
		if err != nil || len(history) == 0 {
			t.Fatal(err)
		}
		if err = ReviseFeedback(context.Background(), svc.settings.Telemetry.Database, tasks[0], history[len(history)-1].ID, true); err != nil {
			t.Fatal(err)
		}
		proof, err := (ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version)
		if !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
			t.Fatal("stale selection accepted", proof, err)
		}
	})

	t.Run("changed-tool-sequence", func(t *testing.T) {
		svc, store, version, tasks, _ := observedToolsValidatorFixture(t)
		changed := rewriteCanonicalAppEventsForTest(t, svc.settings.Telemetry.Database, tasks[0], func(event runtime.Event) bool {
			return (event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted) && event.Data.ToolName == "lookup"
		}, func(event *runtime.Event) { event.Data.ToolName = "different_lookup" })
		if changed != 2 {
			t.Fatal("fixture did not rewrite paired tool events", changed)
		}
		proof, err := (ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version)
		if !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
			t.Fatal("changed durable tool sequence accepted", proof, err)
		}
	})

	for _, mode := range []string{"unobserved-required-tool", "privacy-downgrade"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks, _ := groupedAppFixture(t)
			setValidatorSkillRoot(t, svc)
			key := skills.Key{Scope: "project", Name: mode}
			selection, err := svc.PlanGroupedWorkflowSelection(context.Background(), "a", key, tasks, 0)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unobserved-required-tool" {
				attempt.Result.Draft.RequiredTools = []string{"never_observed"}
			} else {
				attempt.Result.Draft.Privacy = skills.PrivacyPublic
			}
			body, err := json.Marshal(attempt)
			if err != nil || attempt.Validate() != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = raw.ExecContext(context.Background(), `UPDATE skill_generation_attempts SET body=? WHERE id=?`, body, attempt.ID); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}
			version := publishValidatorAttempt(t, svc, attempt)
			store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{"project"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			proof, err := (ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version)
			if !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
				t.Fatal("unsafe published draft accepted", proof, err)
			}
		})
	}
}

func TestObservedToolsProvenanceValidatorLeavesCandidateValidationCasesInert(t *testing.T) {
	svc, tasks, _ := groupedAppFixture(t)
	setValidatorSkillRoot(t, svc)
	key := skills.Key{Scope: "project", Name: "inert-validation-case"}
	selection, err := svc.PlanGroupedWorkflowSelection(context.Background(), "a", key, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Result.Draft.ValidationCases = []string{"panic('candidate text must never execute')"}
	body, err := json.Marshal(attempt)
	if err != nil || attempt.Validate() != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(context.Background(), `UPDATE skill_generation_attempts SET body=? WHERE id=?`, body, attempt.ID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	version := publishValidatorAttempt(t, svc, attempt)
	store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proof, err := (ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version)
	if err != nil || proof != (skills.Evidence{ID: ObservedToolsProvenanceValidatorID, Passed: true, Deterministic: true}) {
		t.Fatal("inert candidate validation text affected provenance", proof, err)
	}
}

func TestObservedToolsProvenanceValidatorRejectsAttemptAndPolicyDrift(t *testing.T) {
	for _, mode := range []string{"attempt-digest", "selection-policy"} {
		t.Run(mode, func(t *testing.T) {
			svc, store, version, _, _ := observedToolsValidatorFixture(t)
			attempt, selection := validatorTelemetryRecords(t, svc, version)
			var body []byte
			var query string
			var id string
			if mode == "attempt-digest" {
				attempt.EstimatedCost += .01
				if attempt.Validate() != nil {
					t.Fatal("attempt drift fixture invalid")
				}
				body, _ = json.Marshal(attempt)
				query, id = `UPDATE skill_generation_attempts SET body=? WHERE id=?`, attempt.ID
			} else {
				selection.PolicyDigest = strings.Repeat("c", 64)
				body, _ = json.Marshal(selection)
				query, id = `UPDATE workflow_selections SET body=? WHERE id=?`, selection.ID
			}
			raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = raw.ExecContext(context.Background(), query, body, id); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			raw.Close()
			proof, err := (ObservedToolsProvenanceValidator{Publications: store, Database: svc.settings.Telemetry.Database}).Validate(context.Background(), version)
			if !errors.Is(err, skills.ErrValidation) || proof != (skills.Evidence{}) {
				t.Fatal("durable provenance drift accepted", proof, err)
			}
		})
	}
}

type panicPublicationStore struct{ skills.PublicationStore }

func (panicPublicationStore) Load(context.Context, skills.Key, string) (skills.Version, error) {
	panic("private publication panic")
}

type driftingPublicationStore struct {
	skills.PublicationStore
	calls atomic.Int32
}

func (s *driftingPublicationStore) PublicationBinding(ctx context.Context, key skills.Key, version string) (skills.PublicationBinding, error) {
	binding, err := s.PublicationStore.PublicationBinding(ctx, key, version)
	if err == nil && s.calls.Add(1) > 1 {
		binding.AttemptDigest = strings.Repeat("f", 64)
	}
	return binding, err
}

func validatorFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func validatorTelemetryRecords(t *testing.T, svc *Service, version skills.Version) (skills.GenerationAttempt, skills.WorkflowSelection) {
	t.Helper()
	store, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bindingStore, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{version.Draft.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindingStore.PublicationBinding(context.Background(), version.Draft.Key, version.ID)
	bindingStore.Close()
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.SkillGenerationAttempt(context.Background(), binding.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := store.WorkflowSelection(context.Background(), version.Draft.Key.Scope, binding.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return attempt, selection
}

func publishValidatorAttempt(t *testing.T, svc *Service, attempt skills.GenerationAttempt) skills.Version {
	t.Helper()
	if err := attempt.Validate(); err != nil {
		t.Fatalf("invalid publication fixture attempt: %v: %+v", err, attempt)
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{attempt.Key.Scope})
	if err != nil {
		t.Fatal("open publication store:", err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	version, err := store.PublishGeneration(context.Background(), attempt, true)
	if err != nil {
		t.Fatal("publish attempt:", err)
	}
	return version
}

func setValidatorSkillRoot(t *testing.T, svc *Service) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Skills.Root = filepath.Join(parent, "catalog")
}
