package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestConfiguredWorkboardSchedulePolicyDigestIsStableVersionedAndHostDerived(t *testing.T) {
	settings := config.Defaults()
	first, err := configuredWorkboardSchedulePolicyDigest(settings)
	second, secondErr := configuredWorkboardSchedulePolicyDigest(settings)
	configID, configErr := settingsConfigID(settings)
	if err != nil || secondErr != nil || configErr != nil || len(first) != 64 || first != second || first == configID {
		t.Fatalf("policy digest is not stable and domain-separated: %q %q %q errors=%v/%v/%v", first, second, configID, err, secondErr, configErr)
	}
	changed := settings
	changed.Workboard.Scheduler.Interval = "6s"
	different, err := configuredWorkboardSchedulePolicyDigest(changed)
	if err != nil || different == first {
		t.Fatalf("host configuration change did not invalidate policy: %q %q %v", first, different, err)
	}
	if configuredWorkboardSchedulePolicyVersion != 1 || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatal("policy digest contract drift", configuredWorkboardSchedulePolicyVersion, first)
	}
}

func TestConfiguredWorkboardSchedulePlanStartIsSingleUseAndCanceledStartDoesNotConsumeIt(t *testing.T) {
	var cycles atomic.Int32
	plan := &ConfiguredWorkboardSchedulePlan{
		lister: &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a")}}},
		scheduler: WorkboardCycleRunnerFunc(func(context.Context, string) (WorkboardScheduleResult, error) {
			cycles.Add(1)
			return WorkboardScheduleResult{}, nil
		}),
		interval: time.Hour,
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if supervisor, err := plan.Start(canceled); err == nil || supervisor != nil {
		t.Fatal("canceled start admitted")
	}
	supervisor, err := plan.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for cycles.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cycles.Load() != 1 {
		supervisor.Close()
		t.Fatal("successful Start did not own the immediate pass", cycles.Load())
	}
	if duplicate, duplicateErr := plan.Start(context.Background()); duplicateErr == nil || duplicate != nil {
		supervisor.Close()
		t.Fatal("single-use plan admitted a second owner")
	}
	if err = supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareConfiguredWorkboardScheduleRejectsDisabledAndInvalidInputs(t *testing.T) {
	settings := config.Defaults()
	settings.Telemetry.Database = filepath.Join(t.TempDir(), "disabled.db")
	service, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan, prepareErr := PrepareConfiguredWorkboardSchedule(context.Background(), service, nil); prepareErr == nil || plan != nil {
		t.Fatal("nil store admitted")
	}
	if plan, prepareErr := PrepareConfiguredWorkboardSchedule(context.Background(), nil, nil); prepareErr == nil || plan != nil {
		t.Fatal("nil service admitted")
	}
	store, err := telemetry.Open(context.Background(), settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if plan, prepareErr := PrepareConfiguredWorkboardSchedule(context.Background(), service, store); prepareErr == nil || plan != nil {
		t.Fatal("disabled scheduler admitted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if plan, prepareErr := PrepareConfiguredWorkboardSchedule(canceled, service, nil); prepareErr == nil || plan != nil {
		t.Fatal("canceled preparation admitted")
	}
}
