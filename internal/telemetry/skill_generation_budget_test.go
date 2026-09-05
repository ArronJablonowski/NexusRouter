package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func testGenerationBudget() skills.GenerationBudget {
	return skills.GenerationBudget{Version: 1, Window: time.Hour, MaxCost: 1, MaxAttempts: 10, MaxInFlight: 1}
}

func TestGenerationBudgetAtomicTwoStoreReservation(t *testing.T) {
	s, path := generationStore(t)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			a := generationAttemptFixture(fmt.Sprintf("attempt-%d", i))
			a.StartedAt = time.Now().UTC()
			errs[i] = store.BeginSkillGenerationBudgeted(context.Background(), a, testGenerationBudget())
		}(i, store)
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("budget admitted multiple writers", errs)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM skill_generation_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestGenerationBudgetAccountsUnbudgetedAndTerminalHistory(t *testing.T) {
	for _, mode := range []string{"unbudgeted", "expired-started", "failed-cost", "failed-count", "terminal-releases"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			a := generationAttemptFixture("first")
			a.StartedAt = time.Now().UTC()
			a.EstimatedCost = .6
			if mode == "expired-started" {
				a.StartedAt = a.StartedAt.Add(-24 * time.Hour)
			}
			if err := s.BeginSkillGeneration(ctx, a); err != nil {
				t.Fatal(err)
			}
			budget := testGenerationBudget()
			budget.MaxCost = 1
			if mode == "failed-cost" || mode == "failed-count" || mode == "terminal-releases" {
				a.Status = "failed"
				a.Code = "generation_failed"
				a.FinishedAt = time.Now().UTC()
				if err := s.FinishSkillGeneration(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failed-count" {
				budget.MaxAttempts = 1
			}
			b := generationAttemptFixture("next")
			b.StartedAt = time.Now().UTC()
			b.EstimatedCost = .6
			if mode == "terminal-releases" {
				b.EstimatedCost = .2
			}
			err := s.BeginSkillGenerationBudgeted(ctx, b, budget)
			if mode == "terminal-releases" {
				if err != nil {
					t.Fatal("terminal did not release inflight", err)
				}
				if err = s.BeginSkillGenerationBudgeted(ctx, b, budget); !errors.Is(err, ErrConflict) {
					t.Fatal("same ID reauthorized", err)
				}
				return
			}
			if err == nil {
				t.Fatal("historical reservation ignored")
			}
			if _, err = s.SkillGenerationAttempt(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("budget denial inserted", err)
			}
		})
	}
}

func TestGenerationBudgetCorruptAndOversizedHistoryDenied(t *testing.T) {
	for _, mode := range []string{"corrupt", "oversize-row", "too-many", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			a := generationAttemptFixture("first")
			if err := s.BeginSkillGeneration(ctx, a); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				if _, err := s.db.Exec(`UPDATE skill_generation_attempts SET body='{}'`); err != nil {
					t.Fatal(err)
				}
			case "oversize-row":
				if _, err := s.db.Exec(`UPDATE skill_generation_attempts SET body=zeroblob(524289)`); err != nil {
					t.Fatal(err)
				}
			case "too-many", "aggregate":
				n, size := 1001, 1
				if mode == "aggregate" {
					n, size = 17, 524288
				}
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < n; i++ {
					if _, err = tx.Exec(`INSERT INTO skill_generation_attempts(id,scope,name,status,body) VALUES(?,'project','checks','started',zeroblob(?))`, fmt.Sprintf("row-%04d", i), size); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			b := generationAttemptFixture("next")
			b.StartedAt = time.Now().UTC()
			if err := s.BeginSkillGenerationBudgeted(ctx, b, testGenerationBudget()); err == nil {
				t.Fatal("corrupt scope admitted")
			}
			if _, err := s.SkillGenerationAttempt(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("corrupt scope inserted", err)
			}
		})
	}
}

func TestGenerationBudgetNilCancellationAndInvalidBudget(t *testing.T) {
	s, _ := generationStore(t)
	a := generationAttemptFixture("attempt")
	a.StartedAt = time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if err := s.BeginSkillGenerationBudgeted(ctx, a, testGenerationBudget()); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	if err := s.BeginSkillGenerationBudgeted(context.Background(), a, skills.GenerationBudget{}); err == nil {
		t.Fatal("invalid budget accepted")
	}
	var absent *Store
	if err := absent.BeginSkillGenerationBudgeted(context.Background(), a, testGenerationBudget()); err == nil {
		t.Fatal("nil store accepted")
	}
}
