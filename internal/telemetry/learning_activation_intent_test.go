package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func activationIntentFixture(t *testing.T) (*Store, string, skills.LearningActivationIntent) {
	t.Helper()
	s, path := generationStore(t)
	ctx := context.Background()
	i := skills.LearningActivationIntent{Version: 1, Scope: "project", Name: "default", SelectionID: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), ValidatorID: "deterministic", LearningRevision: 4, Expected: skills.ActivationState{Version: 1, Key: skills.Key{Scope: "project", Name: strings.Repeat("c", 64)}, Revision: strings.Repeat("d", 64)}, Candidate: strings.Repeat("e", 32), CreatedAt: time.Unix(300, 0).UTC()}
	state := skills.LearningState{Version: 1, Scope: i.Scope, Name: i.Name, Domain: "general", PolicyDigest: i.PolicyDigest, Revision: i.LearningRevision, Phase: "generate", ScanRevision: 1, ConsumeRevision: 1, Epoch: 1, PendingSelectionID: i.SelectionID, PendingBucketID: i.Expected.Key.Name}
	if state.Validate() != nil {
		t.Fatal("invalid fixture state")
	}
	body, _ := json.Marshal(state)
	if _, err := s.db.Exec(`INSERT INTO learning_states(scope,name,revision,body) VALUES(?,?,?,?)`, state.Scope, state.Name, state.Revision, body); err != nil {
		t.Fatal(err)
	}
	a := generationAttemptFixture(i.SelectionID)
	a.Key = i.Expected.Key
	if err := s.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSkillGeneration(ctx, draftedGeneration(a)); err != nil {
		t.Fatal(err)
	}
	return s, path, i
}

