package api

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func parseSubmissionListQuery(raw string) (submissions.ListOptions, error) {
	opts := submissions.ListOptions{Limit: 25}
	if len(raw) > 4096 {
		return opts, submissions.ErrInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return opts, submissions.ErrInvalid
	}
	for key, items := range values {
		if len(items) != 1 {
			return opts, submissions.ErrInvalid
		}
		value := items[0]
		switch key {
		case "state":
			opts.State = value
		case "after":
			if len(value) > 1024 {
				return opts, submissions.ErrInvalid
			}
			opts.After = value
		case "limit":
			opts.Limit, err = strconv.Atoi(value)
			if err != nil || strconv.Itoa(opts.Limit) != value {
				return opts, submissions.ErrInvalid
			}
		default:
			return opts, submissions.ErrInvalid
		}
	}
	if opts.Validate() != nil {
		return opts, submissions.ErrInvalid
	}
	return opts, nil
}

func (h *Handler) listSubmissions(w http.ResponseWriter, r *http.Request) {
	opts, err := parseSubmissionListQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid_submission_query")
		return
	}
	if h.services.Submissions == nil {
		failure(w, 503, "submissions_unavailable")
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
	if r.Context().Err() != nil {
		return
	}
	page, err := h.services.Submissions(r.Context(), opts)
	if err != nil {
		submissionFailure(w, err)
		return
	}
	valid := page.Validate() == nil && len(page.Items) <= opts.Limit
	for _, item := range page.Items {
		if opts.State != "" && item.State != opts.State {
			valid = false
		}
	}
	if page.HasMore {
		next := submissions.ListOptions{State: opts.State, After: page.NextCursor, Limit: opts.Limit}
		cursor, cursorErr := submissions.DecodeListCursor(page.NextCursor)
		valid = valid && next.Validate() == nil && cursorErr == nil && cursor.Last > 0 && cursor.Last < cursor.HighWater
		if opts.After != "" {
			previous, _ := submissions.DecodeListCursor(opts.After) // Already validated at admission.
			valid = valid && cursor.HighWater == previous.HighWater && cursor.Last > previous.Last
		}
	}
	if !valid {
		failure(w, 500, "invalid_submission_page")
		return
	}
	if page.Items == nil {
		page.Items = []submissions.Summary{}
	}
	writeJSON(w, 200, page)
}
