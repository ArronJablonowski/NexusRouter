package webuiapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func TestBrowserDependencyRouteBindsSessionAndClosedQuery(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Dependencies: func(_ context.Context, subject, boardID, cardID string, options contract.DependencyOptions) (contract.DependencyPage, error) {
		calls.Add(1)
		if len(subject) != 64 || boardID != "board-a" || cardID != "card-a" || options.Limit != 1 || options.Direction != contract.DependencyDependents {
			t.Fatalf("unbound dependency request: %d %q %q %+v", len(subject), boardID, cardID, options)
		}
		return contract.DependencyPage{Version: 1, BoardID: boardID, CardID: cardID, Direction: options.Direction,
			GraphRevision: 1, GraphDigest: strings.Repeat("a", 64), Items: []contract.DependencyLink{
				{Version: 1, BoardID: boardID, CardID: "card-b", DependencyID: cardID},
			}}, nil
	}})
	path := "/app/api/v1/workboards/board-a/cards/card-a/dependencies?direction=dependents&limit=1"
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserWorkboardGET(path))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized dependency read dispatched", unauthorized.Code, calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, invalid := range []string{
		"/app/api/v1/workboards/board-a/cards/card-a/dependencies",
		"/app/api/v1/workboards/board-a/cards/card-a/dependencies?direction=dependents&unknown=x",
		"/app/api/v1/workboards/board-a/cards/card-a/dependencies?direction=dependents&direction=dependents",
		"/app/api/v1/workboards/board-a/cards/card-a/dependencies?direction=dependents&limit=01",
	} {
		request := browserWorkboardGET(invalid)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("invalid query dispatched: %s status=%d calls=%d", invalid, response.Code, calls.Load())
		}
	}
	request := browserWorkboardGET(path)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
	request = browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a/cards/card-a/dependencies", "")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || calls.Load() != 1 {
		t.Fatal("invalid method dispatched", response.Code, calls.Load())
	}
}
