package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func shellFixture(t *testing.T) http.Handler {
	t.Helper()
	handler, err := NewShellHandler(ShellOptions{
		BasePath:      "/console",
		HostAllowed:   func(host string) bool { return host == "darwin.local" },
		Authenticated: func(request *http.Request) bool { return request.Header.Get("X-Test-Session") == "valid" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func shellRequest(t *testing.T, handler http.Handler, method, target string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://darwin.local"+target, nil)
	if authenticated {
		request.Header.Set("X-Test-Session", "valid")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestShellServesEmbeddedAssetsAndClientRoutes(t *testing.T) {
	handler := shellFixture(t)
	for _, test := range []struct{ target, contentType, contains string }{
		{"/console", "text/html", "/console/assets/v1/app.js"},
		{"/console/chats/chat-a", "text/html", "DarwinRouter"},
		{"/console/workboards/board-a", "text/html", "Workboard lanes"},
		{"/console/assets/v1/app.css", "text/css", "color-scheme"},
		{"/console/assets/v1/operation-contract.js", "text/javascript", "DarwinOperationContract"},
		{"/console/assets/v1/inspector.js", "text/javascript", "DarwinInspector"},
		{"/console/assets/v1/workboards.js", "text/javascript", "kanban"},
		{"/console/assets/v1/app.js", "text/javascript", "aria-current"},
	} {
		response := shellRequest(t, handler, http.MethodGet, test.target, true)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("unexpected response for %s: %d %q", test.target, response.Code, response.Body.String())
		}
	}
}

func TestShellRequiresHostAndAuthenticationForEveryResource(t *testing.T) {
	handler := shellFixture(t)
	unauthorized := shellRequest(t, handler, http.MethodGet, "/console/assets/v1/app.js", false)
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), "app.js") {
		t.Fatal("asset bypassed authentication or leaked path")
	}
	if workboards := shellRequest(t, handler, http.MethodGet, "/console/assets/v1/workboards.js", false); workboards.Code != http.StatusUnauthorized || strings.Contains(workboards.Body.String(), "kanban") {
		t.Fatal("workboard asset bypassed authentication or leaked content")
	}
	request := httptest.NewRequest(http.MethodGet, "http://evil.example/console", nil)
	request.Header.Set("X-Test-Session", "valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal("untrusted Host accepted", response.Code)
	}
}

func TestShellRejectsTraversalInvalidMethodsAndUnknownAssets(t *testing.T) {
	handler := shellFixture(t)
	for _, target := range []string{"/console/assets/v1/missing.js", "/console/route.json", "/outside", "/console//chats", "/console/" + strings.Repeat("a", MaxShellRequestPath)} {
		if response := shellRequest(t, handler, http.MethodGet, target, true); response.Code != http.StatusNotFound {
			t.Fatalf("unsafe or unknown path accepted: %s -> %d", target, response.Code)
		}
	}
	encoded := httptest.NewRequest(http.MethodGet, "http://darwin.local/console/%2e%2e/secret", nil)
	encoded.Header.Set("X-Test-Session", "valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, encoded)
	if response.Code != http.StatusNotFound {
		t.Fatal("encoded traversal accepted", response.Code)
	}
	method := shellRequest(t, handler, http.MethodPost, "/console", true)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("invalid method handling", method.Code)
	}
}

func TestShellAppliesSecurityHeadersToSuccessAndFailure(t *testing.T) {
	handler := shellFixture(t)
	for _, response := range []*httptest.ResponseRecorder{
		shellRequest(t, handler, http.MethodGet, "/console", true),
		shellRequest(t, handler, http.MethodGet, "/console", false),
	} {
		for name, want := range map[string]string{
			"Content-Security-Policy":    shellCSP,
			"Referrer-Policy":            "no-referrer",
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Cross-Origin-Opener-Policy": "same-origin",
			"Cache-Control":              "no-store",
		} {
			if got := response.Header().Get(name); got != want {
				t.Fatalf("%s = %q, want %q", name, got, want)
			}
		}
		if response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("CORS header emitted")
		}
	}
}

func TestShellHEADAndConfigurationBounds(t *testing.T) {
	handler := shellFixture(t)
	response := shellRequest(t, handler, http.MethodHead, "/console/chats", true)
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
		t.Fatal("invalid HEAD response")
	}
	for _, options := range []ShellOptions{
		{},
		{BasePath: "/bad/", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }},
		{BasePath: "/" + strings.Repeat("a", MaxShellBasePath), HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }},
	} {
		if _, err := NewShellHandler(options); err == nil {
			t.Fatal("invalid shell configuration accepted")
		}
	}
}

