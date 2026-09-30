package toolgate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func previewAuthorization(a tools.Authorization) tools.Authorization {
	a.Arguments = json.RawMessage(`{"content":"private-argument-content"}`)
	digest := sha256.Sum256(a.Arguments)
	a.ArgumentsDigest = hex.EncodeToString(digest[:])
	a.Description = "Write the approved artifact"
	return a
}

func TestPreviewIsBoundToDigestAndMutationIsolated(t *testing.T) {
	g, a := fixture(t)
	a = previewAuthorization(a)
	original := append([]byte(nil), a.Arguments...)
	g.Review = nil
	var request approvals.Request
	g.ReviewPrompt = func(_ context.Context, p tools.ApprovalPrompt) (string, bool, error) {
		request = p.Request
		if !bytes.Equal(p.Arguments, original) || p.Description != a.Description || p.Request.ArgumentsDigest != a.ArgumentsDigest {
			t.Fatal("review did not receive exact proposal")
		}
		for i := range p.Arguments {
			p.Arguments[i] = 'x'
		}
		p.Request.Scope = "attacker-scope"
		return "operator", true, nil
	}
	called := false
	out, err := g.ExecuteApproved(context.Background(), a, func(context.Context) (runtime.ToolResult, error) {
		called = true
		if !bytes.Equal(a.Arguments, original) {
			t.Fatal("review mutated caller arguments")
		}
		return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
	})
	if err != nil || !called || out.Effect != runtime.ConfirmedEffect {
		t.Fatal(out, err, called)
	}
	r, err := g.Store.ReadApproval(context.Background(), request.ID)
	if err != nil || r.State != approvals.Consumed || r.Request.Scope != a.Scope {
		t.Fatal(r, err)
	}
	body, err := json.Marshal(r)
	if err != nil || bytes.Contains(body, []byte("private-argument-content")) || bytes.Contains(body, []byte(a.Description)) {
		t.Fatal("raw preview entered approval ledger", err)
	}
}

func TestInvalidPreviewRejectedBeforeLedgerInsert(t *testing.T) {
	for _, which := range []string{"nil", "oversize", "utf8", "json", "digest", "description_size", "description_utf8", "ambiguous"} {
		t.Run(which, func(t *testing.T) {
			g, a := fixture(t)
			a = previewAuthorization(a)
			valid := a
			g.Review = nil
			var reviews int
			g.ReviewPrompt = func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
				reviews++
				return "operator", true, nil
			}
			switch which {
			case "nil":
				a.Arguments = nil
			case "oversize":
				a.Arguments = []byte(strings.Repeat(" ", (1<<20)+1))
			case "utf8":
				a.Arguments = []byte{255}
			case "json":
				a.Arguments = []byte(`{"broken":`)
			case "digest":
				a.ArgumentsDigest = strings.Repeat("f", 64)
			case "description_size":
				a.Description = strings.Repeat("s", 4097)
			case "description_utf8":
				a.Description = string([]byte{255})
			case "ambiguous":
				g.Review = func(context.Context, approvals.Request) (string, bool, error) {
					t.Fatal("legacy review invoked")
					return "", false, nil
				}
			}
			called := false
			handler := func(context.Context) (runtime.ToolResult, error) {
				called = true
				return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
			}
			out, err := g.ExecuteApproved(context.Background(), a, handler)
			if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect || called || reviews != 0 {
				t.Fatal(out, err, called, reviews)
			}
			// A fresh valid request for this same durable call must still work:
			// invalid preview processing must not reserve the unique ledger slot.
			g.Review = nil
			if _, err = g.ExecuteApproved(context.Background(), valid, handler); err != nil || !called || reviews != 1 {
				t.Fatal(err, called, reviews)
			}
		})
	}
}

func TestPreviewCallbackPanicIsSanitized(t *testing.T) {
	g, a := fixture(t)
	a = previewAuthorization(a)
	g.Review = nil
	g.ReviewPrompt = func(context.Context, tools.ApprovalPrompt) (string, bool, error) { panic("private secret") }
	out, err := g.ExecuteApproved(context.Background(), a, func(context.Context) (runtime.ToolResult, error) {
		t.Fatal("handler invoked")
		return runtime.ToolResult{}, nil
	})
	if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect {
		t.Fatal(out, err)
	}
}
