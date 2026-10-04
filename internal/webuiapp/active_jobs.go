package webuiapp

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// serveActiveJobs filters before pagination so older running jobs are not hidden
// behind newer completed tasks. No prompts, results or execution authority leak.
func (h *Handler) serveActiveJobs(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(r) {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(r) || len(r.URL.RawQuery) > 4096 {
		h.writeError(w, r, 400, "invalid_job_query")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.writeError(w, r, 400, "invalid_job_query")
		return
	}
	if values.Get("kind") == "queued" || values.Get("kind") == "admitted" {
		h.serveQueuedJobs(w, r, values)
		return
	}
	options := sessions.TaskListOptions{State: "running", Limit: 100}
	for k, v := range values {
		if k != "after" || len(v) != 1 {
			err = sessions.ErrTaskList
			break
		}
		options.After = v[0]
	}
	if err != nil || options.Validate() != nil {
		h.writeError(w, r, 400, "invalid_job_query")
		return
	}
	if h.reads.Tasks == nil {
		h.writeError(w, r, 503, "jobs_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	read := h.reads.JobTasks
	if read == nil {
		read = h.reads.Tasks
	}
	page, err := safeActiveJobs(ctx, read, options)
	if err != nil || page.Validate() != nil || len(page.Items) > options.Limit {
		h.writeError(w, r, 503, "jobs_unavailable")
		return
	}
	for _, item := range page.Items {
		if item.State != "running" {
			h.writeError(w, r, 503, "jobs_unavailable")
			return
		}
	}
	h.writeJSON(w, 200, page)
}
func safeActiveJobs(ctx context.Context, read func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error), options sessions.TaskListOptions) (page sessions.TaskPage, err error) {
	defer func() {
		if recover() != nil {
			page = sessions.TaskPage{}
			err = sessions.ErrTaskList
		}
	}()
	return read(ctx, options)
}

func (h *Handler) serveQueuedJobs(w http.ResponseWriter, r *http.Request, values url.Values) {
	options := submissions.ListOptions{State: "queued", Limit: 100}
	if values.Get("kind") == "admitted" {
		options.State = "running"
	}
	for k, v := range values {
		if len(v) != 1 || (k != "kind" && k != "after") {
			h.writeError(w, r, 400, "invalid_job_query")
			return
		}
		if k == "after" {
			options.After = v[0]
		}
	}
	if options.Validate() != nil {
		h.writeError(w, r, 400, "invalid_job_query")
		return
	}
	if h.reads.Submissions == nil {
		h.writeError(w, r, 503, "jobs_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	page, err := safeQueuedJobs(ctx, h.reads.Submissions, options)
	if err != nil || page.Validate() != nil || len(page.Items) > 100 {
		h.writeError(w, r, 503, "jobs_unavailable")
		return
	}
	for _, item := range page.Items {
		if item.State != options.State {
			h.writeError(w, r, 503, "jobs_unavailable")
			return
		}
	}
	h.writeJSON(w, 200, page)
}
func safeQueuedJobs(ctx context.Context, read func(context.Context, submissions.ListOptions) (submissions.Page, error), options submissions.ListOptions) (page submissions.Page, err error) {
	defer func() {
		if recover() != nil {
			page = submissions.Page{}
			err = submissions.ErrInvalid
		}
	}()
	return read(ctx, options)
}
