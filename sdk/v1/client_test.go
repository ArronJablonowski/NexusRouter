package v1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestUninitializedClientRejectsAllOperations(t *testing.T) {
	ctx := context.Background()
	for _, client := range []*Client{nil, {}} {
		_, a := client.Run(ctx, Request{Version: 1})
		_, b := client.RunStream(ctx, Request{Version: 1}, func(runtime.Event) error { return nil })
		_, c := client.CancelTask(ctx, "task")
		_, d := client.SteerTask(ctx, "task", "key", "text")
		_, e := client.SteeringStatus(ctx, "task", "message")
		_, f := client.ListSteering(ctx, "task")
		g := client.Feedback(ctx, "task", true, 0)
		_, h := client.FeedbackHistory(ctx, "task")
		i := client.ReviseFeedback(ctx, "task", "expected", true)
		_, j := client.RunTextStream(ctx, Request{Version: 1}, func(string) error { return nil })
		for _, err := range []error{a, b, c, d, e, f, g, h, i, j} {
			if !errors.Is(err, ErrAdmission) {
				t.Fatal(err)
			}
		}
	}
	if ErrAdmission != app.ErrAdmission || ErrEventDelivery != app.ErrEventDelivery {
		t.Fatal("error identities changed")
	}
}

func TestSDKConfigAndVersionFailBeforeStorage(t *testing.T) {
	t.Setenv("DARWIN__RUNTIME__MAX_TURNS", "invalid-private-environment")
	path := filepath.Join(t.TempDir(), "missing.db")
	overrides := map[string]string{"telemetry.database": path}
	client, err := New(ConfigOptions{Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	overrides["telemetry.database"] = "private replacement"
	if client.database != path {
		t.Fatal("map mutation changed client")
	}
	for _, version := range []int{0, -1, 2} {
		out, err := client.Run(context.Background(), Request{Version: version, Prompt: "hello"})
		if !errors.Is(err, ErrAdmission) || out.Version != 1 || out.TaskID != "" {
			t.Fatal(out, err)
		}
		out, err = client.RunTextStream(context.Background(), Request{Version: version}, func(string) error { t.Fatal("invalid request reached callback"); return nil })
		if !errors.Is(err, ErrAdmission) || out.Version != 1 || out.TaskID != "" {
			t.Fatal(out, err)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created storage", err)
	}
	if _, err = New(ConfigOptions{Overrides: map[string]string{"private_unknown": "private_value"}}); err != ErrAdmission {
		t.Fatal(err)
	}
}

func TestSDKExplicitEnvironmentAndOverridePrecedence(t *testing.T) {
	environment := map[string]string{"runtime.max_turns": "2"}
	overrides := map[string]string{"runtime.max_turns": "3"}
	if _, err := New(ConfigOptions{Environment: environment, Overrides: overrides}); err != nil {
		t.Fatal(err)
	}
	environment["runtime.max_turns"] = "private-invalid"
	if _, err := New(ConfigOptions{Environment: environment, Overrides: overrides}); err != ErrAdmission {
		t.Fatal("invalidexplicitenvironment accepted", err)
	}
}

func TestSDKRequestResultFieldParity(t *testing.T) {
	public := reflect.TypeFor[Request]()
	internal := reflect.TypeFor[app.Request]()
	for i := 0; i < internal.NumField(); i++ {
		field := internal.Field(i)
		if !field.IsExported() {
			continue
		}
		other, ok := public.FieldByName(field.Name)
		if !ok || other.Type != field.Type {
			t.Fatalf("request field missing %s", field.Name)
		}
	}
	public = reflect.TypeFor[Result]()
	internal = reflect.TypeFor[app.Result]()
	for i := 0; i < internal.NumField(); i++ {
		field := internal.Field(i)
		if !field.IsExported() {
			continue
		}
		other, ok := public.FieldByName(field.Name)
		if !ok || other.Type != field.Type {
			t.Fatalf("result field missing %s", field.Name)
		}
	}
}
