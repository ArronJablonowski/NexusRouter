package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const browserApprovalPrefix = "/v1/browser-session/challenges/"

func browserChallengeApprovalID(path string) string {
	if !strings.HasPrefix(path, browserApprovalPrefix) || !strings.HasSuffix(path, "/approve") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, browserApprovalPrefix), "/approve")
	request := contract.BrowserSessionRequest{Version: 1, ChallengeID: id}
	if strings.Contains(id, "/") || request.Validate() != nil {
		return ""
	}
	return id
}

func (h *Handler) serveBrowserChallengeApproval(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.TransferEncoding) != 0 || r.ContentLength < 0 || h.services.ApproveBrowserChallenge == nil {
		failure(w, http.StatusBadRequest, "invalid_browser_approval")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, http.StatusUnsupportedMediaType, "json_required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil || contract.RejectDuplicateJSONFields(body) != nil {
		failure(w, http.StatusBadRequest, "invalid_browser_approval")
		return
	}
	var input contract.BrowserChallengeApprovalRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Validate() != nil {
		failure(w, http.StatusBadRequest, "invalid_browser_approval")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.services.ApproveBrowserChallenge(ctx, id, input.DisplayCode); err != nil {
		if errors.Is(err, browserauth.ErrExhausted) {
			w.Header().Set("Retry-After", "60")
			failure(w, http.StatusTooManyRequests, "browser_approval_rate_limited")
			return
		}
		if errors.Is(err, browserauth.ErrInvalid) || errors.Is(err, browserauth.ErrExpired) {
			failure(w, http.StatusNotFound, "browser_challenge_unavailable")
		} else {
			failure(w, http.StatusServiceUnavailable, "browser_approval_unavailable")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
