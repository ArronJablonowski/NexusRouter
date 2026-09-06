package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRegressionMonitorCatalogRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"schema", "missing-monitor", "pending-pointer", "policy", "duplicate-sequence", "terminal-without-receipt", "invalid-code", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, _, _, _ := regressionFixture(t)
			state, p := prepareMonitorFixture(t, s, key)
			var c catalog
			if err := s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			index := (Key{key.Scope, "monitor"}).index()
			switch mode {
			case "schema":
				c.Schema = 4
			case "missing-monitor":
				delete(c.RegressionMonitors, index)
			case "pending-pointer":
				state.PendingOperationID = "wrong"
				c.RegressionMonitors[index] = state
			case "policy":
				p.PolicyDigest = strings.Repeat("b", 64)
				c.RegressionMonitorChecks[p.OperationID] = p
			case "duplicate-sequence":
				other := p
				other.OperationID = "other"
				c.RegressionMonitorChecks[other.OperationID] = other
			case "terminal-without-receipt":
				p.Status = "completed"
				p.FinishedAt = time.Now().UTC()
				state.Revision++
				state.PendingOperationID = ""
				c.RegressionMonitors[index] = state
				c.RegressionMonitorChecks[p.OperationID] = p
			case "invalid-code":
				p.Code = "secret"
				c.RegressionMonitorChecks[p.OperationID] = p
			case "wrong-key":
				delete(c.RegressionMonitorChecks, p.OperationID)
				c.RegressionMonitorChecks["wrong"] = p
			}
			if err := s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			if _, err := s.RegressionMonitorState(context.Background(), key.Scope, "monitor"); err == nil {
				t.Fatal("corrupt catalog accepted")
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestRegressionMonitorCapacityBeforePreparation(t *testing.T) {
	for _, mode := range []string{"monitors", "checks"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, _, _, _ := regressionFixture(t)
			ctx := context.Background()
			_, p := prepareMonitorFixture(t, s, key)
			if _, err := s.ExecuteRegressionMonitorCheck(ctx, p, pass, allowRegressionMonitor); err != nil {
				t.Fatal(err)
			}
			d := sample()
			d.Key.Name = "zz-last"
			v, err := s.Draft(ctx, d, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Activate(ctx, d.Key, v.ID, "", pass, false); err != nil {
				t.Fatal(err)
			}
			if err = s.with(ctx, func(c *catalog) error {
				index := (Key{key.Scope, "monitor"}).index()
				state := c.RegressionMonitors[index]
				state.NextDue = time.Now().UTC().Add(-time.Second)
				if mode == "monitors" {
					for i := range 999 {
						copy := state
						copy.Name = fmt.Sprintf("monitor-%d", i)
						copy.Revision = 0
						copy.After = ""
						c.RegressionMonitors[(Key{copy.Scope, copy.Name}).index()] = copy
					}
				} else {
					state.Revision = 2001
					for i := range 999 {
						copy := p
						copy.Sequence = int64(i + 3)
						copy.OperationID = fmt.Sprintf("check-%d", i)
						copy.Status = "failed"
						copy.Code = "check_failed"
						copy.FinishedAt = time.Now().UTC()
						c.RegressionMonitorChecks[copy.OperationID] = copy
					}
				}
				c.RegressionMonitors[index] = state
				return nil
			}, true); err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			calls := 0
			guard := RegressionMonitorGuard(func(context.Context, RegressionMonitorState, RegressionMonitorCheck) error { calls++; return nil })
			name := "monitor"
			if mode == "monitors" {
				name = "new-monitor"
			}
			if _, _, err = s.PrepareRegressionMonitor(ctx, key.Scope, name, "validator", strings.Repeat("a", 64), time.Second, guard); !errors.Is(err, ErrInvalid) || calls != 0 {
				t.Fatal(err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestRegressionMonitorErrorAdvancesToOtherSkill(t *testing.T) {
	s, _, key, _, _, _ := regressionFixture(t)
	ctx := context.Background()
	d := sample()
	d.Key.Name = "zz-last"
	v, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, d.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	_, p := prepareMonitorFixture(t, s, key)
	if _, err = s.ExecuteRegressionMonitorCheck(ctx, p, ValidatorFunc(func(context.Context, Version) (Evidence, error) { return Evidence{}, errors.New("invalid") }), allowRegressionMonitor); err != nil {
		t.Fatal(err)
	}
	if err = s.with(ctx, func(c *catalog) error {
		index := (Key{key.Scope, "monitor"}).index()
		state := c.RegressionMonitors[index]
		state.NextDue = time.Now().UTC().Add(-time.Second)
		c.RegressionMonitors[index] = state
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	_, next := prepareMonitorFixture(t, s, key)
	if next.Expected.Key != d.Key || next.OperationID == p.OperationID || next.PreviousCursor != key.Name {
		t.Fatal(next)
	}
}

func TestRegressionMonitorSchemaFiveSurvivesOtherMutations(t *testing.T) {
	s, _, key, _, active, _ := regressionFixture(t)
	ctx := context.Background()
	_, p := prepareMonitorFixture(t, s, key)
	if _, err := s.ExecuteRegressionMonitorCheck(ctx, p, pass, allowRegressionMonitor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevalidateAndRollbackOnce(ctx, "standalone", "validator", p.Expected, pass); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackAt(ctx, p.Expected, false); err != nil {
		t.Fatal(err)
	}
	state, err := s.ActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateOnce(ctx, "activate", state, active, pass, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishGeneration(ctx, generationAttemptFixture("drafted"), false); err != nil {
		t.Fatal(err)
	}
	var c catalog
	if err = s.read("catalog.json", &c); err != nil || c.Schema != 5 {
		t.Fatal(c.Schema, err)
	}
	if _, err = s.RegressionMonitorCheck(ctx, key.Scope, "monitor", p.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegressionOperation(ctx, key, p.OperationID); err != nil {
		t.Fatal(err)
	}
}
