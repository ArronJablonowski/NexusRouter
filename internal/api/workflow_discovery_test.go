package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func workflowDiscoveryPage() skills.WorkflowCandidatePage {
	return skills.WorkflowCandidatePage{Version: 1, Domain: "creative", Scanned: 1, Candidates: []skills.WorkflowCandidate{{TaskID: "task", SessionID: "session", Domain: "creative", Privacy: "local_only", EvaluationID: "evaluation", EvaluationDigest: strings.Repeat("a", 64), SourceDigest: strings.Repeat("b", 64), SourceSequence: 4}}}
}

func TestWorkflowDiscoveryHTTPCallbackAndResponse(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancel", "domain", "invalid", "nil-candidates", "wrong-cursor"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := services()
			calls := 0
			s.DiscoverSkillWorkflows = func(ctx context.Context, domain, after string, limit int) (skills.WorkflowCandidatePage, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second {
					t.Error("unbounded callback")
				}
				if domain != "creative" || after != "before" || limit != 20 {
					t.Error("incorrect forwarding", domain, after, limit)
				}
				page := workflowDiscoveryPage()
				switch mode {
				case "error":
					return page, errors.New("private-error")
				case "panic":
					panic("private-error")
				case "cancel":
					cancel()
				case "domain":
					page.Domain = "other"
				case "invalid":
					page.Candidates[0].SourceDigest = "invalid"
				case "nil-candidates":
					page.Candidates = nil
				case "wrong-cursor":
					page.Scanned = 20
					page.Next = "aaa"
				}
				return page, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/skills/workflows?domain=creative&after=before", "").WithContext(ctx))
			want := 200
			switch mode {
			case "error", "panic", "cancel":
				want = 422
			case "domain", "invalid", "nil-candidates", "wrong-cursor":
				want = 500
			}
			if w.Code != want || calls != 1 || strings.Contains(w.Body.String(), "private-error") {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			if want != 200 && strings.Contains(w.Body.String(), "evaluation") {
				t.Fatal("failed response leaked metadata")
			}
			if len(h.slots) != 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("slot leak or cache enabled")
			}
		})
	}
}

func TestWorkflowDiscoveryHTTPQueryAndAdmission(t *testing.T) {
	for _, query := range []string{"", "domain=", "domain=creative&after=", "domain=creative&scan_limit=", "domain=creative&unknown=x", "domain=creative&domain=creative", "Domain=creative", "domain=creative&after=x&after=y", "domain=creative&scan_limit=1&scan_limit=2", "domain=creative&scan_limit=0", "domain=creative&scan_limit=21", "domain=creative&scan_limit=01", "domain=creative&scan_limit=%2B1", "domain=creative&scan_limit=1.0", "domain=creative&after=bad%20cursor", "domain=bad.domain", "domain=creative&after=%zz", "domain=creative&x=" + strings.Repeat("x", 1024), "domain=" + strings.Repeat("a", 65)} {
		t.Run(query, func(t *testing.T) {
			s := services()
			calls := 0
			s.DiscoverSkillWorkflows = func(context.Context, string, string, int) (skills.WorkflowCandidatePage, error) {
				calls++
				return workflowDiscoveryPage(), nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/skills/workflows?"+query, ""))
			if w.Code != 400 || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
	for _, mode := range []string{"auth", "origin", "capacity", "missing", "canceled", "method", "path"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.DiscoverSkillWorkflows = func(context.Context, string, string, int) (skills.WorkflowCandidatePage, error) {
				calls++
				return workflowDiscoveryPage(), nil
			}
			if mode == "missing" {
				s.DiscoverSkillWorkflows = nil
			}
			h, _ := New(token, 1, s)
			r := request("GET", "/v1/skills/workflows?domain=creative", "")
			want := 503
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "capacity":
				h.slots <- struct{}{}
			case "canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				r = r.WithContext(ctx)
				want = 422
			case "method":
				r.Method = "POST"
				r.URL.RawQuery = ""
				want = 404
			case "path":
				r.URL.Path += "/extra"
				r.URL.RawQuery = ""
				want = 404
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestWorkflowDiscoveryHTTPCanonicalCursorAndLimit(t *testing.T) {
	for _, limit := range []int{1, 20} {
		s := services()
		cursor := strings.Repeat("z", 128)
		calls := 0
		s.DiscoverSkillWorkflows = func(_ context.Context, domain, after string, n int) (skills.WorkflowCandidatePage, error) {
			calls++
			if after != cursor || n != limit {
				t.Error(after, n)
			}
			return skills.WorkflowCandidatePage{Version: 1, Domain: domain, Candidates: []skills.WorkflowCandidate{}}, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		q := url.Values{"domain": {"creative"}, "after": {cursor}, "scan_limit": {strconv.Itoa(limit)}}
		h.ServeHTTP(w, request("GET", "/v1/skills/workflows?"+q.Encode(), ""))
		if w.Code != 200 || calls != 1 {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
	}
}
