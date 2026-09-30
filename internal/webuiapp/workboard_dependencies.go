package webuiapp

import (
	"context"
	"net/http"
	"strings"
	"time"

	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func (h *Handler) serveWorkboardDependencies(writer http.ResponseWriter, request *http.Request, boardID, cardID string) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseBrowserDependencyQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.workboards.Dependencies == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_dependencies_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	page, err := safeCall(func() (contract.DependencyPage, error) {
		return h.workboards.Dependencies(ctx, subject, boardID, cardID, options)
	})
	if err != nil || page.Validate() != nil || page.BoardID != boardID || page.CardID != cardID || page.Direction != options.Direction || len(page.Items) > options.Limit {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, page)
}

func parseBrowserDependencyQuery(raw string) (contract.DependencyOptions, error) {
	options := contract.DependencyOptions{Limit: 25}
	err := parseBrowserClosedQuery(raw, map[string]func(string) error{
		"after":     func(value string) error { options.After = value; return nil },
		"limit":     func(value string) error { n, err := browserPositiveInt(value); options.Limit = n; return err },
		"direction": func(value string) error { options.Direction = contract.DependencyDirection(value); return nil },
	})
	if err != nil || options.Validate() != nil {
		return options, contract.ErrContract
	}
	return options, nil
}

func browserWorkboardDependencyRoute(base, path string) (boardID, cardID string, ok bool) {
	prefix := base + "/api/v1/workboards/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 4 || parts[1] != "cards" || parts[3] != "dependencies" ||
		!contract.ValidID(parts[0]) || !contract.ValidID(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
}
