package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestChatApprovalSessionExactDecisionAndModelIsolation(t *testing.T) {
	for _, command := range []string{"approve", "deny"} {
		t.Run(command, func(t *testing.T) {
			cfg, p := chatApprovalFixture(t, "new file")
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			requests := make(chan chatApprovalRequest)
			reviewer := newChatReviewer(cfg, nil, requests)
			decision := make(chan bool, 1)
			hooks := chatHooks{Approvals: requests, RunLive: func(ctx context.Context, _ app.Request, event func(runtime.Event) error, text func(string) error) (app.Result, error) {
				if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: p.Request.TaskID}); err != nil {
					return app.Result{}, err
				}
				if err := text("/approve " + p.Request.ID + "\n"); err != nil {
					return app.Result{}, err
				}
				_, allowed, err := reviewer(ctx, p)
				decision <- allowed
				if err != nil || !allowed {
					return app.Result{}, tools.ErrDenied
				}
				return app.Result{TaskID: p.Request.TaskID}, nil
			}}
			out := &chatLiveOutput{visible: make(chan struct{})}
			lines := make(chan chatLine, 3)
			lines <- chatLine{Text: "create file"}
			done := make(chan int, 1)
			go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
			chatLiveWait(t, ctx, out, "[approval pending "+p.Request.ID+"]")
			select {
			case <-decision:
				t.Fatal("model text approved tool")
			default:
			}
			lines <- chatLine{Text: "/approve wrong-id"}
			chatLiveWait(t, ctx, out, "exact active request ID is required")
			select {
			case <-decision:
				t.Fatal("wrong ID decided request")
			default:
			}
			lines <- chatLine{Text: "/" + command + " " + p.Request.ID}
			select {
			case allowed := <-decision:
				if allowed != (command == "approve") {
					t.Fatal("wrong decision")
				}
			case <-ctx.Done():
				t.Fatal("decision not delivered")
			}
			close(lines)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("session did not join")
			}
			if !strings.Contains(out.String(), "| /approve "+p.Request.ID) {
				t.Fatal("model instruction not visibly untrusted")
			}
		})
	}
}

type chatApprovalFailOutput struct {
	out  *chatLiveOutput
	mode string
}

func (w chatApprovalFailOutput) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "[approval pending") {
		switch w.mode {
		case "panic":
			panic("fixture")
		case "short":
			return len(body) - 1, nil
		default:
			return 0, errors.New("fixture")
		}
	}
	return w.out.Write(body)
}

func TestChatApprovalSessionCancellationAndOutputFailure(t *testing.T) {
	for _, mode := range []string{"quit", "cancel", "eof", "context", "error", "short", "panic"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p := chatApprovalFixture(t, "new file")
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			requests := make(chan chatApprovalRequest)
			reviewer := newChatReviewer(cfg, nil, requests)
			decision := make(chan bool, 1)
			hooks := chatHooks{Approvals: requests, Run: func(ctx context.Context, _ app.Request, event func(runtime.Event) error) (app.Result, error) {
				if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: p.Request.TaskID}); err != nil {
					return app.Result{}, err
				}
				_, allowed, err := reviewer(ctx, p)
				decision <- allowed
				return app.Result{}, err
			}}
			out := &chatLiveOutput{visible: make(chan struct{})}
			var writer io.Writer = out
			broken := mode == "error" || mode == "short" || mode == "panic"
			if broken {
				writer = chatApprovalFailOutput{out, mode}
			}
			lines := make(chan chatLine, 2)
			lines <- chatLine{Text: "create file"}
			done := make(chan int, 1)
			go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, writer) }()
			if !broken {
				chatLiveWait(t, ctx, out, "[approval pending "+p.Request.ID+"]")
				switch mode {
				case "quit":
					lines <- chatLine{Text: "/quit"}
				case "cancel":
					lines <- chatLine{Text: "/cancel"}
				case "eof":
					close(lines)
				case "context":
					cancel()
				}
			}
			select {
			case allowed := <-decision:
				if allowed {
					t.Fatal("abandoned proposal approved")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reviewer not released")
			}
			if mode == "cancel" {
				close(lines)
			}
			select {
			case code := <-done:
				if broken && code != 1 {
					t.Fatal("output error not reported", code)
				}
			case <-time.After(time.Second):
				t.Fatal("session did not join")
			}
		})
	}
}

