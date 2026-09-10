package api

import (
	"errors"
	"net/http"
	"strings"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func (h *Handler) serveWorkboardDependencies(w http.ResponseWriter, r *http.Request, boardID, cardID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !strictNativeGET(r) {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseWorkboardDependencyQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.services.WorkboardDependencies == nil {
		failure(w, http.StatusServiceUnavailable, "workboard_dependencies_unavailable")
		return
	}
	if !takeWorkboardSlot(h.workboardSlots) {
		capacityFailure(w)
		return
	}
	defer releaseWorkboardSlot(h.workboardSlots)
	page, err := safeWorkboardCall(func() (contract.DependencyPage, error) {
		return h.services.WorkboardDependencies(r.Context(), boardID, cardID, options)
	})
	if err != nil || page.Validate() != nil || page.BoardID != boardID || page.CardID != cardID || page.Direction != options.Direction || len(page.Items) > options.Limit {
		workboardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseWorkboardDependencyQuery(raw string) (contract.DependencyOptions, error) {
	result := contract.DependencyOptions{Limit: 25}
	err := parseClosedQuery(raw, map[string]func(string) error{
		"after":     func(value string) error { result.After = value; return nil },
		"limit":     func(value string) error { n, err := canonicalPositiveInt(value); result.Limit = n; return err },
		"direction": func(value string) error { result.Direction = contract.DependencyDirection(value); return nil },
	})
	if err != nil || result.Validate() != nil {
		return result, errors.New("invalid query")
	}
	return result, nil
}

func nativeWorkboardDependencyRoute(path string) (boardID, cardID string, ok bool) {
	if !strings.HasPrefix(path, "/v1/workboards/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/workboards/"), "/")
	if len(parts) != 4 || parts[1] != "cards" || parts[3] != "dependencies" ||
		!contract.ValidID(parts[0]) || !contract.ValidID(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
}
