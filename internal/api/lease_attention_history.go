package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func attentionHistoryRoute(path string) bool {
	return strings.HasPrefix(path, "/v1/resources/attention/") && strings.HasSuffix(path, "/history")
}

func attentionHistoryQuery(raw string) (workers.LeaseAttentionHistoryOptions, error) {
	options := workers.LeaseAttentionHistoryOptions{Limit: 25}
	bad := func() (workers.LeaseAttentionHistoryOptions, error) {
		return workers.LeaseAttentionHistoryOptions{}, workers.ErrLeaseAttention
	}
	if len(raw) > 2048 {
		return bad()
	}
	if raw != "" {
		for _, part := range strings.Split(raw, "&") {
			if part == "" {
				return bad()
			}
		}
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return bad()
	}
	for key, list := range values {
		if len(list) != 1 || list[0] == "" {
			return bad()
		}
		switch key {
		case "after_sequence":
			v, err := strconv.ParseInt(list[0], 10, 64)
			if err != nil || strconv.FormatInt(v, 10) != list[0] {
				return bad()
			}
			options.AfterSequence = v
		case "limit":
			v, err := strconv.Atoi(list[0])
			if err != nil || strconv.Itoa(v) != list[0] {
				return bad()
			}
			options.Limit = v
		default:
			return bad()
		}
	}
	if options.Validate() != nil {
		return bad()
	}
	return options, nil
}

func (h *Handler) serveLeaseAttentionHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, 405, "method_not_allowed")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/resources/attention/"), "/history")
	options, err := attentionHistoryQuery(r.URL.RawQuery)
	if !sessions.ValidEventPageID(id) || strings.Contains(id, "/") || err != nil || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Body != nil && r.Body != http.NoBody {
		failure(w, 400, "invalid_attention_history_request")
		return
	}
	if h.services.LeaseAttentionHistory == nil {
		failure(w, 503, "lease_attention_history_unavailable")
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
		failure(w, 503, "lease_attention_history_unavailable")
		return
	}
	page, err := h.callLeaseAttentionHistory(ctx, id, options)
	if ctx.Err() != nil {
		failure(w, 503, "lease_attention_history_unavailable")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		failure(w, 404, "attention_not_found")
		return
	}
	if err != nil {
		failure(w, 503, "lease_attention_history_unavailable")
		return
	}
	if page.Validate() != nil || page.AttentionID != id || len(page.Items) > options.Limit || page.HasMore && len(page.Items) != options.Limit {
		failure(w, 500, "invalid_attention_history")
		return
	}
	for i, item := range page.Items {
		if item.Sequence != options.AfterSequence+int64(i)+1 {
			failure(w, 500, "invalid_attention_history")
			return
		}
	}
	writeJSON(w, 200, page)
}

func (h *Handler) callLeaseAttentionHistory(ctx context.Context, id string, options workers.LeaseAttentionHistoryOptions) (page workers.LeaseAttentionHistoryPage, err error) {
	defer func() {
		if recover() != nil {
			err = workers.ErrLeaseAttention
		}
	}()
	return h.services.LeaseAttentionHistory(ctx, id, options)
}