func TestChatApprovalSessionEOFBeforeProposal(t *testing.T) {
	cfg, p := chatApprovalFixture(t, "new file")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	requests := make(chan chatApprovalRequest)
	reviewer := newChatReviewer(cfg, nil, requests)
	release := make(chan struct{})
	decision := make(chan bool, 1)
	hooks := chatHooks{Approvals: requests, Run: func(ctx context.Context, _ app.Request, event func(runtime.Event) error) (app.Result, error) {
		if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: p.Request.TaskID}); err != nil {
			return app.Result{}, err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return app.Result{}, ctx.Err()
		}
		_, allowed, err := reviewer(ctx, p)
		decision <- allowed
		return app.Result{}, err
	}}
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine)
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
	lines <- chatLine{Text: "create file"}
	chatLiveWait(t, ctx, out, "[task "+p.Request.TaskID+"]")
	// The unbuffered EOF delivery guarantees the session consumes EOF before
	// it can select a subsequently emitted approval request.
	lines <- chatLine{Err: io.EOF}
	close(release)
	select {
	case allowed := <-decision:
		if allowed {
			t.Fatal("EOF authorized later proposal")
		}
	case <-ctx.Done():
		t.Fatal("EOF did not release reviewer")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("EOF session did not join")
	}
	if strings.Contains(out.String(), "[approval pending") {
		t.Fatal("proposal displayed after EOF")
	}
}

func TestChatApprovalSessionReviewExpiryKeepsSessionAlive(t *testing.T) {
	cfg, p := chatApprovalFixture(t, "new file")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	requests := make(chan chatApprovalRequest)
	reviewer := newChatReviewer(cfg, nil, requests)
	finish := make(chan struct{})
	decision := make(chan bool, 1)
	hooks := chatHooks{Approvals: requests, Run: func(ctx context.Context, _ app.Request, event func(runtime.Event) error) (app.Result, error) {
		if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: p.Request.TaskID}); err != nil {
			return app.Result{}, err
		}
		reviewCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		_, allowed, err := reviewer(reviewCtx, p)
		decision <- allowed
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return app.Result{}, err
	}}
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 2)
	lines <- chatLine{Text: "create file"}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
	chatLiveWait(t, ctx, out, "[approval pending")
	select {
	case allowed := <-decision:
		if allowed {
			t.Fatal("expired review allowed")
		}
	case <-ctx.Done():
		t.Fatal("review did not expire")
	}
	chatLiveWait(t, ctx, out, "Approval no longer pending.")
	lines <- chatLine{Text: "/approve " + p.Request.ID}
	chatLiveWait(t, ctx, out, "exact active request ID is required")
	lines <- chatLine{Text: "/status"}
	chatLiveWait(t, ctx, out, "Task active: "+p.Request.TaskID)
	select {
	case <-done:
		t.Fatal("review expiry killed session")
	default:
	}
	close(finish)
	close(lines)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("session did not join")
	}
}

func TestChatApprovalSessionStaleIDCannotDecideSecondProposal(t *testing.T) {
	cfg, first := chatApprovalFixture(t, "first file")
	_, second := chatApprovalFixture(t, "second file")
	second.Request.ID = "approval-second"
	second.Request.ToolCallID = "call-second"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	requests := make(chan chatApprovalRequest)
	reviewer := newChatReviewer(cfg, nil, requests)
	decision := make(chan bool, 2)
	hooks := chatHooks{Approvals: requests, Run: func(ctx context.Context, _ app.Request, event func(runtime.Event) error) (app.Result, error) {
		if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: first.Request.TaskID}); err != nil {
			return app.Result{}, err
		}
		for _, p := range []tools.ApprovalPrompt{first, second} {
			_, allowed, err := reviewer(ctx, p)
			decision <- allowed
			if err != nil {
				return app.Result{}, err
			}
		}
		return app.Result{TaskID: first.Request.TaskID}, nil
	}}
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 3)
	lines <- chatLine{Text: "create files"}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
	chatLiveWait(t, ctx, out, "[approval pending "+first.Request.ID+"]")
	lines <- chatLine{Text: "/approve " + first.Request.ID}
	select {
	case allowed := <-decision:
		if !allowed {
			t.Fatal("first approval lost")
		}
	case <-ctx.Done():
		t.Fatal("first decision missing")
	}
	chatLiveWait(t, ctx, out, "[approval pending "+second.Request.ID+"]")
	lines <- chatLine{Text: "/approve " + first.Request.ID}
	chatLiveWait(t, ctx, out, "exact active request ID is required")
	select {
	case <-decision:
		t.Fatal("stale approval decided new proposal")
	default:
	}
	lines <- chatLine{Text: "/deny " + second.Request.ID}
	select {
	case allowed := <-decision:
		if allowed {
			t.Fatal("second denial lost")
		}
	case <-ctx.Done():
		t.Fatal("second decision missing")
	}
	close(lines)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("session did not join")
	}
}
