// Package api exposes authenticated adapters over the application service.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/daemon"
	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/memory"
	"github.com/ArronJablonowski/NexusRouter/metrics"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

type Services struct {
	Models                  func(context.Context) ([]string, error)
	ConfiguredModels        func(context.Context) (routing.ModelCatalog, error)
	Memory                  func(context.Context, string) (memory.Fact, error)
	ExportMemory            func(context.Context) (memory.ExportSnapshot, error)
	Memories                func(context.Context, string, string, int, bool) ([]memory.Fact, error)
	PutMemory               func(context.Context, memory.Fact, int64) error
	DeleteMemory            func(context.Context, string, int64) error
	ModelDeprecation        func(context.Context, string, string, string, evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error)
	DaemonStatus            func(context.Context) (daemon.Status, error)
	StopDaemon              func(context.Context, string) (daemon.Status, error)
	DiscoverSkillWorkflows  func(context.Context, string, string, int) (skills.WorkflowCandidatePage, error)
	GenerateSkillDraft      func(context.Context, string, string, string, []string, float64) (skills.GenerationAttempt, error)
	PublishSkillGeneration  func(context.Context, string) (skills.Version, error)
	SkillGenerations        func(context.Context, string, string, int) ([]skills.GenerationSummary, error)
	SkillGeneration         func(context.Context, string, string) (skills.GenerationAttempt, error)
	ApprovalExecution       func(context.Context, string, string) (approvals.ExecutionStatus, error)
	DecideApproval          func(context.Context, approvals.Command) (approvals.Record, error)
	Approval                func(context.Context, string, string) (approvals.Record, error)
	Approvals               func(context.Context, approvals.ListOptions) (approvals.Page, error)
	SteeringList            func(context.Context, string) ([]runtime.SteeringMessage, error)
	Steer                   func(context.Context, string, string, string) (runtime.SteeringMessage, error)
	Steering                func(context.Context, string, string) (runtime.SteeringMessage, error)
	Metrics                 func(context.Context) (metrics.Snapshot, error)
	HealthReport            func(context.Context) (health.Report, error)
	SubmissionRecoveries    func(context.Context, string) ([]submissions.Recovery, error)
	Submissions             func(context.Context, submissions.ListOptions) (submissions.Page, error)
	Submit                  func(context.Context, string, app.Request) (submissions.Status, error)
	SubmitBranch            func(context.Context, string, sessions.TaskHeadFence, app.Request) (submissions.Status, error)
	SubmitResume            func(context.Context, string, sessions.TaskHeadFence, app.Request) (submissions.Status, error)
	ResumeSubmission        func(context.Context, string, app.Request) (submissions.Status, error)
	RunSubmission           func(context.Context, string, app.Request) (submissions.Status, error)
	Submission              func(context.Context, string) (submissions.Status, error)
	SubmissionStream        func(context.Context, string, int64, int) (submissions.StreamPage, error)
	CancelSubmission        func(context.Context, string) (submissions.Status, error)
	Cancel                  func(context.Context, string) (runtime.CancellationStatus, error)
	Cancellation            func(context.Context, string) (runtime.CancellationStatus, error)
	TaskContinuation        func(context.Context, string) (sessions.ContinuationStatus, error)
	RouteExplanation        func(context.Context, string) (sessions.RouteExplanation, error)
	Tasks                   func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error)
	SessionTasks            func(context.Context, string, sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error)
	SkillTaskOutcome        func(context.Context, string) (skills.TaskOutcome, error)
	CompareSkillOutcomes    func(context.Context, skills.ComparisonRequest) (skills.ComparisonReport, error)
	SelectSkillComparison   func(context.Context, skills.ComparisonSelectionRequest) (skills.ComparisonSelectionReport, error)
	TaskLeases              func(context.Context, string) (workers.TaskLeaseStatus, error)
	ScopeLeases             func(context.Context, string) (workers.ScopeLeaseStatus, error)
	LeaseAttention          func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error)
	LeaseAttentionHistory   func(context.Context, string, workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error)
	Events                  func(context.Context, string, int64, int) (sessions.EventPage, error)
	RunStream               func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error)
	RunTextStream           func(context.Context, app.Request, func(string) error) (app.Result, error)
	Summarize               func(context.Context, string, string, int, float64) (sessions.SummaryAttempt, error)
	PrepareSummary          func(context.Context, string, app.PrepareSummaryRequest) (sessions.ContextCompactionOperationState, error)
	SummaryPreparation      func(context.Context, string) (sessions.ContextCompactionOperationState, error)
	SummaryAttempt          func(context.Context, string) (sessions.SummaryAttempt, error)
	SummaryAttempts         func(context.Context, string, string, int) ([]sessions.SummaryAttempt, error)
	ReviewSummary           func(context.Context, string, string, string, string) (sessions.SummaryReview, error)
	SummaryReviews          func(context.Context, string) ([]sessions.SummaryReview, error)
	FeedbackHistory         func(context.Context, string) ([]evaluation.Record, error)
	ReviseFeedback          func(context.Context, string, string, bool) error
	RunAudit                func(context.Context, evaluation.AuditRequest, func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error)
	InspectAudit            func(context.Context, string, string) (evaluation.AuditStatus, error)
	CancelAudit             func(context.Context, string, string) (evaluation.AuditStatus, error)
	AuditEvents             func(context.Context, string, string, int64) (evaluation.AuditEventPage, error)
	TaskUsage               func(context.Context, string) (accounting.Totals, error)
	Run                     func(context.Context, app.Request) (app.Result, error)
	Inspect                 func(context.Context, string) (sessions.Snapshot, error)
	Health                  func(context.Context) error
	Feedback                func(context.Context, string, bool, float64) error
	ApproveBrowserChallenge func(context.Context, string, string) error
	WorkboardList           func(context.Context, contract.BoardListOptions) (contract.Page, error)
	WorkboardRead           func(context.Context, string, contract.BoardSnapshotOptions) (contract.BoardSnapshot, error)
	WorkboardMutate         func(context.Context, contract.BoardRequest) (contract.OperationReceipt, error)
	WorkboardEvents         func(context.Context, string, contract.BoardEventOptions) (contract.BoardEventPage, error)
	WorkboardAttemptHistory func(context.Context, string, string, contract.AttemptHistoryOptions) (contract.AttemptHistoryPage, error)
	WorkboardAttemptDetail  func(context.Context, string, string, string, contract.AttemptDetailOptions) (contract.AttemptDetailPage, error)
	WorkboardDependencies   func(context.Context, string, string, contract.DependencyOptions) (contract.DependencyPage, error)
	WorkboardFinalizeCancel func(context.Context, app.WorkboardCancelFinalizationRequest) (contract.OperationReceipt, error)
}
type Handler struct {
	modelSlots       chan struct{}
	memorySlots      chan struct{}
	deprecationSlots chan struct{}
	secret           [32]byte
	services         Services
	slots            chan struct{}
	controls         chan struct{}
	intake           chan struct{}
	healthSlots      chan struct{}
	metricsSlots     chan struct{}
	steeringSlots    chan struct{}
	approvalSlots    chan struct{}
	workboardSlots   chan struct{}
	workboardWrites  chan struct{}
	taskWaitTimeout  time.Duration
}

