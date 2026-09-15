package v1_test

import (
	"context"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func enableSDKOutcomeSupervision(t *testing.T, options sdk.ConfigOptions) *sdk.Client {
	t.Helper()
	if options.Overrides == nil {
		options.Overrides = map[string]string{}
	}
	options.Overrides["skills.outcome_rollback_supervisor.enabled"] = "true"
	options.Overrides["skills.outcome_rollback_supervisor.model_id"] = "chat"
	options.Overrides["skills.outcome_rollback_supervisor.domain"] = "creative"
	options.Overrides["skills.outcome_rollback_supervisor.interval"] = "1s"
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSDKOutcomeSupervisionInspectionAndOwnedMonitor(t *testing.T) {
	_, options, _, _, expected, _ := sdkOutcomeRollbackEvidence(t)
	client := enableSDKOutcomeSupervision(t, options)
	candidate, err := client.OutcomeRollbackCandidate(context.Background(), expected.Key)
	if err != nil || candidate.Validate() != nil || candidate.Current != expected {
		t.Fatal(candidate, err)
	}
	readiness, err := client.InspectOutcomeRollbackReadiness(context.Background(), expected.Key)
	if err != nil || readiness.Validate() != nil || readiness.Status != "ready" || readiness.Candidate != candidate {
		t.Fatal(readiness, err)
	}
	monitor, err := client.StartOutcomeSupervision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for monitor.Health().Status == "unknown" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if check := monitor.Health(); check.Validate() != nil || check.Status != "healthy" || check.Component != "outcome_supervision" {
		t.Fatal(check)
	}
	state, err := client.OutcomeSupervisionState(context.Background())
	if err != nil || state.Validate() != nil || state.PendingCheckID != "" || state.After != expected.Key.Name {
		t.Fatal(state, err)
	}
	if again, err := client.DurableOutcomeSupervisionStep(context.Background()); err != nil || again != state {
		t.Fatal(again, err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if check := monitor.Health(); check.Validate() != nil || check.Status != "unavailable" {
		t.Fatal(check)
	}
	if err := (*sdk.OutcomeSupervisionMonitor)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSDKConfiguredOutcomeSupervisionDisabledHandle(t *testing.T) {
	client, err := sdk.New(sdk.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := client.StartConfiguredOutcomeSupervision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if check := supervisor.Health(); check.Validate() != nil || check.Status != "disabled" || check.Component != "outcome_supervision" {
		t.Fatal(check)
	}
	if err = supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err = (*sdk.ConfiguredOutcomeSupervision)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}
