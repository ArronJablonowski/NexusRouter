package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

const auditRequestLimit = 8 << 10

// auditRoute deliberately recognizes malformed audit paths so they cannot
// fall through to the generic task inspection or event handlers.
func auditRoute(path string) bool {
	if !strings.HasPrefix(path, "/v1/tasks/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/tasks/"), "/")
	return len(parts) >= 2 && parts[1] == "audits"
}

func (h *Handler) serveAudits(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
	if len(parts) < 2 || parts[1] != "audits" || !evaluation.ValidAuditOperationID(parts[0]) {
		failure(w, 400, "invalid_audit_path")
		return
	}
	task := parts[0]
	switch {
	case len(parts) == 2 && r.Method == http.MethodPost:
		h.serveAuditRun(w, r, task)
	case len(parts) == 3 && r.Method == http.MethodGet:
		h.serveAuditInspect(w, r, task, parts[2])
	case len(parts) == 4 && parts[3] == "cancel" && r.Method == http.MethodPost:
		h.serveAuditCancel(w, r, task, parts[2])
	case len(parts) == 4 && parts[3] == "events" && r.Method == http.MethodGet:
		h.serveAuditEvents(w, r, task, parts[2])
	default:
		failure(w, 404, "not_found")
	}
}

func (h *Handler) serveAuditRun(w http.ResponseWriter, r *http.Request, task string) {
	if h.services.RunAudit == nil {
		failure(w, 503, "audits_unavailable")
		return
	}
	if auditHasHeader(r.Header, "Last-Event-ID") {
		failure(w, 400, "resume_not_supported")
		return
	}
	key, ok := submissionKey(r.Header)
	if !ok {
		failure(w, 400, "invalid_idempotency_key")
		return
	}
	if !auditJSONMedia(w, r) {
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "capacity")
		return
	}
	req, ok := decodeAuditRequest(w, r, task, key)
	if !ok {
		return
	}
	if !taskStreamWriter(w) {
		failure(w, 500, "streaming_unavailable")
		return
	}
	if r.Context().Err() != nil {
		return
	}

	stream := newAuditStream(w, r)
	defer func() {
		if recover() != nil {
			if stream.started && !stream.broken {
				stream.error("internal_error")
			} else if !stream.started {
				failure(w, 500, "internal_error")
			}
		}
	}()
	var last evaluation.AuditEvent
	var count int64
	var eventMu sync.Mutex
	corrupt := false
	status, runErr := h.services.RunAudit(r.Context(), req, func(event evaluation.AuditEvent) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		safe, valid := auditSafeStatus(event.Status, auditResponseSecrets(r, key)...)
		event.Status = safe
		if !valid || event.Validate() != nil || !validAuditOperationID(event.AuditID) || event.Status.TaskID != task || event.Sequence != count+1 || (count > 0 && event.AuditID != last.AuditID) {
			corrupt = true
			return app.ErrAuditDelivery
		}
		body, err := json.Marshal(event)
		if err != nil {
			corrupt = true
			return app.ErrAuditDelivery
		}
		if count == 0 && !stream.start() {
			return app.ErrAuditDelivery
		}
		if err := stream.write("audit", event.AuditID+":"+strconv.FormatInt(event.Sequence, 10), body); err != nil {
			return app.ErrAuditDelivery
		}
		last, count = event, event.Sequence
		return nil
	})
	if stream.broken || r.Context().Err() != nil {
		return
	}
	if corrupt {
		if stream.started {
			stream.error("invalid_audit_event")
		} else {
			failure(w, 500, "invalid_audit_event")
		}
		return
	}
	if count == 0 {
		if runErr != nil {
			auditFailure(w, runErr, false)
		} else {
			failure(w, 500, "invalid_audit_status")
		}
		return
	}
	status, ok = auditSafeStatus(status, auditResponseSecrets(r, key)...)
	if !ok {
		stream.error("invalid_audit_status")
		return
	}
	if status.Validate() != nil || status.TaskID != task || status.ID != last.AuditID || !reflect.DeepEqual(status, last.Status) || (status.Status == "pending") != (count == 1) || (status.Status != "pending") != (count == 2) {
		stream.error("invalid_audit_status")
		return
	}
	// A durable terminal event is the complete result, including safe failure
	// disposition. Do not append a second error event for provider internals.
	if runErr != nil && status.Status == "pending" {
		stream.error(auditErrorCode(runErr))
	}
}