func TestEmbeddedShellHasNoExternalResourcesOrInlineCode(t *testing.T) {
	digest, err := ShellAssetDigest()
	if err != nil || digest != "2310dd1286d55734a4fc2b70b23ba3fc8877628fc35aafda2d9f707bbafe7909" || ShellAssetVersion != "v1" {
		t.Fatal("embedded shell manifest changed without a versioned review", digest, err)
	}
	for _, name := range []string{"assets/v1/index.html", "assets/v1/app.css", "assets/v1/operation-contract.js", "assets/v1/inspector.js", "assets/v1/workboards.js", "assets/v1/app.js", "assets/v1/bootstrap.html", "assets/v1/bootstrap.css", "assets/v1/bootstrap.js"} {
		file, err := embeddedShellAssets.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		for _, forbidden := range []string{"http://", "https://", "//cdn", "<iframe", "<object", "<embed", "serviceworker", "localstorage", "sessionstorage", "indexeddb"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("external or active resource %q in %s", forbidden, name)
			}
		}
	}
	index, _ := embeddedShellAssets.ReadFile("assets/v1/index.html")
	bootstrap, _ := embeddedShellAssets.ReadFile("assets/v1/bootstrap.html")
	if strings.Contains(string(index), "<script>") || strings.Contains(string(index), " style=") || strings.Contains(string(bootstrap), "<script>") || strings.Contains(string(bootstrap), " style=") {
		t.Fatal("inline code conflicts with shell CSP")
	}
	bootstrapScript, _ := embeddedShellAssets.ReadFile("assets/v1/bootstrap.js")
	for _, required := range []string{"challenge.expires_at", "Date.now() >= expiresAt", "retry.hidden = false"} {
		if !strings.Contains(string(bootstrapScript), required) {
			t.Fatal("bootstrap expiry cannot reach retry state", required)
		}
	}
}

func TestEmbeddedChatPresentationIsBoundedAndXSSSafe(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "localStorage", "sessionStorage"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsafe browser rendering primitive %q is present", forbidden)
		}
	}
	for _, forbidden := range []string{"message.content", "tool_calls", "tool_call_id", `role !== "system"`, `role !== "tool"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("browser consumed non-presentation transcript field %q", forbidden)
		}
	}
	for _, required := range []string{
		"document.createElement", "node.textContent = value", "const pageLimit = 25", "const maxChats = 100",
		"const historyPageLimit = 100", "const maxMessages = 500", "new URLSearchParams", "page.items.length > pageLimit",
		"new EventSource(base +", "source.onerror", "clearAllProvisional()", `payload.durability === "provisional"`,
		`payload.durability !== "committed"`, "payload.revision <= eventRevision", "payload.subject !== selectedChat", "encodeURIComponent(chatID)",
		"source.close()", "source = null", "body.next_cursor", "messageIDs", "lastMessageRevision", `loadHistory(selectedChat, "", true, false)`,
		"body.messages.length > remaining", "body.head_revision !== historyHead", "message.revision > afterRevision",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("chat client lost bounded presentation/reconnect guard %q", required)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := strings.ToLower(string(index))
	for _, forbidden := range []string{"contenteditable", "onsubmit=", "onclick=", "onkeydown="} {
		if strings.Contains(markup, forbidden) {
			t.Fatalf("chat shell exposes unsafe action markup %q", forbidden)
		}
	}
	for _, required := range []string{`id="chat-list-state"`, `id="load-more"`, `id="transcript-state"`, `id="load-more-messages"`, `id="provisional"`,
		`id="composer"`, `id="composer-text"`, `id="send-message"`, `id="steering-text"`, `id="steer-task"`, `id="cancel-task"`,
		`id="feedback-panel"`, `id="approval-dialog"`, `role="dialog"`, `aria-modal="true"`, `id="approval-dialog-scope"`,
		`id="mutation-state"`, `id="reconcile"`, `id="acknowledge-unresolved"`, `id="approval-reconcile"`, `id="approval-acknowledge"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("chat shell lost interaction or presentation state %q", required)
		}
	}
}

