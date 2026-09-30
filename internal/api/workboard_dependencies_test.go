package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestNativeDependencyRouteBindsBearerAndClosedQuery(t *testing.T) {
	var calls atomic.Int32
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardDependencies = func(_ context.Context, boardID, cardID string, options contract.DependencyOptions) (contract.DependencyPage, error) {
			calls.Add(1)
			if boardID != "board-a" || cardID != "card-a" || options.Limit != 1 || options.Direction != contract.DependencyPrerequisites {
				t.Fatalf("unbound dependency request: %q %q %+v", boardID, cardID, options)
			}
			return contract.DependencyPage{Version: 1, BoardID: boardID, CardID: cardID, Direction: options.Direction,
				GraphRevision: 1, GraphDigest: strings.Repeat("a", 64), Items: []contract.DependencyLink{
					{Version: 1, BoardID: boardID, CardID: cardID, DependencyID: "card-b"},
				}}, nil
		}
	})
	path := "/v1/workboards/board-a/cards/card-a/dependencies?direction=prerequisites&limit=1"
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized dependency read dispatched", unauthorized.Code, calls.Load())
	}
	for _, invalid := range []string{
		"/v1/workboards/board-a/cards/card-a/dependencies",
		"/v1/workboards/board-a/cards/card-a/dependencies?direction=prerequisites&unknown=x",
		"/v1/workboards/board-a/cards/card-a/dependencies?direction=prerequisites&direction=prerequisites",
		"/v1/workboards/board-a/cards/card-a/dependencies?direction=prerequisites&limit=01",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, invalid, ""))
		if response.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("invalid query dispatched: %s status=%d calls=%d", invalid, response.Code, calls.Load())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, path, ""))
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodPost, "/v1/workboards/board-a/cards/card-a/dependencies", ""))
	if response.Code != http.StatusMethodNotAllowed || calls.Load() != 1 {
		t.Fatal("invalid method dispatched", response.Code, calls.Load())
	}
}
