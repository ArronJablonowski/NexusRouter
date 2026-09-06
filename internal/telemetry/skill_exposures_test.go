package telemetry

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func exposureEvent(task string, use *runtime.SkillContextUse) runtime.Event {
	e := event(task+"-start", 1, runtime.TaskStarted)
	e.TaskID = task
	e.SessionID = task
	e.CorrelationID = task
	e.Data.SkillContext = use
	e.Data.Privacy = "local_only"
	return e
}
func exposureUse() *runtime.SkillContextUse {
	return &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "skill", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
}

func TestSkillExposuresAtomicIdempotentAndIndexed(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	e := exposureEvent("task", exposureUse())
	if _, err := s.db.Exec(`CREATE TRIGGER reject_exposure BEFORE INSERT ON skill_exposures BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, 0, e); err == nil {
		t.Fatal("exposure failure acknowledged")
	}
	var n int
	for _, table := range []string{"events", "task_heads", "workflow_scan_tasks", "skill_exposures"} {
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial append", table, n, err)
		}
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_exposure`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Append(ctx, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	for i, use := range []*runtime.SkillContextUse{nil, {Version: 1, Complete: true}, {Version: 1, Complete: false}} {
		if err := s.Append(ctx, 0, exposureEvent(string(rune('a'+i)), use)); err != nil {
			t.Fatal(err)
		}
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var task, privacy string
	var ordinal int64
	if err = ro.db.QueryRow(`SELECT task_id,ordinal,privacy FROM skill_exposures WHERE scope=? AND name=? AND version=? AND privacy=? ORDER BY ordinal DESC,task_id`, "project", "skill", e.Data.SkillContext.References[0].Version, "local_only").Scan(&task, &ordinal, &privacy); err != nil || task != "task" || ordinal != 1 || privacy != "local_only" {
		t.Fatal(task, ordinal, privacy, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM skill_exposures").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var a, b, c int
	var detail string
	if err = s.db.QueryRow(`EXPLAIN QUERY PLAN SELECT task_id FROM skill_exposures WHERE scope='project' AND name='skill' AND version='v' AND privacy='local_only' ORDER BY ordinal DESC,task_id`).Scan(&a, &b, &c, &detail); err != nil || !strings.Contains(detail, "skill_exposures_lookup") {
		t.Fatal(detail, err)
	}
}

func TestSkillExposuresMigration26BackfillAndRollback(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "backfill", true: "rollback"}[corrupt], func(t *testing.T) {
			s, path := generationStore(t)
			ctx := context.Background()
			e := exposureEvent("task", exposureUse())
			if err := s.Append(ctx, 0, e); err != nil {
				t.Fatal(err)
			}
			if err := s.Append(ctx, 0, exposureEvent("legacy", nil)); err != nil {
				t.Fatal(err)
			}
			before, err := s.Read(ctx, "task", 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`DROP TABLE skill_exposures; DROP INDEX task_heads_session; PRAGMA user_version=26`); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if _, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.skill_context.references[0].digest','bad') WHERE task_id='task'`); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(ctx, path)
			if corrupt {
				if err == nil {
					opened.Close()
					t.Fatal("corrupt migration accepted")
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var version, tables int
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 26 || db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='skill_exposures'").Scan(&tables) != nil || tables != 0 {
					t.Fatal("partial migration", version, tables)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			after, err := opened.Read(ctx, "task", 0, 10)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("migration rewrote journal", err)
			}
			var n, version int
			if opened.db.QueryRow("SELECT count(*) FROM skill_exposures").Scan(&n) != nil || n != 1 || opened.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 27 {
				t.Fatal(n, version)
			}
			if err = opened.initialize(ctx); err != nil {
				t.Fatal("reopen", err)
			}
		})
	}
}
