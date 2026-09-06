package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func leaseAttentionQuery(raw string) (workers.LeaseAttentionOptions, error) {
	options := workers.LeaseAttentionOptions{State: "open", Limit: 25}
	bad := func() (workers.LeaseAttentionOptions, error) {
		return workers.LeaseAttentionOptions{}, workers.ErrLeaseAttention
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
		case "state":
			options.State = list[0]
		case "after":
			options.After = list[0]
		case "limit":
			limit, err := strconv.Atoi(list[0])
			if err != nil || strconv.Itoa(limit) != list[0] {
				return bad()
			}
			options.Limit = limit
		default:
			return bad()
		}
	}
	if options.Validate() != nil {
		return bad()
	}
	return options, nil
}

func (h *Handler) serveLeaseAttention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, 405, "method_not_allowed")
		return
	}
	options, err := leaseAttentionQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Body != nil && r.Body != http.NoBody {
		failure(w, 400, "invalid_attention_request")
		return
	}
	if h.services.LeaseAttention == nil {
		failure(w, 503, "lease_attention_unavailable")
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
		failure(w, 503, "lease_attention_unavailable")
		return
	}
	page, err := h.callLeaseAttention(ctx, options)
	if err != nil || ctx.Err() != nil {
		failure(w, 503, "lease_attention_unavailable")
		return
	}
	if page.Validate() != nil || len(page.Items) > options.Limit || page.HasMore && len(page.Items) != options.Limit {
		failure(w, 500, "invalid_lease_attention")
		return
	}
	for _, item := range page.Items {
		if item.ID <= options.After || options.State != "all" && item.State != options.State {
			failure(w, 500, "invalid_lease_attention")
			return
		}
	}
	writeJSON(w, 200, page)
}

func (h *Handler) callLeaseAttention(ctx context.Context, options workers.LeaseAttentionOptions) (page workers.LeaseAttentionPage, err error) {
	defer func() {
		if recover() != nil {
			err = workers.ErrLeaseAttention
		}
	}()
	return h.services.LeaseAttention(ctx, options)
}
