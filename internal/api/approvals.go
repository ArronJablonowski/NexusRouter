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

	"github.com/ArronJablonowski/DarwinRouter/approvals"
)

func approvalRoute(path string) bool {
	return strings.HasPrefix(path, "/v1/tasks/") && strings.Contains(strings.TrimPrefix(path, "/v1/tasks/"), "/approvals")
}

func approvalListQuery(task, raw string) (approvals.ListOptions, error) {
	opts := approvals.ListOptions{TaskID: task, Limit: 25}
	if len(raw) > 1024 {
		return opts, approvals.ErrInvalid
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		return opts, approvals.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 {
			return opts, approvals.ErrInvalid
		}
		switch key {
		case "after":
			if values[0] == "" {
				return opts, approvals.ErrInvalid
			}
			opts.AfterCallID = values[0]
		case "limit":
			opts.Limit, err = strconv.Atoi(values[0])
			if err != nil || strconv.Itoa(opts.Limit) != values[0] {
				return opts, approvals.ErrInvalid
			}
		default:
			return opts, approvals.ErrInvalid
		}
	}
	return opts, opts.Validate()
}

func (h *Handler) serveApprovals(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
	if r.Method != http.MethodGet || (len(parts) != 2 && len(parts) != 3) || !replayTaskID(parts[0]) || parts[1] != "approvals" || (len(parts) == 3 && !replayTaskID(parts[2])) {
		failure(w, 404, "not_found")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 || (len(parts) == 3 && (r.URL.RawQuery != "" || r.URL.ForceQuery)) {
		failure(w, 400, "invalid_approval_request")
		return
	}
	opts, err := approvalListQuery(parts[0], r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid_approval_query")
		return
	}
	if (len(parts) == 2 && h.services.Approvals == nil) || (len(parts) == 3 && h.services.Approval == nil) {
		failure(w, 503, "approvals_unavailable")
		return
	}
	select {
	case h.approvalSlots <- struct{}{}:
		defer func() { <-h.approvalSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "approval_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	if len(parts) == 3 {
		record, err := h.services.Approval(ctx, parts[0], parts[2])
		if err != nil {
			approvalFailure(w, err)
			return
		}
		if ctx.Err() != nil {
			failure(w, 503, "approvals_unavailable")
			return
		}
		if record.Validate() != nil || record.Request.TaskID != parts[0] || record.Request.ID != parts[2] {
			failure(w, 500, "invalid_approval_record")
			return
		}
		writeJSON(w, 200, record)
		return
	}
	page, err := h.services.Approvals(ctx, opts)
	if err != nil {
		approvalFailure(w, err)
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	if page.Validate() != nil || page.Query != opts {
		failure(w, 500, "invalid_approval_page")
		return
	}
	if page.Records == nil {
		page.Records = []approvals.Record{}
	}
	writeJSON(w, 200, page)
}

func approvalFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		failure(w, 404, "approval_not_found")
		return
	}
	failure(w, 503, "approvals_unavailable")
}
