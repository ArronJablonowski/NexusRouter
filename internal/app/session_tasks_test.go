package app

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestListSessionTasksMissingDatabaseIsReadOnly(t *testing.T) {
	s := submissionService(t)
	page, err := s.ListSessionTasks(context.Background(), "session", sessions.SessionTaskListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || page.SessionID != "session" || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("session task listing created storage", err)
	}
}

func TestListSessionTasksRejectsCredentialIdentity(t *testing.T) {
	s := submissionService(t)
	const secret = "private-session-task-credential"
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
	page, err := s.ListSessionTasks(context.Background(), "session", sessions.SessionTaskListOptions{Limit: 25})
	if !errors.Is(err, ErrInspection) || page.Version != 0 || len(page.Items) != 0 {
		t.Fatal("credential identity escaped", page, err)
	}
}

func TestListSessionTasksRechecksRotatedCredentials(t *testing.T) {
	s := submissionService(t)
	const secret = "rotated-private-session-task"
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
	page, err := s.ListSessionTasks(context.Background(), "session", sessions.SessionTaskListOptions{Limit: 25})
	if !errors.Is(err, ErrInspection) || page.Version != 0 || lookups.Load() < 2 {
		t.Fatal("rotated credential identity escaped", page, err, lookups.Load())
	}
}
