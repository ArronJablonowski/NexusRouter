package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestRemoteReviewSupervisorJoinsBeforeCleanup(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	s := startRemoteReviewSupervisor(context.Background(), func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		<-released
		return ctx.Err()
	})
	<-entered
	if !s.ready() || s.health().Validate() != nil || s.health().Status != "healthy" {
		t.Fatal(s.health())
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("close returned before worker finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(released)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close failed to join")
	}
	if s.ready() || s.health().Code != "supervisor_stopped" || s.health().Validate() != nil {
		t.Fatal(s.health())
	}
	s.Close()
	var disabled *remoteReviewSupervisor
	if !disabled.ready() || disabled.health().Validate() != nil || disabled.health().Status != "disabled" {
		t.Fatal(disabled.health())
	}
}
func TestRemoteReviewSupervisorUnexpectedExitIsUnhealthy(t *testing.T) {
	for _, err := range []error{nil, errors.New("private diagnostic")} {
		s := startRemoteReviewSupervisor(context.Background(), func(context.Context) error { return err })
		<-s.done
		if s.ready() || s.health().Code != "supervisor_error" || s.health().Validate() != nil {
			t.Fatal(s.health())
		}
		s.Close()
	}
}
func TestRemoteReviewSupervisorRealQueueFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jobs")
	q, err := remote.OpenReviewQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := remote.OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "malformed.job.json"), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	s := startRemoteReviewSupervisor(context.Background(), func(ctx context.Context) error {
		return (&remote.Client{}).RunReviewJobs(ctx, q, routes, "", func(bool) (remote.RemoteEvaluator, error) {
			t.Error("policy invoked for corrupt queue")
			return remote.RemoteEvaluator{}, remote.ErrInvalid
		})
	})
	defer s.Close()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("queue failure was swallowed")
	}
	if s.ready() || s.health().Code != "supervisor_error" {
		t.Fatal(s.health())
	}
}
