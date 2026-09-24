package app

import (
	"context"
	"testing"
	"time"
)

func TestDispatcherReusesValidatedTaskStorage(t *testing.T) {
	s, _ := autoFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := 0; i < 4; i++ {
		db, release, err := s.taskStorage(ctx, i%2 == 0)
		if err != nil {
			t.Fatal(err)
		}
		if db != d.db {
			t.Fatal("request reopened storage")
		}
		release()
	}
	status, err := s.Submit(ctx, "sqlite-reuse-0001", Request{ModelID: "a", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	wait, cancelWait := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWait()
	ticks := time.NewTicker(10 * time.Millisecond)
	defer ticks.Stop()
	status, err = waitForSubmission(wait, status, ticks.C, s.SubmissionStatus)
	if err != nil || status.State != "succeeded" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	db, release, err := s.openTaskStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if db == d.db {
		t.Fatal("closed dispatcher store retained")
	}
}

func TestActiveTaskStorageDoesNotReplaceClosedHandle(t *testing.T) {
	s, _ := autoFixture(t)
	ctx := context.Background()
	db, release, err := s.openTaskStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.taskStore = db
	release()
	if _, _, err = s.openTaskStore(ctx); err == nil {
		t.Fatal("closed active store silently replaced")
	}
}