func (h *Handler) serveAuditInspect(w http.ResponseWriter, r *http.Request, task, operation string) {
	if !validAuditOperationID(operation) {
		failure(w, 400, "invalid_audit_id")
		return
	}
	if h.services.InspectAudit == nil {
		failure(w, 503, "audits_unavailable")
		return
	}
	if !auditNoBody(w, r) {
		return
	}
	release, ok := h.auditControlPermit(w)
	if !ok {
		return
	}
	defer release()
	status, err := h.services.InspectAudit(r.Context(), task, operation)
	if err != nil {
		auditFailure(w, err, false)
		return
	}
	status, ok = auditSafeStatus(status, auditResponseSecrets(r, "")...)
	if !ok || !validAuditStatusFor(status, task, operation) {
		failure(w, 500, "invalid_audit_status")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) serveAuditCancel(w http.ResponseWriter, r *http.Request, task, operation string) {
	if !validAuditOperationID(operation) {
		failure(w, 400, "invalid_audit_id")
		return
	}
	if h.services.CancelAudit == nil {
		failure(w, 503, "audits_unavailable")
		return
	}
	release, ok := h.auditControlPermit(w)
	if !ok {
		return
	}
	defer release()
	if !auditEmptyObject(w, r) {
		return
	}
	status, err := h.services.CancelAudit(r.Context(), task, operation)
	if err != nil {
		auditFailure(w, err, false)
		return
	}
	status, ok = auditSafeStatus(status, auditResponseSecrets(r, "")...)
	if !ok || !validAuditStatusFor(status, task, operation) {
		failure(w, 500, "invalid_audit_status")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) serveAuditEvents(w http.ResponseWriter, r *http.Request, task, operation string) {
	if !validAuditOperationID(operation) {
		failure(w, 400, "invalid_audit_id")
		return
	}
	if h.services.AuditEvents == nil {
		failure(w, 503, "audit_events_unavailable")
		return
	}
	if !auditNoBody(w, r) {
		return
	}
	after, err := auditCursor(r.Header, operation)
	if err != nil {
		failure(w, 400, "invalid_event_cursor")
		return
	}
	release, ok := h.auditControlPermit(w)
	if !ok {
		return
	}
	defer release()
	page, err := h.services.AuditEvents(r.Context(), task, operation, after)
	if err != nil {
		if errors.Is(err, app.ErrAdmission) {
			failure(w, 409, "event_cursor_conflict")
			return
		}
		auditFailure(w, err, true)
		return
	}
	if page.Validate() != nil || page.AuditID != operation || page.FromSequence != after {
		failure(w, 500, "invalid_audit_event_page")
		return
	}
	if !taskStreamWriter(w) {
		failure(w, 500, "streaming_unavailable")
		return
	}
	bodies := make([][]byte, len(page.Events))
	for i, event := range page.Events {
		safe, valid := auditSafeStatus(event.Status, auditResponseSecrets(r, "")...)
		page.Events[i].Status = safe
		if !valid || page.Events[i].Validate() != nil || safe.TaskID != task || event.AuditID != operation {
			failure(w, 500, "invalid_audit_event_page")
			return
		}
		bodies[i], err = json.Marshal(page.Events[i])
		if err != nil {
			failure(w, 500, "invalid_audit_event_page")
			return
		}
	}
	if page.Validate() != nil {
		failure(w, 500, "invalid_audit_event_page")
		return
	}
	checkpoint, err := json.Marshal(struct {
		Version      int    `json:"version"`
		AuditID      string `json:"audit_id"`
		FromSequence int64  `json:"from_sequence"`
		NextSequence int64  `json:"next_sequence"`
		HeadSequence int64  `json:"head_sequence"`
		HasMore      bool   `json:"has_more"`
	}{page.Version, page.AuditID, page.FromSequence, page.NextSequence, page.HeadSequence, page.HasMore})
	if err != nil {
		failure(w, 500, "invalid_audit_event_page")
		return
	}
	stream := newAuditStream(w, r)
	if !stream.start() {
		return
	}
	for i, event := range page.Events {
		if stream.write("audit", operation+":"+strconv.FormatInt(event.Sequence, 10), bodies[i]) != nil {
			return
		}
	}
	_ = stream.write("checkpoint", "", checkpoint)
}

func decodeAuditRequest(w http.ResponseWriter, r *http.Request, task, key string) (evaluation.AuditRequest, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, auditRequestLimit))
	if err != nil {
		status := 400
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			status = 413
		}
		failure(w, status, "invalid_request")
		return evaluation.AuditRequest{}, false
	}
	fields, err := chatObject(body, "reviewer_model_id", "max_cost")
	var reviewer string
	var maxCost float64
	if err != nil || len(fields) != 2 || chatString(fields["reviewer_model_id"], &reviewer) != nil || !evaluation.ValidAuditOperationID(reviewer) || auditNumber(fields["max_cost"], &maxCost) != nil || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) || maxCost < 0 {
		failure(w, 400, "invalid_request")
		return evaluation.AuditRequest{}, false
	}
	req := evaluation.AuditRequest{Version: 1, IdempotencyKey: key, TaskID: task, ReviewerModelID: reviewer, MaxCost: maxCost}
	if req.Validate() != nil {
		failure(w, 400, "invalid_request")
		return evaluation.AuditRequest{}, false
	}
	return req, true
}

