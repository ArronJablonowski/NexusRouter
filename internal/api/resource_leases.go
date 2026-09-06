package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func leaseScopeQuery(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 {
		return "", workers.ErrScopeLeaseStatus
	}
	query, err := url.ParseQuery(raw)
	if err != nil || len(query) != 1 || len(query["scope"]) != 1 || !workers.ValidLeaseScope(query["scope"][0]) {
		return "", workers.ErrScopeLeaseStatus
	}
	return query["scope"][0], nil
}

// Resource-scope observations identify task holders, not lease capabilities.
// Expiry, an empty list, or a successful query never authorizes resource use.
func (h *Handler) serveResourceLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	scope, err := leaseScopeQuery(r.URL.RawQuery)
	if err != nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) {
		failure(w, 400, "invalid_scope_request")
		return
	}
	if h.services.ScopeLeases == nil {
		failure(w, 503, "scope_leases_unavailable")
		return
	}
	select {
	case h.controls <- struct{}{}:
		defer func() { <-h.controls }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "control_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "scope_leases_unavailable")
		return
	}
	status, err := h.callScopeLeases(ctx, scope)
	if err != nil || ctx.Err() != nil {
		failure(w, 503, "scope_leases_unavailable")
		return
	}
	if status.Scope != scope || status.Validate() != nil {
		failure(w, 500, "invalid_scope_leases")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) callScopeLeases(ctx context.Context, scope string) (status workers.ScopeLeaseStatus, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("scope leases unavailable")
		}
	}()
	return h.services.ScopeLeases(ctx, scope)
}
