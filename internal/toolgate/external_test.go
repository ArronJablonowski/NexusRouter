package toolgate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestExternalDecisionApprovesExactlyOneExecution(t *testing.T) {
	g, a := fixture(t)
	a = previewAuthorization(a)
	g.Review = nil
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shown := make(chan approvals.Request, 1)
	g.Present = func(_ context.Context, p tools.ApprovalPrompt) error {
		if string(p.Arguments) != string(a.Arguments) || p.Description != a.Description {
			t.Error("incorrect preview")
		}
		shown <- p.Request
		p.Arguments[0] = 'x'
		return nil
	}
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := g.ExecuteApproved(ctx, a, func(ctx context.Context) (runtime.ToolResult, error) {
			calls.Add(1)
			return runtime.ToolResult{Content: "written", Effect: runtime.ConfirmedEffect}, nil
		})
		done <- err
	}()
	var req approvals.Request
	select {
	case req = <-shown:
	case <-ctx.Done():
		t.Fatal("presenter not called")
	}
	select {
	case err := <-done:
		t.Fatal("presentation was treated as authority", err)
	case <-time.After(150 * time.Millisecond):
	}
	if calls.Load() != 0 || string(a.Arguments)[0] != '{' {
		t.Fatal("early execution or mutated input")
	}
	decision := approvals.Decision{ID: "external-decision", Actor: "authenticated-operator", Allowed: true, Time: time.Now().UTC()}
	if _, err := g.Store.DecideApproval(ctx, req.ID, decision); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("decision not observed")
	}
	r, err := g.Store.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Consumed || len(r.Decisions) != 1 || r.Decisions[0].ID != decision.ID || calls.Load() != 1 {
		t.Fatal(r, err, calls.Load())
	}
}

func TestExternalDecisionClosedStatesNeverDispatch(t *testing.T) {
	for _, state := range []string{"denied", "revoked", "consumed"} {
		t.Run(state, func(t *testing.T) {
			g, a := fixture(t)
			a = previewAuthorization(a)
			g.Review = nil
			g.Present = func(ctx context.Context, p tools.ApprovalPrompt) error {
				d := approvals.Decision{ID: "external-allow", Actor: "operator", Allowed: state != "denied", Time: time.Now().UTC()}
				if _, err := g.Store.DecideApproval(ctx, p.Request.ID, d); err != nil {
					return err
				}
				if state == "revoked" {
					d.ID, d.Allowed, d.Time = "external-revoke", false, time.Now().UTC()
					_, err := g.Store.DecideApproval(ctx, p.Request.ID, d)
					return err
				}
				if state == "consumed" {
					l, err := g.Store.AcquireLease(ctx, a.TaskID, "external-holder", a.Scope, true, time.Now().UTC(), time.Minute)
					if err != nil {
						return err
					}
					if _, err = g.Store.ConsumeApproval(ctx, p.Request, l.Token, l.Owner, time.Now().UTC()); err != nil {
						return err
					}
					return g.Store.ReleaseLease(ctx, l.Token, l.Owner)
				}
				return nil
			}
			out, err := g.ExecuteApproved(context.Background(), a, func(context.Context) (runtime.ToolResult, error) {
				t.Error("closed approval dispatched")
				return runtime.ToolResult{}, nil
			})
			if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect {
				t.Fatal(out, err)
			}
		})
	}
}

func TestExternalPresentationFailuresAndCancellation(t *testing.T) {
	for _, kind := range []string{"error", "panic", "context_cancel", "durable_cancel", "deadline", "ambiguous", "invalid_preview"} {
		t.Run(kind, func(t *testing.T) {
			g, a := fixture(t)
			a = previewAuthorization(a)
			g.Review = nil
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if kind == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
			}
			presented := false
			g.Present = func(ctx context.Context, p tools.ApprovalPrompt) error {
				presented = true
				switch kind {
				case "error":
					return errors.New("secret")
				case "panic":
					panic("secret")
				case "context_cancel":
					cancel()
				case "durable_cancel":
					_, err := g.Store.RequestCancellation(ctx, p.Request.TaskID)
					return err
				}
				return nil
			}
			if kind == "ambiguous" {
				g.ReviewPrompt = func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
					t.Error("ambiguous callback invoked")
					return "", false, nil
				}
			}
			if kind == "invalid_preview" {
				a.Arguments = nil
			}
			out, err := g.ExecuteApproved(ctx, a, func(context.Context) (runtime.ToolResult, error) {
				t.Error("failed presentation dispatched")
				return runtime.ToolResult{}, nil
			})
			if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect {
				t.Fatal(out, err)
			}
			if presented == (kind == "ambiguous" || kind == "invalid_preview") {
				t.Fatal("unexpected presentation", presented)
			}
		})
	}
}
