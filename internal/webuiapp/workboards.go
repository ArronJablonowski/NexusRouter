package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type WorkboardServices struct {
	List           func(context.Context, string, contract.BoardListOptions) (contract.Page, error)
	Read           func(context.Context, string, string, contract.BoardSnapshotOptions) (contract.BoardSnapshot, error)
	Events         func(context.Context, string, string, contract.BoardEventOptions) (contract.BoardEventPage, error)
	AttemptHistory func(context.Context, string, string, string, contract.AttemptHistoryOptions) (contract.AttemptHistoryPage, error)
	AttemptDetail  func(context.Context, string, string, string, string, contract.AttemptDetailOptions) (contract.AttemptDetailPage, error)
	Dependencies   func(context.Context, string, string, string, contract.DependencyOptions) (contract.DependencyPage, error)
	Mutate         func(context.Context, string, contract.BoardRequest) (contract.OperationReceipt, error)
}

func (h *Handler) serveWorkboardAPI(writer http.ResponseWriter, request *http.Request) bool {
	base := h.basePath + "/api/v1/workboards"
	path := request.URL.Path
	switch {
	case path == base && request.Method == http.MethodGet:
		h.serveWorkboardList(writer, request)
		return true
	case path == base:
		h.serveWorkboardCollectionMethod(writer, request)
		return true
	case func() bool { _, _, ok := browserWorkboardDependencyRoute(h.basePath, path); return ok }():
		boardID, cardID, _ := browserWorkboardDependencyRoute(h.basePath, path)
		h.serveWorkboardDependencies(writer, request, boardID, cardID)
		return true
	case func() bool { _, _, _, ok := browserWorkboardLifecycleRoute(h.basePath, path); return ok }():
		boardID, cardID, attemptID, _ := browserWorkboardLifecycleRoute(h.basePath, path)
		if attemptID == "" {
			h.serveWorkboardAttemptHistory(writer, request, boardID, cardID)
		} else {
			h.serveWorkboardAttemptDetail(writer, request, boardID, cardID, attemptID)
		}
		return true
	case browserWorkboardReadID(h.basePath, path) != "":
		h.serveWorkboardRead(writer, request, browserWorkboardReadID(h.basePath, path))
		return true
	default:
		return false
	}
}

func (h *Handler) serveWorkboardAttemptHistory(writer http.ResponseWriter, request *http.Request, boardID, cardID string) {
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
	options, err := parseBrowserAttemptHistoryQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.workboards.AttemptHistory == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_history_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	page, err := safeCall(func() (contract.AttemptHistoryPage, error) {
		return h.workboards.AttemptHistory(ctx, subject, boardID, cardID, options)
	})
	if err != nil || page.Validate() != nil || page.BoardID != boardID || page.CardID != cardID || len(page.Items) > options.Limit {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, page)
}

func (h *Handler) serveWorkboardAttemptDetail(writer http.ResponseWriter, request *http.Request, boardID, cardID, attemptID string) {
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
	options, err := parseBrowserAttemptDetailQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.workboards.AttemptDetail == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_attempt_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	page, err := safeCall(func() (contract.AttemptDetailPage, error) {
		return h.workboards.AttemptDetail(ctx, subject, boardID, cardID, attemptID, options)
	})
	if err != nil || page.Validate() != nil || page.Attempt.BoardID != boardID || page.Attempt.CardID != cardID || page.Attempt.ID != attemptID || len(page.Checkpoints) > options.Limit {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, page)
}

func (h *Handler) serveWorkboardCollectionMethod(writer http.ResponseWriter, request *http.Request) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead && !h.authorizedMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return
	}
	writer.Header().Set("Allow", "GET, POST")
	h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
}

func (h *Handler) serveWorkboardList(writer http.ResponseWriter, request *http.Request) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseBrowserWorkboardListQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.workboards.List == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboards_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	page, err := safeCall(func() (contract.Page, error) { return h.workboards.List(ctx, subject, options) })
	if err != nil || page.Validate() != nil || len(page.Items) > options.Limit {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, page)
}

