package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
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

func TestDurableOutcomeSupervisionWaitingPersistsCheckAndJournal(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 20)
	configureOutcomeSupervision(svc, request)
	readiness, err := svc.InspectOutcomeRollbackReadiness(context.Background(), expected.Key)
	if err != nil || readiness.Status != "waiting" {
		t.Fatal(readiness, err)
	}
	state, err := svc.DurableOutcomeSupervisionStep(context.Background())
	if err != nil || state.Validate() != nil || state.After != expected.Key.Name || state.PendingCheckID != "" {
		t.Fatal(state, err)
	}
	inspected, err := svc.OutcomeSupervisionState(context.Background())
	if err != nil || inspected != state {
		t.Fatal(inspected, err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.OutcomeSupervisionEvents(context.Background(), readiness.OperationID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != telemetry.OutcomeSupervisionWaiting {
		t.Fatal(page, err)
	}
	check, err := svc.OutcomeSupervisionCheck(context.Background(), page.Items[0].CheckID)
	if err != nil || check.Status != "completed" || check.Code != "waiting" || check.OutcomeOperationID != readiness.OperationID {
		t.Fatal(check, err)
	}
	again, err := svc.DurableOutcomeSupervisionStep(context.Background())
	if err != nil || again != state {
		t.Fatal(again, err)
	}
}

func TestDurableOutcomeSupervisionReadySettlesExactReceipt(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 40)
	configureOutcomeSupervision(svc, request)
	readiness, err := svc.InspectOutcomeRollbackReadiness(context.Background(), expected.Key)
	if err != nil || readiness.Status != "ready" {
		t.Fatal(readiness, err)
	}
	state, err := svc.DurableOutcomeSupervisionStep(context.Background())
	if err != nil || state.Validate() != nil || state.PendingCheckID != "" {
		t.Fatal(state, err)
	}
	receipt, err := svc.OutcomeRollbackOperation(context.Background(), expected.Key, readiness.OperationID)
	if err != nil || receipt.Validate() != nil || receipt.Expected != expected {
		t.Fatal(receipt, err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.OutcomeSupervisionEvents(context.Background(), readiness.OperationID, 0, 10)
	if err != nil || len(page.Items) != 2 || page.Items[0].Code != telemetry.OutcomeSupervisionReady {
		t.Fatal(page, err)
	}
	wantTerminal := telemetry.OutcomeSupervisionNoAction
	if receipt.Decision == "rolled_back" {
		wantTerminal = telemetry.OutcomeSupervisionRolledBack
	}
	if page.Items[1].Code != wantTerminal || page.Items[1].CheckID != page.Items[0].CheckID {
		t.Fatal(page.Items)
	}
	check, err := svc.OutcomeSupervisionCheck(context.Background(), page.Items[0].CheckID)
	if err != nil || check.Status != "completed" || check.Code != "evaluated" || check.OutcomeOperationID != receipt.OperationID {
		t.Fatal(check, err)
	}
}

func TestDurableOutcomeSupervisionReconcilesReceiptAfterLostCompletion(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 40)
	configureOutcomeSupervision(svc, request)
	interval, err := configuredOutcomeSupervisionInterval(svc)
	if err != nil {
		t.Fatal(err)
	}
	policyID, err := svc.outcomeSupervisionPolicyDigest(interval)
	if err != nil {
		t.Fatal(err)
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	guard := skills.OutcomeSupervisionGuard(func(context.Context, skills.OutcomeSupervisionState, skills.OutcomeSupervisionCheck) error {
		return nil
	})
	_, check, err := store.PrepareOutcomeSupervision(context.Background(), expected.Key.Scope, configuredOutcomeSupervisorName, policyID, interval, guard)
	if err != nil {
		t.Fatal(err)
	}
	selectionPolicy, err := svc.skillComparisonSelectionPolicy(request)
	if err != nil {
		t.Fatal(err)
	}
	operationID := outcomeSupervisionOperationID(expected, selectionPolicy)
	check, err = store.BindOutcomeSupervisionOperation(context.Background(), check, operationID, guard)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	readiness, err := svc.inspectOutcomeRollbackCandidate(context.Background(), check.Candidate)
	if err != nil || readiness.Status != "ready" {
		t.Fatal(readiness, err)
	}
	if err = svc.recordOutcomeSupervisionEvent(context.Background(), check, policyID, telemetry.OutcomeSupervisionReady); err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.OutcomeRollbackPrepared(context.Background(), operationID, expected, request, readiness.Selection)
	if err != nil || receipt.Validate() != nil {
		t.Fatal(receipt, err)
	}
	// Simulate process loss here: the exact receipt exists but the durable
	// scheduler check remains pending and has no terminal journal event.
	state, err := svc.DurableOutcomeSupervisionStep(context.Background())
	if err != nil || state.Validate() != nil || state.PendingCheckID != "" {
		t.Fatal(state, err)
	}
	terminal, err := svc.OutcomeSupervisionCheck(context.Background(), check.CheckID)
	if err != nil || terminal.Status != "completed" || terminal.OutcomeOperationID != operationID {
		t.Fatal(terminal, err)
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
