package webuiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

type browserRemoteProbe struct {
	calls int
	fail  bool
	after string
}

func (p *browserRemoteProbe) Info(ctx context.Context, id string) (remote.Info, error) {
	p.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second {
		return remote.Info{}, errors.New("unbounded")
	}
	if p.fail {
		return remote.Info{}, errors.New("private failure details")
	}
	return remote.Info{Version: 1, Instance: id, Available: true}, nil
}
func (p *browserRemoteProbe) Tasks(ctx context.Context, id, after string) (remote.TaskPage, error) {
	p.calls++
	p.after = after
	return remote.TaskPage{Version: 1, Instance: id, After: after, Next: after, Tasks: []remote.TaskSummary{}}, nil
}
func TestRemoteInspectionRequiresExplicitBrowserAuthority(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	probe := &browserRemoteProbe{}
	h.remoteInspector = probe
	body := `{"version":1,"instance":"node-a","view":"info"}`
	call := func(r *http.Request, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
		return w
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-inspection", body), 401)
	r := authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", body)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	r = authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", body)
	r.Header.Set("Origin", "https://evil.example")
	call(r, 403)
	for _, bad := range []string{`{"version":1,"instance":"node-a","view":"dispatch"}`, `{"version":1,"instance":"node-a","view":"info","after":"cursor"}`, `{"version":1,"instance":"node-a","view":"info","endpoint":"https://evil.example"}`} {
		call(authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", bad), 400)
	}
	if probe.calls != 0 {
		t.Fatal("unauthorized probe")
	}
	w := call(authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", body), 200)
	var page remoteInspectionPage
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Info == nil || page.Info.Instance != "node-a" || page.ObservedAt.IsZero() || probe.calls != 1 {
		t.Fatal(w.Body.String(), probe.calls)
	}
	call(authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", `{"version":1,"instance":"node-a","view":"tasks","after":"request-cursor-0001"}`), 200)
	if probe.after != "request-cursor-0001" || probe.calls != 2 {
		t.Fatal(probe)
	}
	probe.fail = true
	w = call(authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", body), 503)
	if strings.Contains(w.Body.String(), "private failure details") {
		t.Fatal("error leaked")
	}
	h.remoteInspector = nil
	call(authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", body), 503)
	if probe.calls != 3 {
		t.Fatal("disabled probe")
	}
}
