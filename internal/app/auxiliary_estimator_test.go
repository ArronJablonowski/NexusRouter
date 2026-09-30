package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type auxiliaryContextEstimator func(context.Context, providers.Request) (int, error)

func (f auxiliaryContextEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

func TestAuxiliaryEstimatorFailureIsDurableAndSourceIsUnchanged(t *testing.T) {
	for _, operation := range []string{"audit", "summary"} {
		for _, mode := range []string{"high", "error", "panic"} {
			t.Run(operation+"_"+mode, func(t *testing.T) {
				svc, cfg := autoFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Preserve runtime-secret and provider-secret", Domain: "creative"})
				if err != nil {
					t.Fatal(err)
				}
				db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				before, err := db.Read(ctx, source.TaskID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				var dispatches, estimates atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					dispatches.Add(1)
					fmt.Fprintln(w, `{"error":"should not dispatch auxiliary model"}`)
				}))
				defer server.Close()
				svc.settings.Providers[0].Endpoint = server.URL
				svc.settings.Providers[0].APIKeyEnv = "AUXILIARY_KEY"
				svc.secret = func(name string) string {
					switch name {
					case "DARWIN_API_TOKEN":
						return "runtime-secret"
					case "AUXILIARY_KEY":
						return "provider-secret"
					}
					return ""
				}
				svc.contextEstimator = auxiliaryContextEstimator(func(estimateCtx context.Context, r providers.Request) (int, error) {
					estimates.Add(1)
					deadline, ok := estimateCtx.Deadline()
					if !ok || time.Until(deadline) > 3*time.Second {
						t.Error("unbounded auxiliary estimator")
					}
					if r.Model != "z" || len(r.Tools) != 0 {
						t.Error("wrong auxiliary context", r.Model, len(r.Tools))
					}
					body, err := json.Marshal(r)
					if err != nil || strings.Contains(string(body), "runtime-secret") || strings.Contains(string(body), "provider-secret") || !strings.Contains(string(body), "[REDACTED]") {
						t.Error("unredacted auxiliary estimate", string(body), err)
					}
					if operation == "audit" {
						attempts, err := db.ReviewAttempts(ctx, source.TaskID, "", 100)
						if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
							t.Errorf("audit estimate before durable attempt: %+v %v", attempts, err)
						}
					} else {
						attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
						if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
							t.Errorf("summary estimate before durable attempt: %+v %v", attempts, err)
						}
					}
					switch mode {
					case "error":
						return 0, errors.New("private-estimator-error")
					case "panic":
						panic("private-estimator-error")
					}
					return 8193, nil
				})
				if operation == "audit" {
					_, err = svc.AuditTask(ctx, source.TaskID, "z", 0)
				} else {
					_, err = svc.SummarizeTask(ctx, source.TaskID, "z", 1, 0)
				}
				if err == nil || strings.Contains(err.Error(), "private-estimator-error") || estimates.Load() != 1 || dispatches.Load() != 0 {
					t.Fatal("auxiliary estimate bypassed or leaked", err, estimates.Load(), dispatches.Load())
				}
				var encoded []byte
				if operation == "audit" {
					attempts, readErr := db.ReviewAttempts(ctx, source.TaskID, "", 100)
					if readErr != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != "review_failed" {
						t.Fatal(attempts, readErr)
					}
					encoded, _ = json.Marshal(attempts)
				} else {
					attempts, readErr := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
					if readErr != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != "summary_failed" {
						t.Fatal(attempts, readErr)
					}
					encoded, _ = json.Marshal(attempts)
				}
				if strings.Contains(string(encoded), "private-estimator-error") || strings.Contains(string(encoded), "runtime-secret") || strings.Contains(string(encoded), "provider-secret") {
					t.Fatal("private callback data persisted in attempt")
				}
				after, err := db.Read(ctx, source.TaskID, 0, 100)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("auxiliary estimate modified source journal", err)
				}
			})
		}
	}
}

func TestAutomaticAuditEstimatorDenialPreservesParentSuccess(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.settings.Evaluation.Judge = true
	svc.settings.Evaluation.AutoReviewModel = "z"
	svc.settings.Evaluation.AutoReviewMaxCost = 0
	var dispatches, audits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
		}
		dispatches.Add(1)
		if body.Model != "a" {
			t.Error("denied auditor dispatched", body.Model)
		}
		fmt.Fprintln(w, `{"message":{"content":"accepted parent"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, r providers.Request) (int, error) {
		if r.Model == "z" {
			audits.Add(1)
			return 8193, nil
		}
		return 0, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello"})
	if err != nil || out.Text != "accepted parent" || out.AuditStatus != "failed" || out.AuditID != "" || dispatches.Load() != 1 || audits.Load() != 1 {
		t.Fatal("auxiliary denial changed parent result", out, err, dispatches.Load(), audits.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := db.TaskSnapshot(ctx, out.TaskID)
	if err != nil || snapshot.State != "completed" {
		t.Fatal("parent completion lost", snapshot, err)
	}
	attempts, err := db.ReviewAttempts(ctx, out.TaskID, "", 100)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" {
		t.Fatal("missing audit failure evidence", attempts, err)
	}
}
