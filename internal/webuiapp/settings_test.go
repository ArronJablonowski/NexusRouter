package webuiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func TestSettingsMutationRequiresAuthorityAndDetectsConflict(t *testing.T) {
	digest := strings.Repeat("a", 64)
	var calls atomic.Int32
	handler := mutationHandlerFixture(t, MutationServices{UpdateSettings: func(_ context.Context, request contract.SettingsUpdateRequest) (contract.SettingsInspection, error) {
		calls.Add(1)
		if request.ExpectedDigest != digest {
			return contract.SettingsInspection{}, ErrSettingsConflict
		}
		return contract.SettingsInspection{Version: 1, Digest: strings.Repeat("b", 64), Active: contract.ToolAccessSettings{}, Saved: request.Settings, RestartRequired: true}, nil
	}})
	body := `{"version":1,"expected_digest":"` + digest + `","settings":{"tools_enabled":true,"delegate_read_tools":true,"read_root":"/workspace"}}`
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserRequest(http.MethodPost, "/app/api/v1/settings", body))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal(unauthorized.Code, calls.Load())
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedMutationRequest(t, handler, "/app/api/v1/settings", body))
	var settings contract.SettingsInspection
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &settings) != nil || settings.Validate() != nil || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
	stale := strings.Replace(body, digest, strings.Repeat("c", 64), 1)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedMutationRequest(t, handler, "/app/api/v1/settings", stale))
	if response.Code != http.StatusConflict || calls.Load() != 2 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
}

func TestSettingsMutationRejectsInvalidDependencies(t *testing.T) {
	var calls atomic.Int32
	handler := mutationHandlerFixture(t, MutationServices{UpdateSettings: func(context.Context, contract.SettingsUpdateRequest) (contract.SettingsInspection, error) {
		calls.Add(1)
		return contract.SettingsInspection{}, errors.New("unexpected")
	}})
	body := `{"version":1,"expected_digest":"` + strings.Repeat("a", 64) + `","settings":{"tools_enabled":false,"delegate_read_tools":true,"read_root":""}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedMutationRequest(t, handler, "/app/api/v1/settings", body))
	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
}
