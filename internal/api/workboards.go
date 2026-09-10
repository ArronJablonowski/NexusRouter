package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const workboardBodyLimit = contract.MaxRequestBytes

func workboardQueryRoute(path, method string) bool {
	if method != http.MethodGet {
		return false
	}
	return path == "/v1/workboards" || nativeWorkboardReadID(path) != "" || nativeWorkboardEventID(path) != ""
}

func (h *Handler) serveWorkboards(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/workboards":
		if r.Method == http.MethodGet {
			h.serveWorkboardList(w, r)
			return
		}
		if r.Method == http.MethodPost {
			h.serveWorkboardMutation(w, r, "", contract.BoardCreate)
			return
		}
		w.Header().Set("Allow", "GET, POST")
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
	case nativeWorkboardEventID(r.URL.Path) != "":
		h.serveWorkboardEvents(w, r, nativeWorkboardEventID(r.URL.Path))
	case nativeWorkboardOperationID(r.URL.Path) != "":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		h.serveWorkboardMutation(w, r, nativeWorkboardOperationID(r.URL.Path), "")
	case nativeWorkboardReadID(r.URL.Path) != "":
		h.serveWorkboardRead(w, r, nativeWorkboardReadID(r.URL.Path))
	default:
		failure(w, http.StatusNotFound, "not_found")
	}
}