func TestLearningActivationIntentImmutableReadOnly(t *testing.T) {
	s, path, i := activationIntentFixture(t)
	ctx := context.Background()
	state, _ := s.LearningState(ctx, i.Scope, i.Name)
	if _, err := s.LearningActivationIntent(ctx, i.Scope, i.Name, i.SelectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.PutLearningActivationIntent(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LearningActivationIntent(ctx, i.Scope, i.Name, i.SelectionID)
	if err != nil || got != i {
		t.Fatal(got, err)
	}
	current, err := s.LearningState(ctx, i.Scope, i.Name)
	if err != nil || current != state {
		t.Fatal("intent changed learning state", err)
	}
	for _, field := range []string{"candidate", "validator", "time", "expected"} {
		changed := i
		switch field {
		case "candidate":
			changed.Candidate = strings.Repeat("f", 32)
		case "validator":
			changed.ValidatorID = "different"
		case "time":
			changed.CreatedAt = changed.CreatedAt.Add(time.Second)
		case "expected":
			changed.Expected.Revision = strings.Repeat("f", 64)
		}
		if err := s.PutLearningActivationIntent(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatal(field, err)
		}
	}
	s.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.LearningActivationIntent(ctx, i.Scope, i.Name, i.SelectionID)
	if err != nil || got != i {
		t.Fatal(got, err)
	}
	r.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read mutated", err)
	}
}

func TestLearningActivationIntentRejectsForeignBinding(t *testing.T) {
	for _, mode := range []string{"revision", "policy", "bucket", "selection", "phase", "generation-key", "generation-status", "schema", "cancel", "nil"} {
		t.Run(mode, func(t *testing.T) {
			s, _, i := activationIntentFixture(t)
			ctx := context.Background()
			state, _ := s.LearningState(ctx, i.Scope, i.Name)
			switch mode {
			case "revision":
				i.LearningRevision++
			case "policy":
				i.PolicyDigest = strings.Repeat("f", 64)
			case "bucket":
				i.Expected.Key.Name = strings.Repeat("f", 64)
			case "selection":
				i.SelectionID = strings.Repeat("f", 64)
			case "phase":
				state.Phase = "discover"
				state.PendingSelectionID = ""
				state.PendingBucketID = ""
			case "generation-key", "generation-status":
				a, err := s.SkillGenerationAttempt(ctx, i.SelectionID)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "generation-key" {
					a.Key.Name = "other"
					a.Result.Draft.Key = a.Key
				} else {
					a.Status = "started"
					a.Result = nil
					a.FinishedAt = time.Time{}
				}
				body, _ := json.Marshal(a)
				if _, err := s.db.Exec(`UPDATE skill_generation_attempts SET name=?,status=?,body=? WHERE id=?`, a.Key.Name, a.Status, body, a.ID); err != nil {
					t.Fatal(err)
				}
			case "schema":
				if _, err := s.db.Exec(`PRAGMA user_version=25`); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil":
				ctx = nil
			}
			if mode == "phase" {
				body, _ := json.Marshal(state)
				if _, err := s.db.Exec(`UPDATE learning_states SET body=?`, body); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.PutLearningActivationIntent(ctx, i); err == nil {
				t.Fatal("foreign binding accepted")
			}
			var count int
			if s.db.QueryRow(`SELECT count(*) FROM learning_activation_intents`).Scan(&count) != nil || count != 0 {
				t.Fatal("rejection persisted intent")
			}
		})
	}
}

func TestLearningActivationIntentCorruptionAndStaleRepeat(t *testing.T) {
	s, _, i := activationIntentFixture(t)
	ctx := context.Background()
	if err := s.PutLearningActivationIntent(ctx, i); err != nil {
		t.Fatal(err)
	}
	state, _ := s.LearningState(ctx, i.Scope, i.Name)
	state.Revision++
	state.PendingSelectionID = ""
	state.BucketAfter = state.PendingBucketID
	state.PendingBucketID = ""
	body, _ := json.Marshal(state)
	if _, err := s.db.Exec(`UPDATE learning_states SET revision=?,body=?`, state.Revision, body); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLearningActivationIntent(ctx, i); !errors.Is(err, ErrConflict) {
		t.Fatal("stale repeat rebound", err)
	}
	if got, err := s.LearningActivationIntent(ctx, i.Scope, i.Name, i.SelectionID); err != nil || got != i {
		t.Fatal("advanced cursor hid durable intent", err)
	}
	if _, err := s.db.Exec(`UPDATE learning_activation_intents SET body=CAST(body AS TEXT)||' '`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LearningActivationIntent(ctx, i.Scope, i.Name, i.SelectionID); err == nil || !reflect.DeepEqual(got, skills.LearningActivationIntent{}) {
		t.Fatal("noncanonical accepted", got, err)
	}
}

func TestLearningActivationIntentMigrationPreservesState(t *testing.T) {
	s, _, i := activationIntentFixture(t)
	ctx := context.Background()
	before, err := s.LearningState(ctx, i.Scope, i.Name)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := s.SkillGenerationAttempt(ctx, i.SelectionID)
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; PRAGMA user_version=25`); err != nil {
		t.Fatal(err)
	}
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.LearningState(ctx, i.Scope, i.Name)
	if err != nil || after != before {
		t.Fatal("migration changed learning", err)
	}
	got, err := s.SkillGenerationAttempt(ctx, i.SelectionID)
	if err != nil || !reflect.DeepEqual(got, attempt) {
		t.Fatal("migration changed draft", err)
	}
	var version, count int
	if s.db.QueryRow(`PRAGMA user_version`).Scan(&version) != nil || version != 29 || s.db.QueryRow(`SELECT count(*) FROM learning_activation_intents`).Scan(&count) != nil || count != 0 {
		t.Fatal(version, count)
	}
	if _, err := s.db.Exec(`INSERT INTO learning_activation_intents VALUES('missing','missing','selection','{}')`); err == nil {
		t.Fatal("foreign state admitted")
	}
}

func TestLearningActivationIntentSchemaAndIgnoredInsert(t *testing.T) {
	for _, version := range []int{25, 30} {
		s, _, i := activationIntentFixture(t)
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LearningActivationIntent(context.Background(), i.Scope, i.Name, i.SelectionID); !errors.Is(err, ErrLearningUnavailable) {
			t.Fatal("unsupported read schema", version, err)
		}
		if err := s.PutLearningActivationIntent(context.Background(), i); !errors.Is(err, ErrLearningUnavailable) {
			t.Fatal("unsupported write schema", version, err)
		}
	}
	s, _, i := activationIntentFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER ignore_intent BEFORE INSERT ON learning_activation_intents BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLearningActivationIntent(context.Background(), i); !errors.Is(err, ErrConflict) {
		t.Fatal("ignored insert acknowledged", err)
	}
	if _, err := s.LearningActivationIntent(context.Background(), i.Scope, i.Name, i.SelectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