func auditNumber(raw json.RawMessage, target *float64) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return evaluation.ErrAudit
	}
	return json.Unmarshal(raw, target)
}

func auditJSONMedia(w http.ResponseWriter, r *http.Request) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return false
	}
	return true
}

func auditEmptyObject(w http.ResponseWriter, r *http.Request) bool {
	if !auditJSONMedia(w, r) {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		status := 400
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			status = 413
		}
		failure(w, status, "invalid_request")
		return false
	}
	fields, err := chatObject(body)
	if err != nil || len(fields) != 0 {
		failure(w, 400, "invalid_request")
		return false
	}
	return true
}

func auditNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(w, 400, "invalid_request")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		failure(w, 400, "invalid_request")
		return false
	}
	return true
}

func (h *Handler) auditControlPermit(w http.ResponseWriter) (func(), bool) {
	select {
	case h.controls <- struct{}{}:
		return func() { <-h.controls }, true
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "control_capacity")
		return nil, false
	}
}

func validAuditStatusFor(status evaluation.AuditStatus, task, operation string) bool {
	return status.Validate() == nil && status.TaskID == task && status.ID == operation
}

func auditResponseSecrets(r *http.Request, idempotencyKey string) []string {
	secrets := []string{}
	if idempotencyKey != "" {
		secrets = append(secrets, idempotencyKey)
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && len(strings.TrimPrefix(auth, "Bearer ")) >= 16 {
		secrets = append(secrets, strings.TrimPrefix(auth, "Bearer "))
	}
	return secrets
}

// auditSafeStatus is a final transport-boundary defense. The application
// service owns policy redaction; this additionally guarantees that HTTP
// credentials and raw idempotency keys cannot be reflected by corrupt output.
func auditSafeStatus(in evaluation.AuditStatus, secrets ...string) (evaluation.AuditStatus, bool) {
	out := in
	out.Findings = make([]evaluation.AuditFinding, len(in.Findings))
	for i, finding := range in.Findings {
		out.Findings[i] = finding
		out.Findings[i].EvidenceRefs = append([]string(nil), finding.EvidenceRefs...)
	}
	out.EvidenceRefs = make([]string, len(in.EvidenceRefs))
	copy(out.EvidenceRefs, in.EvidenceRefs)
	out.EvidencePrecedence = append([]evaluation.Source(nil), in.EvidencePrecedence...)
	if in.Usage != nil {
		usage := *in.Usage
		out.Usage = &usage
	}
	if in.StartedAt != nil {
		started := *in.StartedAt
		out.StartedAt = &started
	}
	if in.FinishedAt != nil {
		finished := *in.FinishedAt
		out.FinishedAt = &finished
	}
	identities := []string{out.ID, out.TaskID, out.SourceAttemptID, out.Status, out.TerminalDisposition, out.ErrorCode, out.ReviewerID, out.EvaluatorModel, out.EvaluatorProvider, out.RubricVersion, out.Domain, out.AuditID}
	identities = append(identities, out.EvidenceRefs...)
	for _, finding := range out.Findings {
		identities = append(identities, finding.EvidenceRefs...)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, identity := range identities {
			if strings.Contains(identity, secret) {
				return evaluation.AuditStatus{}, false
			}
		}
		for i := range out.Findings {
			out.Findings[i].Summary = strings.ReplaceAll(out.Findings[i].Summary, secret, "[REDACTED]")
		}
	}
	return out, out.Validate() == nil
}

func validAuditOperationID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id || !evaluation.ValidAuditOperationID(id) {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 32
}

func auditFailure(w http.ResponseWriter, err error, events bool) {
	status, code := 500, "audits_unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, code = 404, "audit_unavailable"
	case errors.Is(err, telemetry.ErrConflict):
		status, code = 409, "audit_conflict"
	case errors.Is(err, context.Canceled):
		status, code = 409, "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = 504, "deadline_exceeded"
	case errors.Is(err, app.ErrAdmission):
		status, code = 422, "admission_denied"
	case errors.Is(err, app.ErrAuditDelivery):
		code = "event_delivery_failed"
	}
	if events && status == 500 {
		code = "audit_events_unavailable"
	}
	failure(w, status, code)
}

