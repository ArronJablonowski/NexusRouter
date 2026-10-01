package webuiapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteRecoveryFragmentLoadsProductionShellWithoutDispatch(t *testing.T) {
	h := handlerFixture(t)
	cookie, _ := authenticateBrowser(t, h)
	d := &browserDispatchFake{}
	h.remoteDispatcher = d
	server := httptest.NewServer(h)
	defer server.Close()
	for _, tc := range []struct {
		suffix string
		want   int
	}{
		{"#remote_peer=node-a&remote_request=request-browser-0001", http.StatusOK},
		{"?remote_peer=node-a&remote_request=request-browser-0001", http.StatusBadRequest},
	} {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/app/settings"+tc.suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "127.0.0.1:7788"
		request.AddCookie(cookie)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.want {
			t.Fatalf("%s: %d want %d: %s", tc.suffix, response.StatusCode, tc.want, body)
		}
		if tc.want == http.StatusOK && !strings.Contains(string(body), "remote-dispatch.js") {
			t.Fatal("missing production shell")
		}
	}
	if d.calls != 0 {
		t.Fatal("opening recovery URL dispatched work")
	}
}