func TestEmbeddedInspectorIsBoundedInertAndExplicit(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/inspector.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`requestJSON("/api/v1/models")`, `requestJSON("/api/v1/health")`, `requestJSON("/api/v1/resources")`,
		`+ "/route")`, `+ "/usage")`, `+ "/tools?" + query.toString())`, `+ "/audits?" + query.toString())`,
		"const maxInspectedModels = 256", "const maxRouteCandidates = 256", "const maxHealthChecks = 512", "const maxInspectionItems = 100", "const maxInspectionPages = 8",
		`toolIDs.has(item.call_id)`, `auditIDs.has(item.id)`, `toolCursors.has(body.next_cursor)`, `auditCursors.has(body.next_cursor)`,
		`["Routed total", "routed"]`, `["Auxiliary total", "auxiliary"]`, `["Orchestrator audit (auxiliary)", "orchestrator_audit"]`,
		`"Completion: pending / unknown"`, `Rubric version: " + (item.rubric_version || "Unknown")`,
		`"Evidence precedence: " + (item.evidence_precedence.length ? item.evidence_precedence.join(" → ") : "None")`,
		`body.availability === "unavailable"`, `? "Unknown"`, `node.textContent = value`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("inspector presentation guard missing %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", ".arguments", ".result", ".prompt", "mutate("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("inspector consumes active or raw field %q", forbidden)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	for _, required := range []string{"Arguments and results are not displayed.", "Auxiliary review evidence is not routed model output."} {
		if !strings.Contains(markup, required) {
			t.Fatalf("inspector safety label missing %q", required)
		}
	}
	for _, required := range []string{`id="inspector"`, `id="refresh-inspector"`, `id="health-state"`, `id="resources-state"`, `id="model-list"`,
		`id="task-inspector"`, `id="route-candidates"`, `id="usage-details"`, `id="tool-list"`, `id="load-more-tools"`, `id="audit-list"`, `id="load-more-audits"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("inspector markup missing %q", required)
		}
	}
}

func TestEmbeddedJavaScriptSourcesStayBelowSourceLimit(t *testing.T) {
	for _, name := range []string{"assets/v1/app.js", "assets/v1/operation-contract.js", "assets/v1/inspector.js", "assets/v1/workboards.js", "assets/v1/bootstrap.js"} {
		body, err := embeddedShellAssets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(string(body), "\n") + 1; lines >= 1000 {
			t.Fatalf("%s has %d lines; source files must remain below 1,000", name, lines)
		}
	}
}

func TestEmbeddedWorkboardKanbanIsBoundedInertAndAccessible(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/workboards.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`requestJSON("/api/v1/workboards?" + query.toString())`, `requestJSON("/api/v1/workboards/" + encodeURIComponent(boardID)`,
		"const boardPageLimit = 25, cardPageLimit = 100, dependencyLimit = 100, attemptLimit = 25, maxBoards = 100, maxCards = 10000", "new URLSearchParams", "validCursorTail(value, boardPageLimit)",
		"value.cards.length <= cardPageLimit", "cardTotal + snapshot.cards.length > maxCards", "ids.has(card.id)",
		`credentials: "same-origin"`, `cache: "no-store"`, "node.textContent = text", `kanban.setAttribute("aria-busy", "true")`,
		"No active workboards yet.", "This workboard has no cards yet.", "Use Refresh to try again.", "column.state === states[index]",
		"boardIDs.has(board.id)", "boardCursors.has(page.next_cursor)", "cardCursors.has(snapshot.next_cursor)", "snapshotFence !== fence",
		`toggle.setAttribute("aria-expanded"`, `toggle.setAttribute("aria-label", "Inspect card: "`, "active claim", "dependencies remaining",
		`new EventSource(base + "/api/v1/workboards/"`, `event.lastEventId !== payload.cursor`, `streamFailures >= 8`, `window.clearTimeout(invalidationTimer)`,
		`direction: "prerequisites"`, `direction: "dependents"`, `"/attempts?"`, `validAttemptRecord`, `validAttempt(value.attempt`,
		`"Prerequisites preview"`, `"Attempt history preview"`, `delete target.dataset.loaded`, `checkpoint.created_at`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("workboard client guard missing %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "localStorage", "sessionStorage", "X-Darwin-CSRF", `method: "POST"`, `+ checkpoint.evidence`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("read-only workboard slice contains unsafe primitive %q", forbidden)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	for _, required := range []string{`href="__DARWIN_BASE_PATH__/workboards"`, `id="workboard-view"`, `aria-labelledby="workboard-title"`,
		`id="board-list-state"`, `role="status"`, `id="workboard-state"`, `id="kanban"`, `role="region"`, `aria-label="Workboard lanes"`,
		`id="refresh-workboards"`, `id="load-more-boards"`, `id="load-more-cards"`, `/assets/v1/workboards.js`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("accessible workboard markup missing %q", required)
		}
	}
}

func TestWorkboardRouteDoesNotStartChatOrInspectorRequests(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	guard := strings.Index(body, `const workboardRoute = window.DarwinRoutes.workboards(relativePath)`)
	chat := strings.Index(body, `loadChats(""); checkRecentOperations(); window.DarwinInspector.loadGlobals()`)
	csrf := strings.Index(body, `fetch(base + "/api/v1/session/csrf"`)
	if guard < 0 || !strings.Contains(body[guard:chat], `if (!workboardRoute)`) || chat < guard || csrf < chat {
		t.Fatal("workboard route does not guard unrelated startup requests")
	}
	if !strings.Contains(body, `for (const link of document.querySelectorAll("[data-view]"))`) || !strings.Contains(body, `link.setAttribute("aria-current", "page")`) {
		t.Fatal("workboard navigation cannot expose its current page")
	}
}

func TestWorkboardClientRejectsInvalidDecodedBoardBeforeBoardRequests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `
const asset = process.argv[1];
const urls = [];
let streams = 0;
const node = {hidden:false, disabled:false, textContent:"", dataset:{}, children:[], classList:{toggle(){}},
  addEventListener(){}, setAttribute(){}, replaceChildren(){this.children=[]}, append(value){this.children.push(value)}};
global.document = {body:{dataset:{basePath:"/app"}}, querySelector(){return node}, createElement(){return Object.assign({}, node, {dataset:{}, children:[]})}};
global.window = {location:{pathname:"/app/workboards/%2F"}, DarwinRoutes:undefined, addEventListener(){}, setTimeout, clearTimeout};
global.fetch = url => { urls.push(String(url)); return Promise.reject(new Error("offline")); };
global.EventSource = class { constructor(){ streams++ } close(){} addEventListener(){} };
require(asset);
setImmediate(() => {
  if (streams !== 0 || urls.some(url => url.includes("/workboards/%2F"))) process.exit(1);
});`
	command := exec.Command(node, "-e", script, "./assets/v1/workboards.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("invalid decoded board opened a board request: %v\n%s", err, output)
	}
}

func TestEmbeddedOperationContractCoversWorkboardReconciliation(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/operation-contract.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, action := range []BoardAction{BoardCreate, BoardRevise, BoardArchive, CardCreate, CardRevise, CardMove, CardReorder,
		DependencyAdd, DependencyRemove, CriteriaRevise, AcceptanceAccept, AcceptanceReject, CardPauseRequest, CardCancelRequest} {
		if !strings.Contains(body, `"`+string(action)+`"`) {
			t.Fatal("browser operation contract omitted action", action)
		}
	}
	for _, subject := range []string{"board", "card"} {
		if !strings.Contains(body, `"`+subject+`"`) {
			t.Fatal("browser operation contract omitted subject", subject)
		}
	}
}

func TestEmbeddedChatMutationsAreExplicitFencedAndAccessible(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		"window.crypto.getRandomValues(bytes)", "Object.freeze({...payload, idempotency_key: key})", "Object.freeze({path, body: JSON.stringify(request), key})",
		"if (pendingIntent || unresolvedOperations.length > 0 || !operationsReady || !csrfToken) return", `"X-Darwin-CSRF"`, `credentials: "same-origin"`, `cache: "no-store"`,
		"Outcome unknown. Check current status before taking another action.", "reconcile.hidden = !canReconcile", "response.status >= 500", "pendingIntent.key !== intent.key",
		`action: "submit"`, `action: "resume"`, `action: "steer"`, `action: "cancel"`, `action: "cancel_submission"`,
		`expected_revision: controls.revision`, `action: revise ? "revise" : "record"`, "payload.attempt_cost = cost",
		`attemptCost.value.trim() === "" || !Number.isFinite(cost) || cost < 0`,
		`/controls`, `/feedback`, `/approvals?limit=`, "const maxApprovals = 25", "const maxApprovalPrompt = 16 << 10",
		`approvalDialogScope.textContent = "Scope: " + item.scopeSummary`, `approvalDialogPrompt.textContent = item.prompt`, `item.canDeny ? approvalDeny : approvalClose`, `event.key === "Escape"`,
		`event.key !== "Tab"`, `event.isComposing`, `!event.shiftKey`, "composer.requestSubmit()", `aria-busy`,
		"window.setTimeout", "window.clearTimeout", "selectedControls.canResume",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("chat mutation safety/accessibility guard missing %q", required)
		}
	}
	for _, functionName := range []string{"function loadChats", "function loadHistory", "function connect"} {
		start := strings.Index(body, functionName)
		if start < 0 {
			t.Fatal("missing observation function", functionName)
		}
		rest := body[start+len(functionName):]
		end := strings.Index(rest, "\n\tfunction ")
		if end < 0 {
			end = len(rest)
		}
		if strings.Contains(rest[:end], "mutate(") {
			t.Fatalf("observation function can replay a mutation: %s", functionName)
		}
	}
	if strings.Contains(body, "pendingIntent.body") || strings.Contains(body, "fetch(base + pendingIntent") {
		t.Fatal("ambiguous intent has an automatic replay path")
	}
}

func TestEmbeddedMutationReconciliationIsReadOnlyBoundedAndContractShaped(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`requestJSON("/api/v1/operations?" + query.toString())`, "const maxOperationItems = 25", "const maxOperationScan = 100", `item.state === "committed" && !hasSubject`,
		`const pageLimit = Math.min(maxOperationItems, maxOperationScan - items.length)`, `readOperationPage(body.next_cursor, items, operationIDs, requestID, successMessage)`,
		`if (items.length >= maxOperationScan) throw new Error("operation scan limit reached")`,
		`!["pending", "committed", "rejected"].includes(item.state)`, `const exactResolved = exact && ["committed", "rejected"].includes(exact.state)`,
		`requestJSON("/api/v1/submissions/" + encodeURIComponent(submissionID))`, "const maxSubmissionPolls = 60", "++submissionPollCount < maxSubmissionPolls",
		`unresolvedOperations = items.filter(item => item.state === "pending")`, "Actions remain blocked.", "Acknowledge that this outcome is unresolved without retrying the request?",
		`acknowledgeUnresolved.addEventListener("click", acknowledgeOutcome)`, `approvalAcknowledge.addEventListener("click", acknowledgeOutcome)`,
		`if (approvalResolved && activeApproval)`, "closeApproval();", "loadApprovals();", "The operation was rejected. No request was replayed.",
		`body.current_revision`, `body.retryable`, `body.operation_id`, `body.requested ? "Cancellation requested; submission is still "`,
		`Number.isSafeInteger(item.reference_count)`, `String(item.reference_count)`, `textBytes(item.scope_summary) > maxApprovalScope`,
		`event.key === "Escape" && !pendingIntent && unresolvedOperations.length === 0 && operationsReady`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("mutation reconciliation or contract guard missing %q", required)
		}
	}
	if strings.Contains(body, "body.error") || strings.Contains(body, "pendingIntent.body") || strings.Contains(body, "fetch(base + pendingIntent") {
		t.Fatal("client uses nested error shape or can replay an unresolved mutation")
	}
	feedbackStart := strings.Index(body, "function submitFeedback")
	feedbackEnd := strings.Index(body[feedbackStart:], "\n\tcomposer.addEventListener")
	if feedbackStart < 0 || feedbackEnd < 0 {
		t.Fatal("feedback submission boundary missing")
	}
	feedback := body[feedbackStart : feedbackStart+feedbackEnd]
	reviseStart := strings.Index(feedback, "if (revise)")
	reviseEnd := strings.Index(feedback[reviseStart:], "} else {")
	if reviseStart < 0 || reviseEnd < 0 || strings.Contains(feedback[reviseStart:reviseStart+reviseEnd], "attempt_cost") {
		t.Fatal("feedback revision includes attempt_cost")
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	if !strings.Contains(markup, "Zero is valid; leave it blank when the cost is unknown.") {
		t.Fatal("feedback cost guidance does not distinguish zero from unknown")
	}
	scope := strings.Index(markup, `id="approval-dialog-scope"`)
	allow := strings.Index(markup, `id="approval-allow"`)
	if scope < 0 || allow < 0 || scope > allow {
		t.Fatal("approval scope is not presented before decision actions")
	}
}

func TestEmbeddedChatKeepsConcurrentProvisionalTasksIsolated(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		"const maxProvisionalTasks = 16", "const maxProvisionalTaskText = 256 << 10", "const maxProvisionalText = 1 << 20",
		"const provisionalTasks = new Map()", "provisionalTasks.get(taskID)", "provisionalTasks.set(taskID, item)",
		"maxProvisionalTaskText - item.size", "maxProvisionalText - provisionalTextSize", "node.textContent = value",
		"appendProvisional(payload.data.task_id", "clearProvisionalTask(payload.data.task_id)",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("concurrent provisional isolation guard missing %q", required)
		}
	}
	eventStart := strings.Index(body, "function applyPresentationEvent")
	eventEnd := strings.Index(body[eventStart:], "function connect")
	if eventStart < 0 || eventEnd < 0 {
		t.Fatal("event application boundary missing")
	}
	events := body[eventStart : eventStart+eventEnd]
	if strings.Count(events, "clearProvisionalTask(payload.data.task_id)") != 2 || !strings.Contains(events, `payload.data.state === "completed"`) {
		t.Fatal("final and out-of-order task terminals do not clear only their matching provisional buffers")
	}
	historyStart := strings.Index(body, "function applyHistoryPage")
	historyEnd := strings.Index(body[historyStart:], "function setTaskState")
	if historyStart < 0 || historyEnd < 0 {
		t.Fatal("history reconciliation boundary missing")
	}
	history := body[historyStart : historyStart+historyEnd]
	if strings.Contains(history, "clearProvisionalTask") || strings.Contains(history, "clearAllProvisional") || strings.Contains(history, "provisionalTasks.clear") {
		t.Fatal("history reconciliation erases another active task's provisional buffer")
	}
}

func TestBootstrapHandlerServesOnlyNarrowPublicAssets(t *testing.T) {
	handler, err := NewBootstrapHandler(BootstrapOptions{BasePath: "/console", HostAllowed: func(host string) bool { return host == "darwin.local" }})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/console/bootstrap", "/console/bootstrap/v1/bootstrap.css", "/console/bootstrap/v1/bootstrap.js"} {
		request := httptest.NewRequest(http.MethodGet, "http://darwin.local"+target, nil)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatal("bootstrap asset unavailable", target, response.Code)
		}
	}
	for _, target := range []string{"/console", "/console/assets/v1/app.js", "/console/bootstrap/unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://darwin.local"+target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatal("bootstrap handler exposed application resource", target, response.Code)
		}
	}
}
