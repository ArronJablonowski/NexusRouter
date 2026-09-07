package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func parseTaskListQuery(raw string) (sessions.TaskListOptions, error) {
	options := sessions.TaskListOptions{Limit: 25}
	if len(raw) > 4096 {
		return options, sessions.ErrTaskList
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return options, sessions.ErrTaskList
	}
	for key, items := range values {
		if len(items) != 1 {
			return options, sessions.ErrTaskList
		}
		switch key {
		case "state":
			options.State = items[0]
		case "after":
			if len(items[0]) > 1024 {
				return options, sessions.ErrTaskList
			}
			options.After = items[0]
		case "limit":
			options.Limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(options.Limit) != items[0] {
				return options, sessions.ErrTaskList
			}
		default:
			return options, sessions.ErrTaskList
		}
	}
	if options.Validate() != nil {
		return options, sessions.ErrTaskList
	}
	return options, nil
}

func (h *Handler) serveTaskList(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.ForceQuery {
		failure(w, http.StatusBadRequest, "invalid_task_query")
		return
	}
	options, err := parseTaskListQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_task_query")
		return
	}
	if h.services.Tasks == nil {
		failure(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	select {
	case h.controls <- struct{}{}:
		defer func() { <-h.controls }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "control_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	page, err := h.callTaskList(ctx, options)
	if ctx.Err() != nil {
		failure(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	if err != nil {
		failure(w, http.StatusInternalServerError, "tasks_unavailable")
		return
	}
	if !validTaskPage(page, options) {
		failure(w, http.StatusInternalServerError, "invalid_task_page")
		return
	}
	if page.Items == nil {
		page.Items = []sessions.TaskSummary{}
	}
	writeJSON(w, http.StatusOK, page)
}

func validTaskPage(page sessions.TaskPage, options sessions.TaskListOptions) bool {
	if page.Validate() != nil || len(page.Items) > options.Limit {
		return false
	}
	for _, item := range page.Items {
		if options.State != "" && item.State != options.State {
			return false
		}
	}
	if !page.HasMore {
		return true
	}
	next, err := sessions.DecodeTaskListCursor(page.NextCursor)
	if err != nil || next.State != options.State || next.Last > next.HighWater {
		return false
	}
	if options.After == "" {
		return true
	}
	previous, _ := sessions.DecodeTaskListCursor(options.After)
	return next.HighWater == previous.HighWater && next.Last < previous.Last
}

func (h *Handler) callTaskList(ctx context.Context, options sessions.TaskListOptions) (page sessions.TaskPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("tasks unavailable")
		}
	}()
	return h.services.Tasks(ctx, options)
}