func (h *Handler) serveWorkboardList(w http.ResponseWriter, r *http.Request) {
	if !strictNativeGET(r) {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseWorkboardListQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.services.WorkboardList == nil {
		failure(w, http.StatusServiceUnavailable, "workboards_unavailable")
		return
	}
	if !takeWorkboardSlot(h.workboardSlots) {
		capacityFailure(w)
		return
	}
	defer releaseWorkboardSlot(h.workboardSlots)
	page, err := safeWorkboardCall(func() (contract.Page, error) { return h.services.WorkboardList(r.Context(), options) })
	if err != nil || page.Validate() != nil || len(page.Items) > options.Limit {
		workboardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) serveWorkboardRead(w http.ResponseWriter, r *http.Request, boardID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !strictNativeGET(r) {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseWorkboardSnapshotQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.services.WorkboardRead == nil {
		failure(w, http.StatusServiceUnavailable, "workboard_unavailable")
		return
	}
	if !takeWorkboardSlot(h.workboardSlots) {
		capacityFailure(w)
		return
	}
	defer releaseWorkboardSlot(h.workboardSlots)
	snapshot, err := safeWorkboardCall(func() (contract.BoardSnapshot, error) { return h.services.WorkboardRead(r.Context(), boardID, options) })
	if err != nil || snapshot.Validate() != nil || snapshot.Board.ID != boardID || len(snapshot.Cards) > options.Limit {
		workboardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) serveWorkboardEvents(w http.ResponseWriter, r *http.Request, boardID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !strictNativeGET(r) {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	options, err := parseWorkboardEventQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	if h.services.WorkboardEvents == nil {
		failure(w, http.StatusServiceUnavailable, "workboard_events_unavailable")
		return
	}
	if !takeWorkboardSlot(h.workboardSlots) {
		capacityFailure(w)
		return
	}
	defer releaseWorkboardSlot(h.workboardSlots)
	page, err := safeWorkboardCall(func() (contract.BoardEventPage, error) {
		return h.services.WorkboardEvents(r.Context(), boardID, options)
	})
	if err != nil || page.Validate() != nil || page.BoardID != boardID || len(page.Items) > options.Limit {
		workboardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) serveWorkboardMutation(w http.ResponseWriter, r *http.Request, boardID string, required contract.BoardAction) {
	key, ok := exactIdempotencyHeader(r)
	if !ok {
		failure(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	var input contract.BoardRequest
	if decodeWorkboardJSON(r, &input) != nil || input.Validate() != nil || input.IdempotencyKey != key ||
		(required != "" && input.Action != required) || (boardID != "" && (input.Action == contract.BoardCreate || input.BoardID != boardID)) {
		failure(w, http.StatusBadRequest, "invalid_workboard_request")
		return
	}
	if h.services.WorkboardMutate == nil {
		failure(w, http.StatusServiceUnavailable, "workboard_mutation_unavailable")
		return
	}
	if !takeWorkboardSlot(h.workboardWrites) {
		capacityFailure(w)
		return
	}
	defer releaseWorkboardSlot(h.workboardWrites)
	receipt, err := safeWorkboardCall(func() (contract.OperationReceipt, error) { return h.services.WorkboardMutate(r.Context(), input) })
	if err != nil || receipt.Validate() != nil || (boardID != "" && receipt.BoardID != boardID) {
		workboardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func strictNativeGET(r *http.Request) bool {
	return r.Method == http.MethodGet && r.Body == http.NoBody && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && !r.URL.ForceQuery
}

func decodeWorkboardJSON(r *http.Request, target any) error {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.Body == nil || r.Body == http.NoBody || r.ContentLength < 0 || r.ContentLength > workboardBodyLimit || len(r.TransferEncoding) != 0 {
		return errors.New("invalid request")
	}
	media, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(parameters) != 0 {
		return errors.New("invalid request")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, workboardBodyLimit+1))
	if err != nil || len(body) == 0 || len(body) > workboardBodyLimit || contract.RejectDuplicateJSONFields(body) != nil {
		return errors.New("invalid request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid request")
	}
	return nil
}

func exactIdempotencyHeader(r *http.Request) (string, bool) {
	var values []string
	for name, entries := range r.Header {
		if strings.EqualFold(name, "Idempotency-Key") {
			values = append(values, entries...)
		}
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return "", false
	}
	key := values[0]
	if len(key) < contract.MinIdempotencyBytes || len(key) > contract.MaxIdempotencyBytes {
		return "", false
	}
	for i := range len(key) {
		if key[i] < 33 || key[i] > 126 {
			return "", false
		}
	}
	return key, true
}

func parseWorkboardListQuery(raw string) (contract.BoardListOptions, error) {
	result := contract.BoardListOptions{Limit: 25}
	err := parseClosedQuery(raw, map[string]func(string) error{
		"after": func(v string) error { result.After = v; return nil },
		"limit": func(v string) error { n, err := canonicalPositiveInt(v); result.Limit = n; return err },
		"state": func(v string) error { result.State = v; return nil },
	})
	if err != nil || result.Validate() != nil {
		return result, errors.New("invalid query")
	}
	return result, nil
}

func parseWorkboardSnapshotQuery(raw string) (contract.BoardSnapshotOptions, error) {
	result := contract.BoardSnapshotOptions{Limit: 100}
	err := parseClosedQuery(raw, map[string]func(string) error{
		"after":       func(v string) error { result.After = v; return nil },
		"limit":       func(v string) error { n, err := canonicalPositiveInt(v); result.Limit = n; return err },
		"state":       func(v string) error { result.State = v; return nil },
		"assignee_id": func(v string) error { result.AssigneeID = v; return nil },
		"owner_id":    func(v string) error { result.OwnerID = v; return nil },
		"claim_state": func(v string) error { result.ClaimState = v; return nil },
	})
	if err != nil || result.Validate() != nil {
		return result, errors.New("invalid query")
	}
	return result, nil
}

func parseWorkboardEventQuery(raw string) (contract.BoardEventOptions, error) {
	result := contract.BoardEventOptions{Limit: 100}
	err := parseClosedQuery(raw, map[string]func(string) error{
		"after": func(v string) error { result.After = v; return nil },
		"limit": func(v string) error { n, err := canonicalPositiveInt(v); result.Limit = n; return err },
	})
	if err != nil || result.Validate() != nil {
		return result, errors.New("invalid query")
	}
	return result, nil
}

func parseClosedQuery(raw string, setters map[string]func(string) error) error {
	if len(raw) > 4096 {
		return errors.New("invalid query")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return err
	}
	for key, items := range values {
		setter, known := setters[key]
		if !known || len(items) != 1 || setter(items[0]) != nil {
			return errors.New("invalid query")
		}
	}
	return nil
}

func canonicalPositiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || strconv.Itoa(n) != value {
		return 0, errors.New("invalid integer")
	}
	return n, nil
}

func nativeWorkboardReadID(path string) string {
	return exactNativeWorkboardMiddle(path, "/v1/workboards/", "")
}

func nativeWorkboardOperationID(path string) string {
	return exactNativeWorkboardMiddle(path, "/v1/workboards/", "/operations")
}

func nativeWorkboardEventID(path string) string {
	return exactNativeWorkboardMiddle(path, "/v1/workboards/", "/events")
}

func exactNativeWorkboardMiddle(path, prefix, suffix string) string {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") || !contract.ValidID(id) {
		return ""
	}
	return id
}

func takeWorkboardSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseWorkboardSlot(slots chan struct{}) { <-slots }

func capacityFailure(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	failure(w, http.StatusServiceUnavailable, "capacity")
}

func safeWorkboardCall[T any](call func() (T, error)) (result T, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("workboard unavailable")
		}
	}()
	return call()
}

func workboardFailure(w http.ResponseWriter, err error) {
	var violation *workboard.Violation
	switch {
	case errors.As(err, &violation) && violation.Code == workboard.CodeStaleRevision:
		failure(w, http.StatusConflict, "revision_conflict")
	case errors.As(err, &violation) && violation.Code == workboard.CodeMissingNode:
		failure(w, http.StatusNotFound, "not_found")
	case errors.As(err, &violation):
		failure(w, http.StatusUnprocessableEntity, "workboard_rejected")
	case errors.Is(err, context.DeadlineExceeded):
		failure(w, http.StatusGatewayTimeout, "workboard_timeout")
	default:
		failure(w, http.StatusServiceUnavailable, "workboard_unavailable")
	}
}
