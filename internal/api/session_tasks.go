package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func sessionTasksRoute(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/sessions/"), "/")
	return strings.HasPrefix(path, "/v1/sessions/") && len(parts) == 2 && parts[0] != "" && parts[1] == "tasks"
}

func parseSessionTaskListQuery(raw string) (sessions.SessionTaskListOptions, error) {
	options := sessions.SessionTaskListOptions{Limit: 25}
	if len(raw) > 4096 {
		return options, sessions.ErrSessionTasks
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return options, sessions.ErrSessionTasks
	}
	for key, items := range values {
		if len(items) != 1 {
			return options, sessions.ErrSessionTasks
		}
		switch key {
		case "after":
			if len(items[0]) > 1024 {
				return options, sessions.ErrSessionTasks
			}
			options.After = items[0]
		case "limit":
			options.Limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(options.Limit) != items[0] {
				return options, sessions.ErrSessionTasks
			}
		default:
			return options, sessions.ErrSessionTasks
		}
	}
	return options, nil
}

func (h *Handler) serveSessionTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.ForceQuery {
		failure(w, http.StatusBadRequest, "invalid_session_task_query")
		return
	}
	session := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/tasks")
	options, err := parseSessionTaskListQuery(r.URL.RawQuery)
	if err != nil || options.Validate(session) != nil {
		failure(w, http.StatusBadRequest, "invalid_session_task_query")
		return
	}
	if h.services.SessionTasks == nil {
		failure(w, http.StatusServiceUnavailable, "session_tasks_unavailable")
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
	page, err := h.callSessionTasks(ctx, session, options)
	if ctx.Err() != nil {
		failure(w, http.StatusServiceUnavailable, "session_tasks_unavailable")
		return
	}
	if err != nil {
		failure(w, http.StatusInternalServerError, "session_tasks_unavailable")
		return
	}
	if !validSessionTaskPage(page, session, options) {
		failure(w, http.StatusInternalServerError, "invalid_session_task_page")
		return
	}
	if page.Items == nil {
		page.Items = []sessions.SessionTask{}
	}
	writeJSON(w, http.StatusOK, page)
}

func validSessionTaskPage(page sessions.SessionTaskPage, session string, options sessions.SessionTaskListOptions) bool {
	if page.Validate() != nil || page.SessionID != session || len(page.Items) > options.Limit {
		return false
	}
	if !page.HasMore {
		return true
	}
	next, err := sessions.DecodeSessionTaskCursor(page.NextCursor)
	if err != nil || next.SessionID != session || next.Last > next.HighWater {
		return false
	}
	if options.After == "" {
		return true
	}
	previous, err := sessions.DecodeSessionTaskCursor(options.After)
	return err == nil && next.HighWater == previous.HighWater && next.Last < previous.Last
}

func (h *Handler) callSessionTasks(ctx context.Context, session string, options sessions.SessionTaskListOptions) (page sessions.SessionTaskPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("session tasks unavailable")
		}
	}()
	return h.services.SessionTasks(ctx, session, options)
}