func (h *Handler) serveWorkboardRead(writer http.ResponseWriter, request *http.Request, boardID string) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodGet {
		if request.Method != http.MethodHead && !h.authorizedMutation(request) {
			h.writeError(writer, request, http.StatusForbidden, "request_denied")
			return
		}
		writer.Header().Set("Allow", http.MethodGet)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseBrowserWorkboardSnapshotQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.workboards.Read == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := safeCall(func() (contract.BoardSnapshot, error) { return h.workboards.Read(ctx, subject, boardID, options) })
	if err != nil || snapshot.Validate() != nil || snapshot.Board.ID != boardID || len(snapshot.Cards) > options.Limit {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, snapshot)
}

func (h *Handler) serveWorkboardMutation(writer http.ResponseWriter, request *http.Request, boardID string, required contract.BoardAction) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodPost {
		if request.Method != http.MethodGet && request.Method != http.MethodHead && !h.authorizedMutation(request) {
			h.writeError(writer, request, http.StatusForbidden, "request_denied")
			return
		}
		writer.Header().Set("Allow", http.MethodPost)
		h.writeError(writer, request, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.authorizedMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return
	}
	if !mutationSlot(h, false) {
		writer.Header().Set("Retry-After", "1")
		h.writeError(writer, request, http.StatusServiceUnavailable, "mutation_capacity")
		return
	}
	defer releaseMutationSlot(h, false)
	var input contract.BoardRequest
	if decodeMutationJSON(request, &input, contract.MaxRequestBytes) != nil || input.Validate() != nil ||
		(required != "" && input.Action != required) || (boardID != "" && (input.Action == contract.BoardCreate || input.BoardID != boardID)) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_request")
		return
	}
	if h.workboards.Mutate == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_mutation_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	receipt, err := safeCall(func() (contract.OperationReceipt, error) { return h.workboards.Mutate(ctx, subject, input) })
	if err != nil || receipt.Validate() != nil || boardID != "" && receipt.BoardID != boardID {
		h.workboardFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, receipt)
}

func (h *Handler) workboardFailure(writer http.ResponseWriter, request *http.Request, err error) {
	code, status, retryable := "workboard_unavailable", http.StatusServiceUnavailable, true
	var violation *workboard.Violation
	switch {
	case errors.As(err, &violation) && violation.Code == workboard.CodeStaleRevision:
		code, status = "revision_conflict", http.StatusConflict
	case errors.As(err, &violation) && violation.Code == workboard.CodeMissingNode:
		code, status, retryable = "not_found", http.StatusNotFound, false
	case errors.As(err, &violation):
		code, status, retryable = "workboard_rejected", http.StatusUnprocessableEntity, false
	case errors.Is(err, context.DeadlineExceeded):
		code, status = "workboard_timeout", http.StatusGatewayTimeout
	}
	published := contract.Error{Version: 1, Code: code, Message: "The workboard operation could not be completed.", Retryable: retryable}
	if published.Validate() != nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_unavailable")
		return
	}
	h.writeJSON(writer, status, published)
}

func workboardBrowserQueryPath(base, path, method string) bool {
	if method != http.MethodGet {
		return false
	}
	_, _, _, lifecycle := browserWorkboardLifecycleRoute(base, path)
	_, _, dependencies := browserWorkboardDependencyRoute(base, path)
	return path == base+"/api/v1/workboards" || browserWorkboardReadID(base, path) != "" || lifecycle || dependencies
}

func browserWorkboardReadID(base, path string) string {
	return exactBrowserWorkboardMiddle(path, base+"/api/v1/workboards/", "")
}

func browserWorkboardOperationID(base, path string) string {
	return exactBrowserWorkboardMiddle(path, base+"/api/v1/workboards/", "/operations")
}

