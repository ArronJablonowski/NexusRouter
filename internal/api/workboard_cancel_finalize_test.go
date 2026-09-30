package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestNativeWorkboardCancelFinalizeIsProofFreeAndOperatorBound(t *testing.T) {
	var calls atomic.Int32
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardFinalizeCancel = func(_ context.Context, request app.WorkboardCancelFinalizationRequest) (contract.OperationReceipt, error) {
			calls.Add(1)
			if request.BoardID != "board-a" || request.CardID != "card-a" || request.AttemptID != "attempt-a" ||
				request.ClaimID != "claim-a" || request.IdempotencyKey != workboardKey ||
				request.ExpectedCardRevision != 7 || request.ExpectedClaimRevision != 3 {
				t.Fatal("transport was not exactly bound", request)
			}
			receipt := validNativeReceipt(request.BoardID)
			receipt.CardID = request.CardID
			cardRevision, claimRevision := int64(8), int64(4)
			receipt.CardRevision, receipt.ClaimRevision = &cardRevision, &claimRevision
			return receipt, nil
		}
	})
	path := "/v1/workboards/board-a/cards/card-a/attempts/attempt-a/cancel-finalize"
	valid := nativeWorkboardRequest(http.MethodPost, path, `{"version":1,"claim_id":"claim-a","expected_card_revision":7,"expected_claim_revision":3}`)
	valid.Header.Set("Idempotency-Key", workboardKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, valid)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}

	for _, body := range []string{
		`{"version":1,"claim_id":"claim-a","expected_card_revision":7,"expected_claim_revision":3,"stop_proof_id":"forged"}`,
		`{"version":1,"claim_id":"claim-a","expected_card_revision":0,"expected_claim_revision":3}`,
	} {
		request := nativeWorkboardRequest(http.MethodPost, path, body)
		request.Header.Set("Idempotency-Key", workboardKey)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || calls.Load() != 1 {
			t.Fatal("unsafe finalize payload dispatched", response.Code, response.Body.String(), calls.Load())
		}
	}
	origin := nativeWorkboardRequest(http.MethodPost, path, `{"version":1,"claim_id":"claim-a","expected_card_revision":7,"expected_claim_revision":3}`)
	origin.Header.Set("Idempotency-Key", workboardKey)
	origin.Header.Set("Origin", "https://example.test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, origin)
	if response.Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatal("browser-origin finalize dispatched", response.Code, calls.Load())
	}
}
