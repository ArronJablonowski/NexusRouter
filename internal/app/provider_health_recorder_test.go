package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
)

type providerHealthWriterStub struct {
	calls atomic.Int64
	done  chan health.Report
	err   error
}

func (s *providerHealthWriterStub) RecordProviderHealth(_ context.Context, report health.Report, _ int) error {
	s.calls.Add(1)
	if s.done != nil {
		s.done <- report
	}
	return s.err
}

func recorderHealthReport(at time.Time) health.Report {
	report := health.Report{Version: 1, CheckedAt: at.UTC(), Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "provider", ID: "ollama", Status: "healthy", Code: "available"},
		{Component: "model", ID: "worker", Status: "healthy", Code: "available"},
	}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	return report
}

func TestProviderHealthRecorderSamplesImmediatelyAndJoins(t *testing.T) {
	written := make(chan health.Report, 1)
	writer := &providerHealthWriterStub{done: written}
	want := recorderHealthReport(time.Unix(1000, 0))
	recorder, err := StartProviderHealthRecorder(context.Background(), writer, func(context.Context) (health.Report, error) {
		return want, nil
	}, 5*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-written:
		if !got.CheckedAt.Equal(want.CheckedAt) {
			t.Fatal("wrong sample written")
		}
	case <-time.After(time.Second):
		t.Fatal("initial sample was not recorded")
	}
	recorder.Close()
	attempt, success, lastErr := recorder.Snapshot()
	if attempt.IsZero() || success.IsZero() || lastErr != nil || writer.calls.Load() != 1 {
		t.Fatal("unexpected recorder state", attempt, success, lastErr, writer.calls.Load())
	}
}

func TestProviderHealthRecorderContainsFailures(t *testing.T) {
	written := make(chan health.Report, 1)
	writer := &providerHealthWriterStub{done: written, err: errors.New("storage failed")}
	recorder, err := StartProviderHealthRecorder(context.Background(), writer, func(context.Context) (health.Report, error) {
		return recorderHealthReport(time.Unix(1000, 0)), nil
	}, 5*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("initial sample was not attempted")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, _, lastErr := recorder.Snapshot()
		if lastErr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed sample was not reflected")
		}
		time.Sleep(time.Millisecond)
	}
	recorder.Close()
}

func TestProviderHealthRecorderRejectsTypedNilWriter(t *testing.T) {
	var writer *providerHealthWriterStub
	if recorder, err := StartProviderHealthRecorder(context.Background(), writer, func(context.Context) (health.Report, error) {
		return recorderHealthReport(time.Unix(1000, 0)), nil
	}, 5*time.Second, 10); !errors.Is(err, ErrHealth) || recorder != nil {
		t.Fatal("typed-nil writer accepted", recorder, err)
	}
}

func TestProviderHealthRecorderCloseIsConcurrentAndRepeatable(t *testing.T) {
	written := make(chan health.Report, 1)
	recorder, err := StartProviderHealthRecorder(context.Background(), &providerHealthWriterStub{done: written}, func(context.Context) (health.Report, error) {
		return recorderHealthReport(time.Unix(1000, 0)), nil
	}, 5*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("initial sample was not recorded")
	}
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			recorder.Close()
			done <- struct{}{}
		}()
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("concurrent close did not return")
		}
	}
	recorder.Close()
}
