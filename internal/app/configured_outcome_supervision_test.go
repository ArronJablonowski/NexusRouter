package app

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func enableOutcomeSupervisionFixture(t *testing.T, service *Service) {
	t.Helper()
	p := config.Defaults().Skills.OutcomeRollbackSupervisor
	p.Enabled = true
	p.Interval = "1s"
	p.ModelID = "a"
	p.Domain = "creative"
	service.settings.Skills.OutcomeRollbackSupervisor = p
	if err := service.settings.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredOutcomeSupervisionDisabledWithoutStores(t *testing.T) {
	service, err := NewService(config.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareConfiguredOutcomeSupervision(service)
	if err != nil {
		t.Fatal(err)
	}
	running, err := plan.Start(context.Background())
	if err != nil || running.Health().Status != "disabled" || running.Health().Validate() != nil {
		t.Fatal(running, err)
	}
	if err = running.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredOutcomeSupervisionStartsWaitsAndJoins(t *testing.T) {
	service, _, _, _ := appOutcomeRollbackFixture(t, 0)
	enableOutcomeSupervisionFixture(t, service)
	plan, err := PrepareConfiguredOutcomeSupervision(service)
	if err != nil {
		t.Fatal(err)
	}
	running, err := plan.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for running.Health().Status == "unknown" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if check := running.Health(); check.Status != "healthy" || check.Validate() != nil {
		t.Fatal(check)
	}
	if err = running.Close(); err != nil {
		t.Fatal(err)
	}
	if check := running.Health(); check.Status != "unavailable" || check.Code != "supervisor_stopped" {
		t.Fatal(check)
	}
}

func TestConfiguredOutcomeSupervisionRejectsSettingsDrift(t *testing.T) {
	service, _, _, _ := appOutcomeRollbackFixture(t, 0)
	enableOutcomeSupervisionFixture(t, service)
	plan, err := PrepareConfiguredOutcomeSupervision(service)
	if err != nil {
		t.Fatal(err)
	}
	service.settings.Skills.OutcomeRollbackSupervisor.MinDrop = .2
	if running, startErr := plan.Start(context.Background()); startErr == nil || running != nil {
		t.Fatal("drifted plan started", startErr)
	}
}