type bearerTokenContextKey struct{}

func New(token string, concurrent int, s Services) (*Handler, error) {
	if !canonicalBearerToken(token) || concurrent < 1 || concurrent > 64 || s.Run == nil || s.Inspect == nil || s.Health == nil {
		return nil, errors.New("invalid API configuration")
	}
	return &Handler{modelSlots: make(chan struct{}, 1), memorySlots: make(chan struct{}, 2), deprecationSlots: make(chan struct{}, 1), secret: sha256.Sum256([]byte(token)), services: s, slots: make(chan struct{}, concurrent), controls: make(chan struct{}, 2), intake: make(chan struct{}, 2), healthSlots: make(chan struct{}, 1), metricsSlots: make(chan struct{}, 1), steeringSlots: make(chan struct{}, 2), approvalSlots: make(chan struct{}, 2), workboardSlots: make(chan struct{}, 4), workboardWrites: make(chan struct{}, 1), taskWaitTimeout: 5 * time.Minute}, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(status int, code string) {
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/models" {
			failure(w, status, code)
			return
		}
		kind := "invalid_request_error"
		switch {
		case status == 401:
			kind = "authentication_error"
		case status == 403:
			kind = "permission_error"
		case status >= 500:
			kind = "server_error"
		}
		chatFailure(w, status, kind, code)
	}
	defer func() {
		if recover() != nil {
			fail(http.StatusInternalServerError, "internal_error")
		}
	}()
	bearer, ok := exactBearerToken(r.Header)
	candidate := sha256.Sum256([]byte(bearer))
	if !ok || subtle.ConstantTimeCompare(candidate[:], h.secret[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(401, "unauthorized")
		return
	}
	// Downstream response validation uses the exact credential admitted here,
	// never another interpretation of the request's header collection.
	r = r.WithContext(context.WithValue(r.Context(), bearerTokenContextKey{}, bearer))
	if r.Header.Get("Origin") != "" {
		fail(403, "browser_origin_denied")
		return
	}
	if r.URL.RawQuery != "" && !(workboardQueryRoute(r.URL.Path, r.Method) || r.URL.Path == "/v1/resources/leases" || r.URL.Path == "/v1/resources/attention" || attentionHistoryRoute(r.URL.Path) || r.Method == http.MethodGet && (r.URL.Path == "/v1/tasks" || sessionTasksRoute(r.URL.Path) || r.URL.Path == "/v1/submissions" || r.URL.Path == "/v1/skills/workflows" || approvalRoute(r.URL.Path) || skillGenerationRoute(r.URL.Path))) {
		fail(400, "query_not_supported")
		return
	}
	timeout := 5 * time.Minute
	if r.URL.Path == "/v1/tasks" && r.Method == http.MethodPost {
		timeout = h.taskWaitTimeout
	}
	if r.URL.Path == "/v1/health" && r.Method == http.MethodGet {
		timeout = 6 * time.Second
	}
	if r.URL.Path == "/v1/metrics" && r.Method == http.MethodGet {
		timeout = 5 * time.Second
	}
	if r.URL.Path == "/v1/workboards" || strings.HasPrefix(r.URL.Path, "/v1/workboards/") {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	switch {
	case r.URL.Path == "/v1/workboards" || strings.HasPrefix(r.URL.Path, "/v1/workboards/"):
		h.serveWorkboards(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/skills/comparison":
		h.serveSkillComparison(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/skills/comparison/select":
		h.serveSkillComparisonSelection(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/resources/attention":
		h.serveLeaseAttention(w, r.WithContext(ctx))
	case attentionHistoryRoute(r.URL.Path):
		h.serveLeaseAttentionHistory(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/resources/leases":
		h.serveResourceLeases(w, r.WithContext(ctx))
	case memoryManagementRoute(r.URL.Path):
		h.serveMemoryManagement(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/models/deprecation":
		h.serveDeprecation(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/routing/models":
		h.serveConfiguredModels(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/models":
		h.serveModels(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/daemon/status" || r.URL.Path == "/v1/daemon/stop":
		h.serveDaemonControl(w, r.WithContext(ctx))
	case browserChallengeApprovalID(r.URL.Path) != "":
		h.serveBrowserChallengeApproval(w, r.WithContext(ctx), browserChallengeApprovalID(r.URL.Path))
	case r.URL.Path == "/v1/skills/workflows":
		h.serveWorkflowDiscovery(w, r.WithContext(ctx))
	case skillGenerationRoute(r.URL.Path):
		h.serveSkillGenerations(w, r.WithContext(ctx))
	case approvalRoute(r.URL.Path):
		h.serveApprovals(w, r.WithContext(ctx))
	case auditRoute(r.URL.Path):
		h.serveAudits(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/usage"):
		h.serveTaskUsage(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/steering"):
		h.serveSteering(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/metrics" && r.Method == http.MethodGet:
		h.serveMetrics(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/health" && r.Method == http.MethodGet:
		h.serveHealthReport(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/submissions" || strings.HasPrefix(r.URL.Path, "/v1/submissions/"):
		h.serveSubmissions(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && ((strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == http.MethodPost) || (strings.HasSuffix(r.URL.Path, "/cancellation") && r.Method == http.MethodGet)):
		h.serveCancellation(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/continuation"):
		h.serveTaskContinuation(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/route"):
		h.serveRouteExplanation(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/skill-outcome"):
		h.serveSkillTaskOutcome(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/leases"):
		h.serveTaskLeases(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/tasks/stream" && r.Method == http.MethodPost:
		h.serveTaskStream(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/summary-preparations" || strings.HasPrefix(r.URL.Path, "/v1/summary-preparations/"):
		h.serveSummaryPreparations(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/summaries" || strings.HasPrefix(r.URL.Path, "/v1/summaries/"):
		h.serveSummaries(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/feedback/revisions" && r.Method == http.MethodPost:
		h.serveFeedbackRevision(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/feedback/") && r.Method == http.MethodGet:
		h.serveFeedbackHistory(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/feedback" && r.Method == http.MethodPost:
		h.serveFeedback(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
		h.serveChatCompletions(w, r.WithContext(ctx))
	case r.URL.Path == "/health" && r.Method == http.MethodGet:
		if h.services.Health(ctx) != nil {
			failure(w, 503, "unhealthy")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "providers_checked": false})
	case sessionTasksRoute(r.URL.Path):
		h.serveSessionTasks(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/tasks" && r.Method == http.MethodGet:
		h.serveTaskList(w, r.WithContext(ctx))
	case r.URL.Path == "/v1/tasks" && r.Method == http.MethodPost:
		h.serveTask(w, r.WithContext(ctx))
	case branchTaskID(r.URL.Path) != "":
		h.serveTaskBranch(w, r.WithContext(ctx), branchTaskID(r.URL.Path))
	case resumeTaskID(r.URL.Path) != "":
		h.serveTaskResume(w, r.WithContext(ctx), resumeTaskID(r.URL.Path))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/events") && r.URL.Path != "/v1/tasks/events" && r.Method == http.MethodGet:
		h.serveEventReplay(w, r.WithContext(ctx))
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		if id == "" || len(id) > 128 || strings.Contains(id, "/") {
			failure(w, 400, "invalid_task_id")
			return
		}
		snapshot, err := h.services.Inspect(ctx, id)
		if err != nil {
			failure(w, 404, "task_unavailable")
			return
		}
		writeJSON(w, 200, snapshot)
	default:
		fail(404, "not_found")
	}
}

func exactBearerToken(header http.Header) (string, bool) {
	var values []string
	seen := false
	for name, items := range header {
		if !strings.EqualFold(name, "Authorization") {
			continue
		}
		if seen {
			return "", false
		}
		seen = true
		values = items
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	return token, values[0] == "Bearer "+token && canonicalBearerToken(token)
}

func canonicalBearerToken(token string) bool {
	if len(token) < 32 || len(token) > 8192 {
		return false
	}
	for index := range len(token) {
		if token[index] <= ' ' || token[index] >= 0x7f || token[index] == ',' {
			return false
		}
	}
	return true
}

func admittedBearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	token, _ := r.Context().Value(bearerTokenContextKey{}).(string)
	return token
}
func failure(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func decodeRequest(reader io.Reader) (app.Request, error) {
	d := json.NewDecoder(reader)
	d.UseNumber()
	req := app.Request{}
	bad := errors.New("invalid request")
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return req, bad
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return req, bad
		}
		seen[key] = true
		if key == "compaction" {
			var raw json.RawMessage
			if d.Decode(&raw) != nil {
				return req, bad
			}
			req.Compaction, err = decodeCompactionRequest(raw)
			if err != nil {
				return req, bad
			}
			continue
		}
		var value any
		if d.Decode(&value) != nil {
			return req, bad
		}
		var target *string
		switch key {
		case "harness_difficulty":
			d, ok := value.(string)
			if !ok || (d != "unknown" && d != "easy" && d != "medium" && d != "hard") {
				return req, bad
			}
			req.HarnessDifficulty = d
			continue
		case "harness_id":
			id, ok := value.(string)
			if !ok || id == "" || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
				return req, bad
			}
			req.HarnessID = id
			continue
		case "model_id":
			target = &req.ModelID
		case "prompt":
			target = &req.Prompt
		case "continue_task_id":
			target = &req.ContinueTaskID
		case "summary_attempt_id":
			id, ok := value.(string)
			if !ok || id == "" || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
				return req, bad
			}
			req.SummaryAttemptID = id
			continue
		case "domain":
			target = &req.Domain
		case "profile":
			target = &req.Profile
		case "validation":
			validation, ok := value.(string)
			if !ok || (validation != "" && validation != "go_source") {
				return req, bad
			}
			req.Validation = validation
			continue
		case "capabilities":
			items, ok := value.([]any)
			if !ok {
				return req, bad
			}
			req.Capabilities = make([]string, len(items))
			for i, item := range items {
				text, ok := item.(string)
				if !ok {
					return req, bad
				}
				req.Capabilities[i] = text
			}
			continue
		case "context_tokens":
			number, ok := value.(json.Number)
			if !ok {
				return req, bad
			}
			req.ContextTokens, err = strconv.Atoi(number.String())
			if err != nil || req.ContextTokens < 0 {
				return req, bad
			}
			continue
		case "max_cost":
			number, ok := value.(json.Number)
			if !ok {
				return req, bad
			}
			req.MaxCost, err = number.Float64()
			if err != nil || req.MaxCost < 0 {
				return req, bad
			}
			continue
		case "local_required":
			local, ok := value.(bool)
			if !ok {
				return req, bad
			}
			req.LocalRequired = local
			continue
		default:
			return req, bad
		}
		text, ok := value.(string)
		if !ok {
			return req, bad
		}
		*target = text
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return req, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return req, bad
	}
	if req.ModelID == "" || strings.TrimSpace(req.Prompt) == "" || (req.Compaction != nil && req.ContinueTaskID == "") || (req.SummaryAttemptID != "" && (req.ContinueTaskID == "" || req.Compaction != nil)) {
		return req, bad
	}
	return req, nil
}
