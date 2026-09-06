package v1_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKDurableSkillRegressionLifecycleAndGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	options, _ := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.rollback_on_regression": "true", "skills.root": root, "skills.scope": "project"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	key := skills.Key{Scope: "project", Name: "workflow"}
	draft := skills.Draft{Key: key, Description: "Workflow", SourceSessions: []string{"session"}, Steps: []string{"Inspect requirements"}, ValidationCases: []string{"Fixture"}}
	version, err := store.Draft(ctx, draft, false)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	if err := store.Activate(ctx, key, version.ID, "", pass, false); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	operation := ""
	validator := skills.ValidatorFunc(func(callCtx context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		pending, err := client.SkillRegressionMonitorState(callCtx, "sdk-monitor")
		if err != nil {
			return skills.Evidence{}, err
		}
		operation = pending.PendingOperationID
		if operation == "" || v.ID != version.ID {
			t.Error("callback missing durable identity")
		}
		return skills.Evidence{ID: "monitor-proof", Passed: true, Deterministic: true}, nil
	})
	state, err := client.DurableSkillRegressionStep(ctx, "sdk-monitor", "validator", time.Hour, validator)
	if err != nil || calls != 1 || state.PendingOperationID != "" || state.After != "workflow" {
		t.Fatal(state, err, calls)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.DurableSkillRegressionStep(ctx, "sdk-monitor", "validator", time.Hour, validator)
	if err != nil || !reflect.DeepEqual(again, state) || calls != 1 {
		t.Fatal("SDK cadence lost on restart", err)
	}
	check, err := restarted.SkillRegressionMonitorCheck(ctx, "sdk-monitor", operation)
	if err != nil || check.Status != "completed" || check.OperationID != operation {
		t.Fatal(check, err)
	}
	// Starting the same named loop must honor the saved due time, then join on Close.
	monitor, err := restarted.StartDurableSkillRegression(ctx, "sdk-monitor", "validator", time.Hour, validator)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- monitor.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor Close did not join")
	}
	if calls != 1 {
		t.Fatal("monitor restart ignored cadence")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	operations := []func(*sdk.Client, context.Context) error{
		func(c *sdk.Client, ctx context.Context) error {
			_, err := c.DurableSkillRegressionStep(ctx, "sdk-monitor", "validator", time.Hour, validator)
			return err
		},
		func(c *sdk.Client, ctx context.Context) error {
			_, err := c.SkillRegressionMonitorState(ctx, "sdk-monitor")
			return err
		},
		func(c *sdk.Client, ctx context.Context) error {
			_, err := c.SkillRegressionMonitorCheck(ctx, "sdk-monitor", operation)
			return err
		},
		func(c *sdk.Client, ctx context.Context) error {
			m, err := c.StartDurableSkillRegression(ctx, "sdk-monitor", "validator", time.Hour, validator)
			if m != nil {
				m.Close()
			}
			return err
		},
	}
	for _, op := range operations {
		for _, invalid := range []*sdk.Client{nil, {}} {
			if err := op(invalid, ctx); !errors.Is(err, sdk.ErrAdmission) {
				t.Fatal("uninitialized SDK", err)
			}
		}
		if err := op(client, nil); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal("nil context", err)
		}
		if err := op(client, canceled); !errors.Is(err, context.Canceled) {
			t.Fatal("canceled context", err)
		}
	}
}