func boardEventsID(base, path string) string {
	return exactBrowserWorkboardMiddle(path, base+"/api/v1/workboards/", "/events")
}

func browserWorkboardLifecycleRoute(base, path string) (boardID, cardID, attemptID string, ok bool) {
	prefix := base + "/api/v1/workboards/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 4 && len(parts) != 5 || len(parts) >= 4 && (parts[1] != "cards" || parts[3] != "attempts") {
		return "", "", "", false
	}
	if !contract.ValidID(parts[0]) || !contract.ValidID(parts[2]) {
		return "", "", "", false
	}
	if len(parts) == 5 {
		if !contract.ValidID(parts[4]) {
			return "", "", "", false
		}
		attemptID = parts[4]
	}
	return parts[0], parts[2], attemptID, true
}

func exactBrowserWorkboardMiddle(path, prefix, suffix string) string {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") || !contract.ValidID(id) {
		return ""
	}
	return id
}

func parseBrowserWorkboardListQuery(raw string) (contract.BoardListOptions, error) {
	options := contract.BoardListOptions{Limit: 25}
	err := parseBrowserClosedQuery(raw, map[string]func(string) error{
		"after": func(v string) error { options.After = v; return nil },
		"limit": func(v string) error { n, err := browserPositiveInt(v); options.Limit = n; return err },
		"state": func(v string) error { options.State = v; return nil },
	})
	if err != nil || options.Validate() != nil {
		return options, contract.ErrContract
	}
	return options, nil
}

func parseBrowserAttemptHistoryQuery(raw string) (contract.AttemptHistoryOptions, error) {
	options := contract.AttemptHistoryOptions{Limit: 25}
	err := parseBrowserClosedQuery(raw, map[string]func(string) error{
		"after": func(v string) error { options.After = v; return nil },
		"limit": func(v string) error { n, err := browserPositiveInt(v); options.Limit = n; return err },
	})
	if err != nil || options.Validate() != nil {
		return options, contract.ErrContract
	}
	return options, nil
}

func parseBrowserAttemptDetailQuery(raw string) (contract.AttemptDetailOptions, error) {
	options := contract.AttemptDetailOptions{Limit: 25}
	err := parseBrowserClosedQuery(raw, map[string]func(string) error{
		"after": func(v string) error { options.After = v; return nil },
		"limit": func(v string) error { n, err := browserPositiveInt(v); options.Limit = n; return err },
	})
	if err != nil || options.Validate() != nil {
		return options, contract.ErrContract
	}
	return options, nil
}

func parseBrowserWorkboardSnapshotQuery(raw string) (contract.BoardSnapshotOptions, error) {
	options := contract.BoardSnapshotOptions{Limit: 100}
	err := parseBrowserClosedQuery(raw, map[string]func(string) error{
		"after":       func(v string) error { options.After = v; return nil },
		"limit":       func(v string) error { n, err := browserPositiveInt(v); options.Limit = n; return err },
		"state":       func(v string) error { options.State = v; return nil },
		"assignee_id": func(v string) error { options.AssigneeID = v; return nil },
		"owner_id":    func(v string) error { options.OwnerID = v; return nil },
		"claim_state": func(v string) error { options.ClaimState = v; return nil },
	})
	if err != nil || options.Validate() != nil {
		return options, contract.ErrContract
	}
	return options, nil
}

func parseBrowserClosedQuery(raw string, setters map[string]func(string) error) error {
	if len(raw) > 4096 {
		return contract.ErrContract
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return contract.ErrContract
	}
	for key, values := range values {
		setter, ok := setters[key]
		if !ok || len(values) != 1 || setter(values[0]) != nil {
			return contract.ErrContract
		}
	}
	return nil
}

func browserPositiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || strconv.Itoa(n) != value {
		return 0, contract.ErrContract
	}
	return n, nil
}
