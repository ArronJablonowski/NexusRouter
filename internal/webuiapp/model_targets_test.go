package webuiapp

import (
	"context"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"net/http/httptest"
	"testing"
)

func TestModelTargetsRequireBrowserAuthority(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	calls := 0
	h.inspections.ModelTargets = func(context.Context) (contract.ModelTargets, error) {
		calls++
		return contract.ModelTargets{Version: 1, Hostname: "qa-host", Models: []contract.ModelTarget{{ID: "coder", Model: "Qwen Coder", Local: true}}}, nil
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, browserGET("/app/api/v1/model-targets", nil))
	if r.Code != 401 || calls != 0 {
		t.Fatal(r.Code, calls)
	}
	cookie, _ := authenticateBrowser(t, h)
	r = httptest.NewRecorder()
	h.ServeHTTP(r, browserGET("/app/api/v1/model-targets", cookie))
	if r.Code != 200 || calls != 1 {
		t.Fatal(r.Code, calls)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, browserGET("/app/api/v1/model-targets?unknown=x", cookie))
	if r.Code == 200 || calls != 1 {
		t.Fatal("unscoped query accepted", r.Code, calls)
	}
}
