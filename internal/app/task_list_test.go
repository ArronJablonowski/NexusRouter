package app

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestListTasksMissingDatabaseIsReadOnly(t *testing.T) {
	s := submissionService(t)
	page, err := s.ListTasks(context.Background(), sessions.TaskListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("task listing created storage", err)
	}
}

func TestListTasksRejectsCredentialIdentity(t *testing.T) {
	s := submissionService(t)
	const secret = "private-task-credential"
	s.secret = func(string) string { return secret }
	db, err := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "start", TaskID: secret, SessionID: "session", CorrelationID: secret, Sequence: 1, Time: time.Unix(100, 0), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	db.Close()
	page, err := s.ListTasks(context.Background(), sessions.TaskListOptions{Limit: 25})
	if !errors.Is(err, ErrInspection) || page.Version != 0 || len(page.Items) != 0 {
		t.Fatal("credential identity escaped", page, err)
	}
}

func TestListTasksRechecksRotatedCredentials(t *testing.T) {
	s := submissionService(t)
	const secret = "rotated-private-task"
	var lookups atomic.Int32
	s.secret = func(string) string {
		if lookups.Add(1) > 1 {
			return secret
		}
		return ""
	}
	db, err := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "start", TaskID: secret, SessionID: "session", CorrelationID: secret, Sequence: 1, Time: time.Unix(100, 0), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	db.Close()
	page, err := s.ListTasks(context.Background(), sessions.TaskListOptions{Limit: 25})
	if !errors.Is(err, ErrInspection) || page.Version != 0 || lookups.Load() < 2 {
		t.Fatal("rotated credential identity escaped", page, err, lookups.Load())
	}
}
