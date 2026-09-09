package webuiapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

type MutationServices struct {
	Chat            func(context.Context, string, contract.ChatRequest) (contract.ChatMutationReceipt, error)
	Cancel          func(context.Context, string, contract.ChatRequest) (contract.CancellationReceipt, error)
	Steer           func(context.Context, string, contract.ChatRequest) (contract.SteeringReceipt, error)
	TaskControls    func(context.Context, string) (contract.TaskControlStatus, error)
	FeedbackContext func(context.Context, string) (contract.FeedbackContext, error)
	Feedback        func(context.Context, string, contract.FeedbackRequest) (contract.FeedbackReceipt, error)
	Approvals       func(context.Context, string, string, int) (contract.ApprovalPage, error)
	DecideApproval  func(context.Context, string, contract.ApprovalRequest) (contract.ApprovalDecisionReceipt, error)
	Operations      func(context.Context, string, string, int) (contract.OperationPage, error)
	Submission      func(context.Context, string, string) (contract.SubmissionStatus, error)
}

func (h *Handler) serveMutationAPI(writer http.ResponseWriter, request *http.Request) bool {
	base := h.basePath + "/api/v1"
	path := request.URL.Path
	switch {
	case path == base+"/chats" && request.Method == http.MethodPost:
		h.serveChatMutation(writer, request, contract.ChatSubmit, "", false)
		return true
	case chatResumeID(h.basePath, path) != "":
		h.serveChatMutation(writer, request, contract.ChatResume, chatResumeID(h.basePath, path), false)
		return true
	case taskActionID(h.basePath, path, "steering") != "":
		h.serveChatMutation(writer, request, contract.ChatSteer, taskActionID(h.basePath, path, "steering"), true)
		return true
	case taskActionID(h.basePath, path, "cancel") != "":
		h.serveChatMutation(writer, request, contract.ChatCancel, taskActionID(h.basePath, path, "cancel"), true)
		return true
	case submissionCancelID(h.basePath, path) != "":
		h.serveChatMutation(writer, request, contract.ChatCancelSubmission, submissionCancelID(h.basePath, path), true)
		return true
	case path == base+"/feedback" && request.Method == http.MethodPost:
		h.serveFeedbackMutation(writer, request, contract.FeedbackRecord)
		return true
	case path == base+"/feedback/revisions":
		h.serveFeedbackMutation(writer, request, contract.FeedbackRevise)
		return true
	case taskActionID(h.basePath, path, "controls") != "":
		h.serveTaskControls(writer, request, taskActionID(h.basePath, path, "controls"))
		return true
	case taskActionID(h.basePath, path, "feedback") != "":
		h.serveFeedbackContext(writer, request, taskActionID(h.basePath, path, "feedback"))
		return true
	case path == base+"/operations":
		h.serveOperationList(writer, request)
		return true
	case submissionStatusID(h.basePath, path) != "":
		h.serveSubmissionStatus(writer, request, submissionStatusID(h.basePath, path))
		return true
	case approvalListTaskID(h.basePath, path) != "":
		h.serveApprovalList(writer, request, approvalListTaskID(h.basePath, path))
		return true
	default:
		task, approval := approvalDecisionIDs(h.basePath, path)
		if task != "" {
			h.serveApprovalDecision(writer, request, task, approval)
			return true
		}
	}
	return false
}

