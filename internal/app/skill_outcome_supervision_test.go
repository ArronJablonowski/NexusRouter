package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func configureOutcomeSupervision(svc *Service, request skills.ComparisonSelectionRequest) {
	svc.settings.Skills.OutcomeRollbackSupervisor = config.OutcomeRollbackSupervisor{Version: 1, Enabled: true, Interval: "1s",
		ModelID: request.ModelID, Domain: request.Domain, Profile: request.Profile, Source: string(request.Source), Privacy: request.Privacy,
		MinSamples: request.MinSamples, MinDrop: request.MinDrop, TasksPerVersion: request.TasksPerVersion}
}

func TestInspectOutcomeRollbackReadinessWaitsWithoutCatalogWrite(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 20)
	configureOutcomeSupervision(svc, request)
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := svc.InspectOutcomeRollbackReadiness(context.Background(), expected.Key)
	if err != nil || readiness.Validate() != nil || readiness.Status != "waiting" || readiness.Candidate.Current != expected ||
		readiness.Selection.Comparison == nil || readiness.Selection.Comparison.Baseline.Samples >= request.MinSamples ||
		readiness.Selection.Comparison.Candidate.Samples >= request.MinSamples {
		t.Fatal(readiness, err)
	}
	again, err := svc.InspectOutcomeRollbackReadiness(context.Background(), expected.Key)
	if err != nil || again.OperationID != readiness.OperationID || again.Status != "waiting" {
		t.Fatal(again, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("readiness inspection created outcome state", err)
	}
	if _, err := svc.OutcomeRollbackOperation(context.Background(), expected.Key, readiness.OperationID); err == nil {
		t.Fatal("waiting inspection created a receipt")
	}
	changed := readiness.Selection.Policy
	changed.Comparison.MinDrop = .2
	if outcomeSupervisionOperationID(expected, changed) == readiness.OperationID {
		t.Fatal("operation id did not bind policy")
	}
	changedState := expected
	first := byte('a')
	if expected.Revision[0] == first {
		first = 'b'
	}
	changedState.Revision = string(append([]byte{first}, expected.Revision[1:]...))
	if outcomeSupervisionOperationID(changedState, readiness.Selection.Policy) == readiness.OperationID {
		t.Fatal("operation id did not bind activation revision")
	}
}

func TestOutcomeSupervisionReadyUsesPreparedSnapshot(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 40)
	configureOutcomeSupervision(svc, request)
	next, readiness, err := svc.OutcomeSupervisionStep(context.Background(), "")
	if err != nil || next != expected.Key.Name || readiness.Validate() != nil || readiness.Status != "ready" {
		t.Fatal(next, readiness, err)
	}
	receipt, err := svc.OutcomeRollbackOperation(context.Background(), expected.Key, readiness.OperationID)
	if err != nil || receipt.Validate() != nil || receipt.OperationID != readiness.OperationID || receipt.Expected != expected ||
		!reflect.DeepEqual(receipt.Selection, readiness.Selection) {
		t.Fatal(receipt, err)
	}
}

func TestOutcomeSupervisionMonitorImmediateHealthAndJoin(t *testing.T) {
	svc, _, request, _ := appOutcomeRollbackFixture(t, 20)
	configureOutcomeSupervision(svc, request)
	monitor, err := StartOutcomeSupervision(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for monitor.Health().Status == "unknown" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if check := monitor.Health(); check.Validate() != nil || check.Status != "healthy" || check.Component != "outcome_supervision" {
		t.Fatal(check)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
	if check := monitor.Health(); check.Validate() != nil || check.Status != "unavailable" || check.Code != "supervisor_stopped" {
		t.Fatal(check)
	}
	if check := (*OutcomeSupervisionMonitor)(nil).Health(); check.Validate() != nil || check.Status != "unknown" {
		t.Fatal(check)
	}
}

func TestOutcomeSupervisionMonitorContainsStepPanic(t *testing.T) {
	svc, _, request, _ := appOutcomeRollbackFixture(t, 20)
	configureOutcomeSupervision(svc, request)
	svc.secret = func(string) string { panic("private supervisor failure") }
	monitor, err := StartOutcomeSupervision(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for monitor.Health().Status == "unknown" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if check := monitor.Health(); check.Validate() != nil || check.Status != "degraded" || check.Code != "supervisor_error" {
		t.Fatal(check)
	}
	if err := monitor.Close(); err != ErrAdmission {
		t.Fatal("panic was not contained as a generic joined failure", err)
	}
}
