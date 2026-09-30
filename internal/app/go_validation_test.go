package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestGoValidationFailsTaskAndChangesOnlyValidatedRoute(t *testing.T) {
	svc, cfg := autoFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		text := "package p\nfunc bad( {"
		if req.Model == "z" {
			text = "package p\nfunc good() {}"
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	ctx := context.Background()
	failed, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Write a complete Go source file without markdown", Domain: "code", Validation: "go_source"})
	if !errors.Is(err, runtime.ErrInvalidOutput) || failed.TaskID == "" || failed.Text != "" {
		t.Fatal(failed, err)
	}
	if err := RecordFeedback(ctx, cfg.Telemetry.Database, failed.TaskID, true, 0); !errors.Is(err, ErrAdmission) {
		t.Fatal("syntax failure accepted as success", err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "a", Provider: "local", Domain: "code", Profile: "default"}
	v, err := db.OutputValidity(ctx, key, "go_source")
	if err != nil || v.Samples != 1 || v.Failures != 1 {
		t.Fatal(v, err)
	}
	if v, err := db.OutputValidity(ctx, key); err != nil || v.Samples != 0 {
		t.Fatal("syntax check leaked into generic population", v, err)
	}
	good, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "Write a complete Go file", Domain: "code", Validation: "go_source"})
	if err != nil || good.Text != "package p\nfunc good() {}" {
		t.Fatal(good, err)
	}
	plain, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "hello", Domain: "code"})
	if err != nil || plain.Text != "package p\nfunc bad( {" {
		t.Fatal("unrequested validation ran", plain, err)
	}
}

func TestGoValidationUsesRedactedDeliveredSource(t *testing.T) {
	svc, cfg := autoFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"package p\nvar privateIdentifier = 1"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "privateIdentifier"
		}
		return ""
	}
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Go source", Validation: "go_source"})
	if !errors.Is(err, runtime.ErrInvalidOutput) {
		t.Fatal("unredacted source was validated", out, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.OutputValidity(ctx, routing.Key{Model: "a", Provider: "local", Domain: "code", Profile: "default"}, "go_source")
	if err != nil || v.Samples != 1 || v.Failures != 1 {
		t.Fatal("persisted source disagrees with validator", v, err)
	}
}
