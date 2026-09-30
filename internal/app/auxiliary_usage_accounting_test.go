package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

func TestAuditOperationsSeparatePublicAndLegacyUsageRoles(t *testing.T) {
	svc, request := auditOperationSource(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auditResponse(t, w, r, "The literal output matches.")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "completed" {
		t.Fatal(status, err)
	}
	status, err = svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "completed" {
		t.Fatal("public replay", status, err)
	}
	if _, err := svc.AuditTask(context.Background(), request.TaskID, request.ReviewerModelID, 0); err != nil {
		t.Fatal("legacy audit", err)
	}

	db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	totals, err := db.UsageTotals(context.Background(), accounting.Scope{TaskID: request.TaskID})
	if err != nil || totals.OrchestratorAudit.Records != 1 || totals.OrchestratorAudit.UnknownUsageRecords != 1 || totals.OrchestratorAudit.KnownCostRecords != 1 || totals.Judge.Records != 1 || totals.Judge.UnknownUsageRecords != 1 || totals.Judge.KnownCostRecords != 1 || totals.Auxiliary.Records != 2 {
		t.Fatalf("audit usage roles: %#v %v", totals, err)
	}
}