func (h *Handler) serveChatMutation(writer http.ResponseWriter, request *http.Request, action contract.ChatAction, pathID string, control bool) {
	if request.Method != http.MethodPost {
		h.writeError(writer, request, http.StatusNotFound, "not_found")
		return
	}
	if !h.requireMutationAuthority(writer, request) {
		return
	}
	if !mutationSlot(h, control) {
		writer.Header().Set("Retry-After", "1")
		h.writeError(writer, request, http.StatusServiceUnavailable, "mutation_capacity")
		return
	}
	defer releaseMutationSlot(h, control)
	var input contract.ChatRequest
	if decodeMutationJSON(request, &input, contract.MaxRequestBytes) != nil || input.Validate() != nil || input.Action != action || pathID != "" && action == contract.ChatResume && input.ChatID != pathID || pathID != "" && (action == contract.ChatSteer || action == contract.ChatCancel) && input.TaskID != pathID || pathID != "" && action == contract.ChatCancelSubmission && input.SubmissionID != pathID {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if action == contract.ChatSubmit || action == contract.ChatResume {
		if h.mutations.Chat == nil {
			h.writeError(writer, request, http.StatusServiceUnavailable, "chat_unavailable")
			return
		}
		result, err := safeCall(func() (contract.ChatMutationReceipt, error) { return h.mutations.Chat(ctx, subject, input) })
		if err != nil || result.Validate() != nil {
			h.mutationFailure(writer, request, err, h.taskMutationMeta(ctx, input.TaskID, err))
			return
		}
		h.writeJSON(writer, http.StatusAccepted, result)
		return
	}
	if action == contract.ChatSteer {
		if h.mutations.Steer == nil {
			h.writeError(writer, request, http.StatusServiceUnavailable, "steering_unavailable")
			return
		}
		result, err := safeCall(func() (contract.SteeringReceipt, error) { return h.mutations.Steer(ctx, subject, input) })
		if err != nil || result.Validate() != nil {
			h.mutationFailure(writer, request, err, h.taskMutationMeta(ctx, input.TaskID, err))
			return
		}
		h.writeJSON(writer, http.StatusOK, result)
		return
	}
	if h.mutations.Cancel == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "cancellation_unavailable")
		return
	}
	result, err := safeCall(func() (contract.CancellationReceipt, error) { return h.mutations.Cancel(ctx, subject, input) })
	if err != nil || result.Validate() != nil {
		meta := h.taskMutationMeta(ctx, input.TaskID, err)
		if input.Action == contract.ChatCancelSubmission {
			meta = mutationErrorMeta{subjectType: "submission", subjectID: input.SubmissionID}
		}
		h.mutationFailure(writer, request, err, meta)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveFeedbackMutation(writer http.ResponseWriter, request *http.Request, action contract.FeedbackAction) {
	if request.Method != http.MethodPost {
		h.writeError(writer, request, http.StatusNotFound, "not_found")
		return
	}
	if !h.requireMutationAuthority(writer, request) {
		return
	}
	if !mutationSlot(h, false) {
		h.writeError(writer, request, http.StatusServiceUnavailable, "mutation_capacity")
		return
	}
	defer releaseMutationSlot(h, false)
	var input contract.FeedbackRequest
	if decodeMutationJSON(request, &input, 4<<10) != nil || input.Validate() != nil || input.Action != action {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.mutations.Feedback == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "feedback_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	result, err := safeCall(func() (contract.FeedbackReceipt, error) { return h.mutations.Feedback(ctx, subject, input) })
	if err != nil || result.Validate() != nil {
		meta := mutationErrorMeta{subjectType: "task", subjectID: input.TaskID}
		if errors.Is(err, browserops.ErrConflict) && h.mutations.FeedbackContext != nil {
			if current, currentErr := h.mutations.FeedbackContext(ctx, input.TaskID); currentErr == nil {
				meta.currentRevision = &current.Revision
			}
		}
		h.mutationFailure(writer, request, err, meta)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveTaskControls(writer http.ResponseWriter, request *http.Request, task string) {
	h.serveProjection(writer, request, func(ctx context.Context) (contract.TaskControlStatus, error) {
		if h.mutations.TaskControls == nil {
			return contract.TaskControlStatus{}, app.ErrBrowserMutation
		}
		return h.mutations.TaskControls(ctx, task)
	})
}

func (h *Handler) serveFeedbackContext(writer http.ResponseWriter, request *http.Request, task string) {
	h.serveProjection(writer, request, func(ctx context.Context) (contract.FeedbackContext, error) {
		if h.mutations.FeedbackContext == nil {
			return contract.FeedbackContext{}, app.ErrBrowserMutation
		}
		return h.mutations.FeedbackContext(ctx, task)
	})
}

func (h *Handler) serveProjection[T interface{ Validate() error }](writer http.ResponseWriter, request *http.Request, call func(context.Context) (T, error)) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := safeCall(func() (T, error) { return call(ctx) })
	if err != nil || result.Validate() != nil {
		h.mutationFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveApprovalList(writer http.ResponseWriter, request *http.Request, task string) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.mutations.Approvals == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "approvals_unavailable")
		return
	}
	after, limit, err := approvalQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := safeCall(func() (contract.ApprovalPage, error) { return h.mutations.Approvals(ctx, task, after, limit) })
	if err != nil || result.Validate() != nil || result.TaskID != task {
		h.mutationFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveOperationList(writer http.ResponseWriter, request *http.Request) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Method != http.MethodGet || request.Body != http.NoBody || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.URL.ForceQuery {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	after, limit, err := operationQuery(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.mutations.Operations == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "operations_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := safeCall(func() (contract.OperationPage, error) { return h.mutations.Operations(ctx, subject, after, limit) })
	if err != nil || result.Validate() != nil {
		h.mutationFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveSubmissionStatus(writer http.ResponseWriter, request *http.Request, submission string) {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.mutations.Submission == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "submission_unavailable")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := safeCall(func() (contract.SubmissionStatus, error) { return h.mutations.Submission(ctx, subject, submission) })
	if err != nil || result.Validate() != nil || result.SubmissionID != submission {
		h.mutationFailure(writer, request, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

func (h *Handler) serveApprovalDecision(writer http.ResponseWriter, request *http.Request, task, approval string) {
	if request.Method != http.MethodPost {
		h.writeError(writer, request, http.StatusNotFound, "not_found")
		return
	}
	if !h.requireMutationAuthority(writer, request) {
		return
	}
	if !mutationSlot(h, true) {
		h.writeError(writer, request, http.StatusServiceUnavailable, "mutation_capacity")
		return
	}
	defer releaseMutationSlot(h, true)
	var input contract.ApprovalRequest
	if decodeMutationJSON(request, &input, 16<<10) != nil || input.Validate() != nil || input.TaskID != task || input.ApprovalID != approval {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.mutations.DecideApproval == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "approvals_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	result, err := safeCall(func() (contract.ApprovalDecisionReceipt, error) {
		return h.mutations.DecideApproval(ctx, subject, input)
	})
	if err != nil || result.Validate() != nil {
		h.mutationFailure(writer, request, err, mutationErrorMeta{subjectType: "approval", subjectID: input.ApprovalID})
		return
	}
	h.writeJSON(writer, http.StatusOK, result)
}

type mutationErrorMeta struct {
	subjectType, subjectID, operationID string
	currentRevision                     *int64
}

func (h *Handler) mutationFailure(writer http.ResponseWriter, request *http.Request, err error, details ...mutationErrorMeta) {
	status, code := http.StatusServiceUnavailable, "mutation_unavailable"
	retryable := true
	switch {
	case errors.Is(err, browserops.ErrConflict), errors.Is(err, telemetry.ErrConflict), errors.Is(err, submissions.ErrConflict), errors.Is(err, approvals.ErrConflict), errors.Is(err, runtime.ErrSteeringClosed):
		status, code = http.StatusConflict, "revision_conflict"
	case errors.Is(err, sql.ErrNoRows):
		status, code = http.StatusNotFound, "not_found"
		retryable = false
	case errors.Is(err, runtime.ErrSteeringLimit), errors.Is(err, submissions.ErrCapacity), errors.Is(err, browserops.ErrCapacity):
		status, code = http.StatusTooManyRequests, "capacity"
	case errors.Is(err, app.ErrAdmission), errors.Is(err, approvals.ErrInvalid), errors.Is(err, browserops.ErrInvalid):
		status, code = http.StatusUnprocessableEntity, "admission_denied"
		retryable = false
	}
	published := contract.Error{Version: 1, Code: code, Message: mutationErrorMessage(code), Retryable: retryable}
	if len(details) == 1 {
		published.SubjectType, published.SubjectID, published.OperationID, published.CurrentRevision = details[0].subjectType, details[0].subjectID, details[0].operationID, details[0].currentRevision
	}
	var operationErr *app.BrowserOperationError
	if errors.As(err, &operationErr) {
		published.OperationID = operationErr.OperationID
	}
	if published.Validate() != nil {
		published = contract.Error{Version: 1, Code: "mutation_unavailable", Message: "The operation could not be completed.", Retryable: true}
		status = http.StatusServiceUnavailable
	}
	h.writeJSON(writer, status, published)
}

func mutationErrorMessage(code string) string {
	switch code {
	case "revision_conflict":
		return "The resource changed; refresh it before retrying."
	case "not_found":
		return "The requested resource was not found."
	case "capacity":
		return "The service is at capacity; retry later."
	case "admission_denied":
		return "The operation is not allowed in the current state."
	default:
		return "The operation could not be completed."
	}
}

func (h *Handler) taskMutationMeta(ctx context.Context, task string, err error) mutationErrorMeta {
	meta := mutationErrorMeta{}
	if task == "" {
		return meta
	}
	meta.subjectType, meta.subjectID = "task", task
	if errors.Is(err, browserops.ErrConflict) && h.mutations.TaskControls != nil {
		if current, currentErr := h.mutations.TaskControls(ctx, task); currentErr == nil {
			meta.currentRevision = &current.Revision
		}
	}
	return meta
}

func (h *Handler) requireMutationAuthority(writer http.ResponseWriter, request *http.Request) bool {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if !h.authorizedMutation(request) {
		h.writeError(writer, request, http.StatusForbidden, "request_denied")
		return false
	}
	return true
}

func decodeMutationJSON(request *http.Request, target any, limit int64) error {
	if request.URL.RawQuery != "" || request.URL.ForceQuery || request.Header.Get("Content-Type") != "application/json" || request.Body == nil || request.Body == http.NoBody || request.ContentLength < 0 || request.ContentLength > limit || len(request.TransferEncoding) != 0 {
		return errors.New("invalid request")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || len(body) == 0 || int64(len(body)) > limit || contract.RejectDuplicateJSONFields(body) != nil {
		return errors.New("invalid request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid request")
	}
	return nil
}

func safeCall[T any](call func() (T, error)) (result T, err error) {
	defer func() {
		if recover() != nil {
			err = app.ErrBrowserMutation
		}
	}()
	return call()
}

func mutationSlot(h *Handler, control bool) bool {
	slots := h.mutationSlots
	if control {
		slots = h.controlSlots
	}
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseMutationSlot(h *Handler, control bool) {
	if control {
		<-h.controlSlots
	} else {
		<-h.mutationSlots
	}
}

func chatResumeID(base, path string) string {
	return exactMiddle(path, base+"/api/v1/chats/", "/resume")
}

func submissionCancelID(base, path string) string {
	return exactMiddle(path, base+"/api/v1/submissions/", "/cancel")
}

func submissionStatusID(base, path string) string {
	prefix := base + "/api/v1/submissions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if strings.Contains(id, "/") || !contract.ValidID(id) {
		return ""
	}
	return id
}

func taskActionID(base, path, action string) string {
	return exactMiddle(path, base+"/api/v1/tasks/", "/"+action)
}

func approvalListTaskID(base, path string) string {
	return exactMiddle(path, base+"/api/v1/tasks/", "/approvals")
}

func approvalDecisionIDs(base, path string) (string, string) {
	prefix, suffix := base+"/api/v1/tasks/", "/decision"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", ""
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix), "/")
	if len(parts) != 3 || parts[1] != "approvals" || !contract.ValidID(parts[0]) || !contract.ValidID(parts[2]) {
		return "", ""
	}
	return parts[0], parts[2]
}

func exactMiddle(path, prefix, suffix string) string {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if !contract.ValidID(id) {
		return ""
	}
	return id
}

func approvalQuery(raw string) (string, int, error) {
	values, err := url.ParseQuery(raw)
	if err != nil || len(raw) > 4096 {
		return "", 0, errors.New("invalid query")
	}
	after, limit := "", 25
	for key, items := range values {
		if len(items) != 1 {
			return "", 0, errors.New("invalid query")
		}
		switch key {
		case "after":
			after = items[0]
		case "limit":
			limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(limit) != items[0] {
				return "", 0, errors.New("invalid query")
			}
		default:
			return "", 0, errors.New("invalid query")
		}
	}
	if limit < 1 || limit > contract.MaxApprovalItems || after != "" && !contract.ValidID(after) {
		return "", 0, errors.New("invalid query")
	}
	return after, limit, nil
}

func operationQuery(raw string) (string, int, error) {
	values, err := url.ParseQuery(raw)
	if err != nil || len(raw) > 4096 {
		return "", 0, errors.New("invalid query")
	}
	after, limit := "", 25
	for key, items := range values {
		if len(items) != 1 {
			return "", 0, errors.New("invalid query")
		}
		switch key {
		case "after":
			after = items[0]
		case "limit":
			limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(limit) != items[0] {
				return "", 0, errors.New("invalid query")
			}
		default:
			return "", 0, errors.New("invalid query")
		}
	}
	if limit < 1 || limit > contract.MaxOperationItems || after != "" && !contract.ValidID(after) {
		return "", 0, errors.New("invalid query")
	}
	return after, limit, nil
}