func auditErrorCode(err error) string {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "audit_unavailable"
	case errors.Is(err, telemetry.ErrConflict):
		return "audit_conflict"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, app.ErrAdmission):
		return "admission_denied"
	case errors.Is(err, app.ErrAuditDelivery):
		return "event_delivery_failed"
	default:
		return "audit_failed"
	}
}

func auditCursor(header http.Header, operation string) (int64, error) {
	var values []string
	for key, items := range header {
		if strings.EqualFold(key, "Last-Event-ID") {
			if values != nil || len(items) != 1 {
				return 0, evaluation.ErrAudit
			}
			values = items
		}
	}
	if values == nil {
		return 0, nil
	}
	prefix := operation + ":"
	if !strings.HasPrefix(values[0], prefix) {
		return 0, evaluation.ErrAudit
	}
	number := strings.TrimPrefix(values[0], prefix)
	after, err := strconv.ParseInt(number, 10, 64)
	if err != nil || after < 0 || strconv.FormatInt(after, 10) != number {
		return 0, evaluation.ErrAudit
	}
	return after, nil
}

func auditHasHeader(header http.Header, name string) bool {
	for key := range header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

type auditStream struct {
	w       http.ResponseWriter
	r       *http.Request
	ctl     *http.ResponseController
	started bool
	broken  bool
}

func newAuditStream(w http.ResponseWriter, r *http.Request) *auditStream {
	return &auditStream{w: w, r: r, ctl: http.NewResponseController(w)}
}

func (s *auditStream) start() (ok bool) {
	defer func() {
		if recover() != nil {
			s.broken = true
			ok = false
		}
	}()
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-store")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(200)
	s.started = true
	if err := s.deadline(); err != nil {
		s.broken = true
		return false
	}
	if err := s.ctl.Flush(); err != nil {
		s.broken = true
		return false
	}
	if !s.clearDeadline() {
		s.broken = true
		return false
	}
	return true
}

func (s *auditStream) write(kind, id string, body []byte) (err error) {
	if s.broken {
		return app.ErrAuditDelivery
	}
	defer func() {
		if recover() != nil {
			err = app.ErrAuditDelivery
		}
		if err != nil {
			s.broken = true
		}
	}()
	if s.r.Context().Err() != nil {
		return s.r.Context().Err()
	}
	if err := s.deadline(); err != nil {
		return err
	}
	frame := "event: " + kind + "\n"
	if id != "" {
		frame += "id: " + id + "\n"
	}
	frame += "data: " + string(body) + "\n\n"
	n, err := io.WriteString(s.w, frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	if err := s.ctl.Flush(); err != nil {
		return err
	}
	if !s.clearDeadline() {
		return app.ErrAuditDelivery
	}
	return nil
}

func (s *auditStream) error(code string) {
	body, err := json.Marshal(map[string]string{"error": code})
	if err == nil {
		_ = s.write("error", "", body)
	}
}

func (s *auditStream) deadline() error {
	err := s.ctl.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *auditStream) clearDeadline() bool {
	err := s.ctl.SetWriteDeadline(time.Time{})
	return err == nil || errors.Is(err, http.ErrNotSupported)
}
