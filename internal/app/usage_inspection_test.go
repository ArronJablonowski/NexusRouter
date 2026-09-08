package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func seedUsageTask(t *testing.T, service *Service, task, session string) {
	t.Helper()
	db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	e := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: session, CorrelationID: task, Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, e); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectTaskUsageReadOnlyAndRestartSafe(t *testing.T) {
	s := submissionService(t)
	seedUsageTask(t, s, "task", "session")
	got, err := s.InspectTaskUsage(context.Background(), "task")
	if err != nil || got.Validate() != nil || got.Scope.TaskID != "task" || got.Scope.SessionID != "session" || got.Coverage != "complete" {
		t.Fatal(got, err)
	}
	restarted := &Service{settings: s.settings}
	again, err := restarted.InspectTaskUsage(context.Background(), "task")
	if err != nil || again.Validate() != nil || again.Scope != got.Scope {
		t.Fatal(again, err)
	}
	if _, err = restarted.InspectTaskUsage(context.Background(), "missing"); err == nil {
		t.Fatal("missing task accepted")
	}
}

func TestInspectTaskUsageGuardsDoNotCreateOrLeakSecrets(t *testing.T) {
	s := submissionService(t)
	for _, tc := range []struct {
		ctx  context.Context
		task string
	}{
		{nil, "task"}, {context.Background(), ""}, {context.Background(), "bad:task"},
	} {
		if _, err := s.InspectTaskUsage(tc.ctx, tc.task); !errors.Is(err, ErrAdmission) {
			t.Fatal(tc, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.InspectTaskUsage(canceled, "task"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created storage", err)
	}
	const secret = "current-secret-collision"
	seedUsageTask(t, s, secret, "session")
	s.secret = func(string) string { return secret }
	if _, err := s.InspectTaskUsage(context.Background(), secret); !errors.Is(err, ErrUsageInspection) {
		t.Fatal("secret collision released", err)
	}
}
