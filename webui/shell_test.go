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
		{"/console/assets/v1/workboard-client.js", "text/javascript", "DarwinWorkboardClient"},
		{"/console/assets/v1/workboards.js", "text/javascript", "kanban"},
		{"/console/assets/v1/workboard-mutations.js", "text/javascript", "idempotency_key"},
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
	if err != nil || digest != "a045f634aef3d529e5addab4eef36d3be632764d2fe277f5bd9f5b784067fdb3" || ShellAssetVersion != "v1" {
		t.Fatal("embedded shell manifest changed without a versioned review", digest, err)
	}
	for _, name := range []string{"assets/v1/index.html", "assets/v1/app.css", "assets/v1/operation-contract.js", "assets/v1/inspector.js", "assets/v1/workboard-client.js", "assets/v1/workboards.js", "assets/v1/workboard-mutations.js", "assets/v1/app.js", "assets/v1/bootstrap.html", "assets/v1/bootstrap.css", "assets/v1/bootstrap.js"} {
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
	for _, name := range []string{"assets/v1/app.js", "assets/v1/operation-contract.js", "assets/v1/inspector.js", "assets/v1/workboard-client.js", "assets/v1/workboards.js", "assets/v1/workboard-mutations.js", "assets/v1/bootstrap.js"} {
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
		"value.cards.length > cardPageLimit", "cardTotal + snapshot.cards.length > maxCards", "client.canonical(cards, previous",
		`credentials: "same-origin"`, `cache: "no-store"`, "node.textContent = text", `kanban.setAttribute("aria-busy", "true")`,
		`"No " + boardState + " workboards."`, "No cards match the current filters.", "Use Refresh to try again.", "column.state === states[index]",
		"boardIDs.has(board.id)", "boardCursors.has(page.next_cursor)", "cardCursors.has(snapshot.next_cursor)", "snapshotFence !== fence",
		`toggle.setAttribute("aria-expanded"`, `toggle.setAttribute("aria-pressed", "false")`, `toggle.setAttribute("aria-label", "Select and inspect card: "`, "selected-card", "active claim", "dependencies remaining",
		`new EventSource(base + "/api/v1/workboards/"`, `event.lastEventId !== payload.cursor`, `streamFailures >= 8`, `window.clearTimeout(invalidationTimer)`,
		`direction: "prerequisites"`, `direction: "dependents"`, `"/attempts?"`, `validAttemptRecord`, `validAttempt(value.attempt`,
		`"Prerequisites preview"`, `"Attempt history preview"`, `delete target.dataset.loaded`, `checkpoint.created_at`,
		`position.setAttribute("role", "group")`, `button.dataset.position = direction`, `new CustomEvent("darwin:card-position"`, `complete: Boolean(currentBoard && !cardCursor && cardTotal === currentBoard.card_count)`,
		`lifecycleControls.setAttribute("role", "group")`, `button.dataset.control = action`, `new CustomEvent("darwin:card-control"`, `"pause requested · awaiting safe boundary"`, `"paused · worker acknowledged"`, `"resume requested · awaiting worker acknowledgement"`, `"cancel requested"`,
		`"Review candidate"`, `"darwin:acceptance-review"`, `renderCandidateReview`, `"Advisory model audits"`, `"Model-audit evidence is advisory and does not independently authorize acceptance."`,
		`card.current_attempt_id`, `page.attempt.state !== "review"`, `evidenceIDs.has(evidence.id)`, `criterionIDs.has(evidence.criterion_id)`,
		`client.captureFocusAnchor(activeFocus, focusBoardID, cardNodes)`, `client.refreshFocusAnchor(capturedFocus, pendingFocusAnchor, boardID, activeFocus, document.body)`, `pendingFocusVersion === current`, `client.restoreFocusAnchor(focusAnchor, boardID, cardNodes, refresh, document.activeElement, document.body)`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("workboard client guard missing %q", required)
		}
	}
	rendered := strings.Index(body, `target.replaceChildren(element("p", "", "Exact review candidate from attempt "`)
	dispatched := strings.Index(body, `window.dispatchEvent(new CustomEvent("darwin:acceptance-review"`)
	if rendered < 0 || dispatched <= rendered {
		t.Fatal("acceptance review event can precede validated review rendering")
	}
	capturedFocus := strings.Index(body, `const capturedFocus = reset ? client.captureFocusAnchor`)
	if capturedFocus < 0 {
		t.Fatal("authoritative refresh does not capture the focused card control")
	}
	clearedCards := strings.Index(body[capturedFocus:], `clearCardState()`)
	appendedCards := strings.Index(body[capturedFocus:], `appendCards(snapshot.cards, ranks, lifecycle, supervision)`)
	restoredFocus := strings.Index(body[capturedFocus:], `client.restoreFocusAnchor(focusAnchor`)
	if clearedCards < 0 || appendedCards < 0 || restoredFocus <= appendedCards || clearedCards >= appendedCards || strings.Count(body, `client.restoreFocusAnchor(focusAnchor`) != 2 {
		t.Fatal("authoritative refresh does not capture focus before teardown and restore it after success or failure")
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

func TestEmbeddedWorkboardFiltersAndPresentationsAreBoundedAndReadOnly(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/workboards.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`value === "" || value === "unassigned" || idPattern.test(value)`, `["", "unclaimed", "active", "attention"].includes(filters.claim)`,
		`query.set("state", appliedFilters.state)`, `query.set("assignee_id", appliedFilters.assignee)`, `query.set("owner_id", appliedFilters.owner)`, `query.set("claim_state", appliedFilters.claim)`,
		`function clearCardState()`, `cardIDs.clear(); cardCursors.clear(); laneRanks.clear(); lifecycleByCard.clear(); supervisionByCard.clear(); snapshotFence = null`, `loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true)`,
		`loadedCards.push(...cards); renderPresentation()`, `cardList.replaceChildren(); client.reparent(cards, cardNodes`, `const filterSignature = [appliedFilters.state`,
		`filterForm.requestSubmit()`, `aria-invalid`, `presentation = "kanban"`, `presentation = "list"`, `loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true); }, 120)`,
		`client.canonical(cards, previous`, `client.compareText(column.rank, previousRank) > 0`, `snapshot.board.state + " board · "`, `" matching cards loaded · "`, `position controls require all cards loaded and filters clear`,
		`let loadedCards = [], visibleColumns = [], presentation = "kanban", appliedBoardState = "active"`, `const boardState = appliedBoardState`, `appliedBoardState = boardStateFilter.value`,
		`cardNodes.clear()`, `cardNodes.set(cards[index].id, nodes[index])`, `card.state.replace("_", " ") + " state"`, `client.reparent(cards, cardNodes`,
		`Boolean(item.candidate_id) === ["review", "accepted", "rejected"].includes(item.state)`, `Boolean(item.acceptance_id) === ["accepted", "rejected"].includes(item.state)`,
		`candidate && claim && claim.state === "released"`, `"Bounded preview: worker "`, `candidate.evidence_count >= 0`, `validEvidence`,
		`"Lease owner " + claim.owner_id`, `claim.state === "attention" ? " · stale/orphan attention required"`, `" · expires " + claim.expires_at`, `" · heartbeat " + claim.last_heartbeat`,
		`selectedCardAnchor = client.cardViewAnchor(card.board_id, card.id, !expanded)`, `client.cardViewTransition(selectedCardAnchor, focusBoardID, boardID, [], false)`, `restoreCardView(cardView && cardView.card)`,
		`selectedCardAnchor = viewTransition ? viewTransition.anchor : null`, `!filtered && !snapshot.has_more && cardTotal === snapshot.board.card_count`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("workboard filter/presentation guard missing %q", required)
		}
	}
	for _, forbidden := range []string{`.sort(`, `method: "POST"`, "X-Darwin-CSRF", "page.attempt.candidate", "page.attempt.evidence", "page.attempt.decision"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("workboard filtering introduced forbidden behavior %q", forbidden)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	for _, required := range []string{`id="board-state-filter"`, `id="card-state-filter"`, `id="assignee-filter" maxlength="128"`, `id="owner-filter" maxlength="128"`,
		`id="claim-state-filter"`, `id="workboard-filter-status"`, `role="status" aria-live="polite"`, `id="workboard-live-status"`, `id="show-kanban"`, `aria-pressed="true"`,
		`id="show-list"`, `aria-pressed="false"`, `id="workboard-card-list"`, `aria-label="Cards in canonical order"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("accessible workboard filter markup missing %q", required)
		}
	}
	styles, err := embeddedShellAssets.ReadFile("assets/v1/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, required := range []string{`@media (max-width: 78rem)`, `.workboard-layout { grid-template-columns: 1fr; }`, `.workboard-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); }`, `.card-position-controls, .card-lifecycle-controls { display: flex;`, `.card-position-controls button, .card-lifecycle-controls button { min-height: 2.75rem;`} {
		if !strings.Contains(css, required) {
			t.Fatalf("responsive workboard layout missing %q", required)
		}
	}
}

func TestEmbeddedWorkboardMutationScaffoldsAreHiddenBoundedAndAccessible(t *testing.T) {
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	for _, required := range []string{
		`id="workboard-mutation-controls" class="workboard-mutation-controls" aria-label="Workboard actions" hidden`,
		`id="board-create-dialog" class="workboard-dialog-backdrop" hidden`, `id="board-revise-dialog" class="workboard-dialog-backdrop" hidden`,
		`id="board-archive-dialog" class="workboard-dialog-backdrop" hidden`, `id="card-create-dialog" class="workboard-dialog-backdrop" hidden`, `id="card-revise-dialog" class="workboard-dialog-backdrop" hidden`,
		`id="dependency-change-dialog" class="workboard-dialog-backdrop" hidden`, `id="open-dependency-change" class="secondary" type="button"`,
		`role="alertdialog" aria-modal="true" aria-labelledby="board-archive-title"`, `id="board-archive-confirm" type="checkbox" required`, `id="submit-board-archive" class="danger" type="button" disabled`,
		`name="title" required maxlength="256" data-max-bytes="256"`, `name="description" maxlength="65536" data-max-bytes="65536"`,
		`name="board_id" type="hidden"`, `name="card_id" type="hidden"`, `name="expected_board_revision" type="hidden"`, `name="expected_card_revision" type="hidden"`, `name="expected_graph_revision" type="hidden"`,
		`name="parent_id" maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,127}"`, `name="assignee_id" maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,127}"`,
		`name="labels" maxlength="2079" data-max-items="32" data-item-max-bytes="64"`, `name="dependencies" maxlength="8255" data-max-items="64" data-item-max-bytes="128"`,
		`name="budget.attempt_limit" type="number" required min="1" max="32"`, `name="budget.time_limit_ms" type="number" required min="0" max="2592000000"`,
		`name="budget.token_limit" type="number" required min="0" max="1000000000"`, `name="budget.cost_micros" type="number" required min="0" max="1000000000000"`,
		`id="card-create-criteria" class="criteria-fields form-span" data-min-items="1" data-max-items="32" data-max-bytes="65536"`,
		`name="criteria.id" required maxlength="128"`, `name="criteria.kind" required`, `name="criteria.required_source" required`, `name="criteria.validator_id" required maxlength="128"`,
		`name="criteria.description" required maxlength="4096" data-max-bytes="4096"`, `name="criteria.required" type="checkbox"`,
		`name="clear_parent" type="checkbox"`, `name="clear_assignee" type="checkbox"`, `class="mutation-form-status" role="status" aria-live="polite"`,
		`id="dependency-change-mode" name="mode" required`, `id="dependency-change-id" name="dependency_id" required`, `id="dependency-card-revision"`, `id="dependency-graph-revision"`,
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("workboard mutation scaffold missing %q", required)
		}
	}
	for _, form := range []string{`id="board-create-form" class="mutation-form" role="form"`, `id="board-revise-form" class="mutation-form" role="form"`, `id="board-archive-form" class="mutation-form" role="form"`, `id="card-create-form" class="mutation-form mutation-form-grid" role="form"`, `id="card-revise-form" class="mutation-form mutation-form-grid" role="form"`, `id="dependency-change-form" class="mutation-form" role="form"`} {
		if !strings.Contains(markup, form) {
			t.Fatal("missing inert mutation form region", form)
		}
	}
	mutationStart := strings.Index(markup, `id="workboard-mutation-controls"`)
	if mutationStart < 0 {
		t.Fatal("missing mutation scaffold boundary")
	}
	mutationEnd := strings.Index(markup[mutationStart:], `<div id="approval-dialog"`)
	if mutationEnd < 0 || strings.Contains(markup[mutationStart:mutationStart+mutationEnd], `<form`) || strings.Contains(markup[mutationStart:mutationStart+mutationEnd], `type="submit"`) {
		t.Fatal("inert mutation scaffold can submit a browser request")
	}
	styles, err := embeddedShellAssets.ReadFile("assets/v1/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`.workboard-dialog-backdrop[hidden] { display: none; }`, `.workboard-dialog { width: min(38rem, 100%);`, `.mutation-form-grid { grid-template-columns: repeat(2, minmax(0, 1fr));`, `.mutation-form-grid, .budget-fields, .criterion-row { grid-template-columns: 1fr; }`} {
		if !strings.Contains(string(styles), required) {
			t.Fatalf("responsive mutation scaffold styling missing %q", required)
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
window.DarwinWorkboardClient = require("./assets/v1/workboard-client.js");
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

func TestWorkboardClientInvalidActorAndModeSwitchIssueNoRequest(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `
const asset = process.argv[1], urls = [], nodes = new Map();
function make() { return {hidden:false, disabled:false, textContent:"", value:"", dataset:{}, children:[], events:{},
  classList:{toggle(){}}, addEventListener(type, callback){this.events[type]=callback}, setAttribute(){},
  replaceChildren(...values){this.children=values}, append(...values){this.children.push(...values)}, contains(value){return value===this},
  focus(){document.activeElement=this}, requestSubmit(){this.events.submit({preventDefault(){}})}}; }
global.document = {body:{dataset:{basePath:"/app"}}, activeElement:null, querySelector(selector){if(!nodes.has(selector))nodes.set(selector,make());return nodes.get(selector)}, createElement(){return make()}};
nodes.set("#board-state-filter", Object.assign(make(), {value:"active"}));
global.window = {location:{pathname:"/app/workboards"}, DarwinRoutes:undefined, addEventListener(){}, setTimeout, clearTimeout};
window.DarwinWorkboardClient = require("./assets/v1/workboard-client.js");
global.fetch = url => { urls.push(String(url)); return Promise.reject(new Error("offline")); };
global.EventSource = class { close(){} addEventListener(){} };
require(asset);
setImmediate(() => {
  const initial = urls.length, assignee = nodes.get("#assignee-filter"), form = nodes.get("#card-filters");
  assignee.value = "not an id"; form.events.submit({preventDefault(){}});
  if (urls.length !== initial) process.exit(1);
  nodes.get("#show-list").events.click();
  if (urls.length !== initial) process.exit(2);
});`
	command := exec.Command(nodeBinary, "-e", script, "./assets/v1/workboards.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("invalid filter or presentation switch issued a request: %v\n%s", err, output)
	}
}

func TestWorkboardClientModelRejectsStaleAndCrossPageResponsesAndRetainsNodes(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `
const client = require(process.argv[1]);
if (client.current(4, 5, "board-a", "board-a") || client.current(5, 5, "board-a", "board-b") || !client.current(5, 5, "board-a", "board-a")) process.exit(1);
const frozen = client.freezeIntent("/operations", {version:1, nested:{value:"exact"}});
if (!Object.isFrozen(frozen) || !Object.isFrozen(frozen.body) || !Object.isFrozen(frozen.body.nested) || frozen.encoded !== JSON.stringify(frozen.body)) process.exit(5);
if (client.mutationResolution(200, false) !== "ambiguous" || client.mutationResolution(500, false) !== "ambiguous" || client.mutationResolution(408, false) !== "ambiguous" || client.mutationResolution(409, false) !== "definitive" || client.mutationResolution(200, true) !== "committed") process.exit(6);
const capture = {action:"card.revise",boardID:"board-a",boardRevision:2,graphRevision:3,cardID:"card-a",cardRevision:4};
if (!client.captureCurrent(capture,{board:{id:"board-a",revision:2},graphRevision:3,card:{id:"card-a",revision:4}}) || client.captureCurrent(capture,{board:{id:"board-a",revision:3},graphRevision:3,card:{id:"card-a",revision:4}})) process.exit(7);
const receipt = {version:1,board_id:"board-a",operation_id:"domain.operation:01",request_digest:"a".repeat(64),response_digest:"b".repeat(64),first_sequence:8,last_sequence:8,event_count:1,transaction_bytes:128,board_revision:3,outcome:"committed",created_at:"2026-09-09T20:00:00Z"};
const boardIntent = {body:{action:"board.revise",board_id:"board-a"},capture:{boardRevision:2}};
if (!client.receiptMatches(receipt,boardIntent) || client.receiptMatches({...receipt,board_revision:2},boardIntent) || client.receiptMatches({...receipt,operation_id:"short"},boardIntent)) process.exit(8);
const cardIntent = {body:{action:"card.revise",board_id:"board-a",card_id:"card-a"},capture:{boardRevision:2,cardRevision:4}};
if (!client.receiptMatches({...receipt,board_revision:7,card_id:"card-a",card_revision:5},cardIntent) || client.receiptMatches({...receipt,card_id:"card-a",card_revision:4},cardIntent)) process.exit(9);
const positionContext = {board:{id:"board-a",state:"active",revision:2,layout_revision:6},complete:true,unfiltered:true,cards:[
  {id:"card-a",state:"backlog",rank:"a",revision:4,remaining_dependencies:0,current_claim_id:""},
  {id:"card-b",state:"backlog",rank:"b",revision:5,remaining_dependencies:0,current_claim_id:""},
  {id:"card-c",state:"ready",rank:"a",revision:3,remaining_dependencies:0,current_claim_id:""}
]};
const down = client.positionPlan(positionContext,"card-a","down"), up = client.positionPlan(positionContext,"card-b","up"), moved = client.positionPlan(positionContext,"card-a","ready");
if (!down || down.action !== "card.reorder" || down.afterCardID !== "card-b" || !up || up.beforeCardID !== "card-a" || !moved || moved.action !== "card.move" || moved.targetState !== "ready") process.exit(12);
if (client.positionPlan(positionContext,"card-a","up") || client.positionPlan({...positionContext,complete:false},"card-a","down") || client.positionPlan({...positionContext,unfiltered:false},"card-a","ready") || client.positionPlan(positionContext,"card-c","ready")) process.exit(13);
if (!client.captureCurrent(down,positionContext) || client.captureCurrent(down,{...positionContext,board:{...positionContext.board,layout_revision:7}}) || client.captureCurrent(down,{...positionContext,cards:positionContext.cards.map(card=>card.id==="card-b"?{...card,rank:"c"}:card)})) process.exit(14);
const moveIntent = {body:{action:"card.move",board_id:"board-a",card_id:"card-a"},capture:moved};
const moveReceipt = {...receipt,card_id:"card-a",card_revision:5};
if (!client.receiptMatches(moveReceipt,moveIntent) || client.receiptMatches({...moveReceipt,board_revision:4},moveIntent) || client.receiptMatches({...moveReceipt,card_revision:6},moveIntent) || client.receiptMatches({...moveReceipt,claim_revision:1},moveIntent)) process.exit(15);
const dependencyCapture = {action:"dependency.change",boardID:"board-a",boardRevision:2,graphRevision:9,cardID:"card-a",cardRevision:4,dependencies:["card-c"]};
const dependencyContext = {...positionContext,graphRevision:9,card:{id:"card-a",revision:4,dependencies:["card-c"]}};
if (!client.captureCurrent(dependencyCapture,dependencyContext) || client.captureCurrent(dependencyCapture,{...dependencyContext,graphRevision:10}) || client.captureCurrent(dependencyCapture,{...dependencyContext,complete:false}) || client.captureCurrent(dependencyCapture,{...dependencyContext,card:{id:"card-a",revision:4,dependencies:[]}})) process.exit(16);
const dependencyIntent = {body:{action:"dependency.add",board_id:"board-a",card_id:"card-a"},capture:dependencyCapture};
const dependencyReceipt = {...receipt,card_id:"card-a",card_revision:5};
if (!client.receiptMatches(dependencyReceipt,dependencyIntent) || !client.receiptMatches({...dependencyReceipt,board_revision:8},dependencyIntent) || client.receiptMatches({...dependencyReceipt,board_revision:2},dependencyIntent) || client.receiptMatches({...dependencyReceipt,card_revision:6},dependencyIntent) || client.receiptMatches({...dependencyReceipt,claim_revision:1},dependencyIntent)) process.exit(17);
if (client.dependencyPlan(dependencyContext,dependencyCapture,"add","card-b").action !== "dependency.add" || client.dependencyPlan(dependencyContext,dependencyCapture,"remove","card-c").action !== "dependency.remove" || client.dependencyPlan(dependencyContext,dependencyCapture,"add","card-c") || client.dependencyPlan(dependencyContext,dependencyCapture,"remove","card-b") || client.dependencyPlan(dependencyContext,dependencyCapture,"add","card-a")) process.exit(18);
const controlContext = {board:{id:"board-a",state:"active",revision:11},cards:[{id:"card-run",state:"in_progress",rank:"a",revision:4,current_claim_id:"claim-a",pause_requested:false,pause_phase:"",cancel_requested:false,supervision:{state:"running",actions:{pause_request:true,resume_request:false,cancel_request:true}}}]};
const pause = client.controlPlan(controlContext,"card-run","card.pause_request"), cancel = client.controlPlan(controlContext,"card-run","card.cancel_request"), pausedContext = {...controlContext,cards:[{...controlContext.cards[0],pause_requested:true,pause_phase:"acknowledged",supervision:{state:"running",actions:{pause_request:false,resume_request:true,cancel_request:true}}}]}, resume = client.controlPlan(pausedContext,"card-run","card.resume_request");
if (!pause || !cancel || !resume || pause.cardRevision !== 4 || client.controlPlan({...controlContext,board:{...controlContext.board,state:"archived"}},"card-run","card.pause_request") || client.controlPlan(pausedContext,"card-run","card.pause_request") || !client.controlPlan(pausedContext,"card-run","card.cancel_request") || client.controlPlan({...pausedContext,cards:[{...pausedContext.cards[0],pause_phase:"resume_requested"}]},"card-run","card.resume_request")) process.exit(19);
if (!client.captureCurrent(pause,controlContext) || client.captureCurrent(pause,{...controlContext,board:{...controlContext.board,revision:12}}) || client.captureCurrent(pause,{...controlContext,cards:[{...controlContext.cards[0],cancel_requested:true}]})) process.exit(20);
const controlIntent = {body:{action:"card.pause_request",board_id:"board-a",card_id:"card-run"},capture:pause}, controlReceipt = {...receipt,board_revision:12,card_id:"card-run",card_revision:5};
if (!client.receiptMatches(controlReceipt,controlIntent) || !client.receiptMatches({...controlReceipt,board_revision:20},controlIntent) || client.receiptMatches({...controlReceipt,board_revision:11},controlIntent) || client.receiptMatches({...controlReceipt,card_revision:6},controlIntent) || client.receiptMatches({...controlReceipt,claim_revision:2},controlIntent)) process.exit(21);
const reviewCard = {id:"card-review",state:"review",rank:"a",revision:9,criteria_revision:3,remaining_dependencies:0,current_attempt_id:"attempt-a",current_claim_id:"",acceptance_id:""};
const reviewContext = {board:{id:"board-a",state:"active",revision:14},cards:[reviewCard]}, digestA = "a".repeat(64), digestB = "b".repeat(64), digestC = "c".repeat(64), digestD = "d".repeat(64);
const reviewAttempt = {id:"attempt-a",board_id:"board-a",card_id:"card-review",state:"review",criteria_revision:3,criteria_digest:digestB,policy_digest:digestC,criteria:[{id:"tests",kind:"objective",required_source:"deterministic",validator_id:"validator-a",required:true},{id:"taste",kind:"subjective",required_source:"user_feedback",validator_id:"operator-a",required:true}],evidence:[{criterion_id:"tests",source:"deterministic",outcome:"passed",actor_id:"validator-a"}],candidate:{id:"candidate-a",board_id:"board-a",card_id:"card-review",attempt_id:"attempt-a",digest:digestA,criteria_digest:digestB,policy_digest:digestC,evidence_digest:digestD,evidence_count:1}};
const accept = client.acceptancePlan(reviewContext,"card-review",reviewAttempt,"acceptance.accept"), reject = client.acceptancePlan(reviewContext,"card-review",reviewAttempt,"acceptance.reject");
if (!accept || !reject || accept.evidenceHeadRevision !== 1 || accept.evidenceSetDigest !== digestD || !client.captureCurrent(accept,reviewContext) || client.captureCurrent(accept,{...reviewContext,cards:[{...reviewCard,revision:10}]})) process.exit(22);
const subjectiveOnly = {...reviewAttempt,criteria:[reviewAttempt.criteria[1]],evidence:[],candidate:{...reviewAttempt.candidate,evidence_count:0}};
const subjectiveAccept = client.acceptancePlan(reviewContext,"card-review",subjectiveOnly,"acceptance.accept"), subjectiveReject = client.acceptancePlan(reviewContext,"card-review",subjectiveOnly,"acceptance.reject");
if (!subjectiveAccept || !subjectiveReject || subjectiveAccept.evidenceHeadRevision !== 0 || subjectiveAccept.evidenceSetDigest !== digestD) process.exit(26);
if (client.acceptancePlan(reviewContext,"card-review",{...reviewAttempt,evidence:[{...reviewAttempt.evidence[0],outcome:"failed"}]},"acceptance.accept") || !client.acceptancePlan(reviewContext,"card-review",{...reviewAttempt,criteria:reviewAttempt.criteria.slice(0,1),evidence:[{...reviewAttempt.evidence[0],outcome:"failed"}]},"acceptance.reject") || client.acceptancePlan(reviewContext,"card-review",{...reviewAttempt,criteria:reviewAttempt.criteria.slice(0,1)},"acceptance.reject")) process.exit(23);
const subjectiveFailure = {...reviewAttempt,evidence:[...reviewAttempt.evidence,{criterion_id:"taste",source:"user_feedback",outcome:"failed",actor_id:"operator-a"}],candidate:{...reviewAttempt.candidate,evidence_count:2}};
if (client.acceptancePlan(reviewContext,"card-review",subjectiveFailure,"acceptance.accept") || !client.acceptancePlan(reviewContext,"card-review",subjectiveFailure,"acceptance.reject")) process.exit(25);
const acceptanceIntent = {body:{action:"acceptance.accept",board_id:"board-a",card_id:"card-review"},capture:accept}, acceptanceReceipt = {...receipt,last_sequence:10,event_count:3,board_revision:15,card_id:"card-review",card_revision:10};
if (!client.receiptMatches(acceptanceReceipt,acceptanceIntent) || !client.receiptMatches({...acceptanceReceipt,board_revision:20},acceptanceIntent) || client.receiptMatches({...acceptanceReceipt,board_revision:14},acceptanceIntent) || client.receiptMatches({...acceptanceReceipt,card_id:"other-card"},acceptanceIntent) || client.receiptMatches({...acceptanceReceipt,claim_revision:1},acceptanceIntent)) process.exit(24);
const ambiguous = client.mutationError({version:1,code:"workboard_unavailable",message:"Unavailable",retryable:true,operation_id:"op_1234567890123456"},503);
const conflict = client.mutationError({version:1,code:"revision_conflict",message:"Conflict",retryable:true,current_revision:3},409);
if (!ambiguous || ambiguous.definitive || ambiguous.operationID !== "op_1234567890123456" || !conflict || !conflict.definitive || client.mutationError({version:1,code:"workboard_unavailable",message:"\ud800",retryable:true},503)) process.exit(10);
if (client.acknowledgeAllowed({operationID:"",reconciledClean:false},true,false,0) || !client.acknowledgeAllowed({operationID:"",reconciledClean:true},true,false,0) || client.acknowledgeAllowed({operationID:"",reconciledClean:true},true,false,1)) process.exit(11);
const states = ["backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"];
const first = [{id:"card-a", state:"backlog", rank:"a"}], accepted = client.canonical(first, null, [], states);
if (!accepted || client.canonical([{id:"card-b", state:"backlog", rank:"0"}], accepted.last, ["card-a"], states) !== null) process.exit(2);
let requests = 0, focused = null;
function node(name) { return {name, children:[], expanded:false, parent:null, append(child){if(child.parent)child.parent.children=child.parent.children.filter(item=>item!==child);child.parent=this;this.children.push(child)}, contains(value){return value===this || this.children.some(child=>child.contains(value))}, focus(){focused=this}}; }
const card = node("card"), toggle = node("toggle"); card.append(toggle); toggle.expanded = true; focused = toggle;
const nodes = new Map([["card-a", card]]), list = node("list"), lane = node("lane");
if (!client.reparent(first, nodes, () => list, focused) || list.children[0] !== card || !toggle.expanded || focused !== toggle || requests !== 0) process.exit(3);
if (!client.reparent(first, nodes, () => lane, focused) || lane.children[0] !== card || list.children.length !== 0 || !toggle.expanded || focused !== toggle || requests !== 0) process.exit(4);`
	command := exec.Command(nodeBinary, "-e", script, "./assets/v1/workboard-client.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workboard client behavior failed: %v\n%s", err, output)
	}
}

func TestWorkboardClientRestoresAuthoritativeRefreshFocusSafely(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `
const client = require(process.argv[1]);
let focused = null;
function control(kind, action = "") {
  return {kind, action, disabled:false, hidden:false, isConnected:true,
    dataset: kind === "position" ? {position:action} : kind === "control" ? {control:action} : {},
    classList:{contains(name){return name === (kind === "toggle" ? "card-toggle" : kind === "attempt" ? "attempt-toggle" : kind === "review" ? "card-review" : kind === "position" ? "card-position" : kind === "control" ? "card-control" : "")}},
    focus(options){focused={node:this,options}}};
}
function card(...children) {
  const node = {children, isConnected:true,
    contains(value){return value === this || children.includes(value)},
    querySelector(selector){
      if (selector === ".card-toggle") return children.find(item => item.kind === "toggle") || null;
      if (selector === ".card-review") return children.find(item => item.kind === "review") || null;
      const position = selector.match(/^\[data-position="([^"]+)"\]$/);
      if (position) return children.find(item => item.kind === "position" && item.action === position[1]) || null;
      const lifecycle = selector.match(/^\[data-control="([^"]+)"\]$/);
      return lifecycle ? children.find(item => item.kind === "control" && item.action === lifecycle[1]) || null : null;
	}, querySelectorAll(selector){return children.filter(item => selector === ".card-position" ? item.kind === "position" : selector === ".card-control" && item.kind === "control")}};
  for (const child of children) { child.card = node; child.parentNode = node; }
  return node;
}
const body = {isConnected:true,contains(node){return node && node.isConnected === true}}, refresh = control("refresh"), toggle = control("toggle"), pause = control("control", "card.pause_request"), down = control("position", "down"), review = control("review"), attempt = control("attempt");
const cardA = card(toggle, pause, down, review), cards = new Map([["card-a", cardA]]);
const exact = client.captureFocusAnchor(pause, "board-a", cards);
if (!exact || !Object.isFrozen(exact) || exact.boardID !== "board-a" || exact.cardID !== "card-a" || exact.kind !== "control" || exact.action !== "card.pause_request") process.exit(1);
if (!client.restoreFocusAnchor(exact, "board-a", cards, refresh, body, body) || focused.node !== pause || focused.options.preventScroll !== true) process.exit(2);
focused = null; pause.disabled = true;
if (!client.restoreFocusAnchor(exact, "board-a", cards, refresh, null, body) || focused.node !== toggle) process.exit(3);
focused = null; pause.disabled = false; pause.hidden = true;
if (!client.restoreFocusAnchor(exact, "board-a", cards, refresh, body, body) || focused.node !== toggle) process.exit(4);
focused = null;
if (!client.restoreFocusAnchor({...exact, cardID:"card-missing"}, "board-a", cards, refresh, body, body) || focused.node !== refresh) process.exit(5);
focused = null;
for (const invalid of [null, {}, {...exact, boardID:"board-other"}, {...exact, cardID:"bad/card"}, {...exact, kind:"control", action:"card.delete"}]) {
  if (client.restoreFocusAnchor(invalid, "board-a", cards, refresh, body, body) || focused) process.exit(6);
}
const liveFocus = control("unrelated"); focused = null;
if (client.restoreFocusAnchor(exact, "board-a", cards, refresh, liveFocus, body) || focused) process.exit(7);
const foreign = control("control", "card.pause_request");
if (client.captureFocusAnchor(foreign, "board-a", cards) || client.captureFocusAnchor(pause, "bad/board", cards)) process.exit(8);
const bad = control("control", "card.delete"), badCard = card(control("toggle"), bad);
if (client.captureFocusAnchor(bad, "board-a", new Map([["card-b", badCard]]))) process.exit(9);
focused = null; refresh.disabled = true;
if (client.restoreFocusAnchor({...exact, cardID:"card-missing"}, "board-a", cards, refresh, body, body) || focused) process.exit(10);
const attemptCard = card(control("toggle"), attempt), attemptAnchor = client.captureFocusAnchor(attempt, "board-a", new Map([["card-attempt", attemptCard]]));
if (!attemptAnchor || attemptAnchor.kind !== "toggle" || attemptAnchor.action !== "") process.exit(11);
pause.isConnected = false;
const carried = client.refreshFocusAnchor(null, exact, "board-a", pause, body);
if (carried !== exact || client.refreshFocusAnchor(null, exact, "board-other", body, body) || client.refreshFocusAnchor(null, exact, "board-a", refresh, body)) process.exit(12);
const nextToggle = control("toggle"), nextPause = control("control", "card.pause_request"), nextCard = card(nextToggle, nextPause), nextCards = new Map([["card-a", nextCard]]);
refresh.disabled = false; focused = null;
if (!client.restoreFocusAnchor(carried, "board-a", nextCards, refresh, body, body) || focused.node !== nextPause) process.exit(13);`
	command := exec.Command(nodeBinary, "-e", script, "./assets/v1/workboard-client.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workboard authoritative focus restoration failed: %v\n%s", err, output)
	}
}

func TestEmbeddedWorkboardMutationsAreFencedAndNeverReplay(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/workboard-mutations.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`fetch(base + "/api/v1/session/csrf"`, `method: "POST"`, `const limit = Math.min(25, 100 - items.length)`, `items.length >= 100`,
		`window.crypto.getRandomValues(bytes)`, `client.freezeIntent(path, body)`, `const resolution = client.mutationResolution`,
		`!csrfToken || !operationsReady || operationReadFailed || inFlight || Boolean(pendingIntent) || unresolved.length > 0`,
		`new Set([400, 401, 403, 404, 409, 422])`, `response.status === 401 || response.status === 403`,
		`pendingIntent = Object.freeze({...intent, operationID: error && error.operationID || ""})`, `pendingIntent.operationID ? items.find(item => item.id === pendingIntent.operationID)`,
		`The exact request is retained and will not be replayed.`, `Acknowledge this unresolved workboard outcome without retrying the exact request?`,
		`window.DarwinWorkboards.refresh()`, `client.captureCurrent(activeCapture`, `This editor is stale because the authoritative snapshot changed.`,
		`event.key === "Escape"`, `event.key !== "Tab"`, `document.activeElement === first`, `activeOpener`,
		`rows.length < 32`, `rows.length > 32`, `textBytes(JSON.stringify(result)) <= 65536`, `clearParent && parent`, `clearAssignee && assignee`,
		`body.expected_graph_revision = activeCapture.graphRevision`, `client.receiptMatches(body, intent)`, `client.mutationError(body, response.status)`, `client.acknowledgeAllowed(pendingIntent`,
		`action === "board.create" ? "/api/v1/workboards"`, `encodeURIComponent(body.board_id) + "/operations"`,
		`expected_layout_revision: plan.layoutRevision`, `client.positionPlan(context, button.dataset.cardId, direction)`, `window.addEventListener("darwin:card-position"`,
		`exact.subjectType === "card" && exact.subjectID === pendingIntent.body.card_id`, `"dependency.add", "dependency.remove"`,
		`client.dependencyPlan(context, activeCapture`, `expected_graph_revision: activeCapture.graphRevision`, `activeCapture.dependencies.length >= 64`,
		`client.controlPlan(context, button.dataset.cardId, button.dataset.control)`, `window.addEventListener("darwin:card-control"`, `window.confirm(warning)`,
		`Cancellation is not final until verified stop finalization`, `expected_card_revision: plan.cardRevision`,
		`client.acceptancePlan(context, detail.card.id, detail.attempt, "acceptance.accept")`, `window.addEventListener("darwin:acceptance-review"`,
		`expected_card_revision: plan.cardRevision`, `evidence_head_revision: plan.evidenceHeadRevision`, `evidence_set_digest: plan.evidenceSetDigest`,
		`Acceptance committed. The card is Done; dependent cards may have been unlocked.`, `Model-audit evidence informs review but never independently authorizes acceptance.`,
		`shell.inert = true`, `shell.inert = false`, `skipLink.inert = true`, `skipLink.inert = false`, `restoreModalFocus(stable ? null : opener, stableReviewFocus)`, `reviewNotice(unknown, true)`,
		`The exact candidate decision was rejected.`, `required acceptance evidence is missing or failed.`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("workboard mutation safety guard missing %q", required)
		}
	}
	if strings.Count(body, `fetch(base + built.path`) != 1 {
		t.Fatal("workboard intent has an automatic replay path")
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("workboard mutation client uses unsafe primitive %q", forbidden)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(index)
	for _, required := range []string{`/assets/v1/workboard-mutations.js`, `id="candidate-review-dialog"`, `id="candidate-review-rationale"`, `id="candidate-review-confirm"`, `id="candidate-review-accept"`, `id="candidate-review-reject"`, `aria-modal="true"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("candidate decision UI missing %q", required)
		}
	}
}

func TestEmbeddedWorkboardDialogsIsolateBackgroundAndRestoreFocus(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/workboard-mutations.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`function showModal(dialog) { document.body.append(dialog); shell.inert = true; skipLink.inert = true; dialog.hidden = false; }`,
		`function hideModal(dialog) { dialog.hidden = true; shell.inert = false; skipLink.inert = false; }`,
		`function availableModalFocus(node)`,
		`document.body.contains(node)`,
		`depth < 64`,
		`current.hidden || current.inert`,
		`function restoreModalFocus(opener, fallback)`,
		`if (barrier() || activeAction || activeReview) return`,
		`if (barrier() || activeAction || activeReview || !detail`,
		`const dialog = dialogs[action]; showModal(dialog)`,
		`hideModal(dialog); activeAction = ""`,
		`restoreModalFocus(opener, stableReviewFocus)`,
		`restoreModalFocus(stable ? null : opener, stableReviewFocus)`,
		`showModal(reviewDialog)`,
		`hideModal(reviewDialog)`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("workboard modal isolation missing %q", required)
		}
	}
	if strings.Contains(body, `dialogs[action].hidden = false`) {
		t.Fatal("ordinary workboard dialog can open while it remains inside the inert shell")
	}
}

func TestEmbeddedWorkboardModalHelpersBehavior(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `'use strict';
const fs = require('fs'), source = fs.readFileSync(process.argv[1], 'utf8');
function extract(name) {
  const start = source.indexOf('function ' + name + '(');
  if (start < 0) process.exit(20);
  const open = source.indexOf('{', start);
  let depth = 0;
  for (let index = open; index < source.length; index++) {
    if (source[index] === '{') depth++;
    else if (source[index] === '}' && --depth === 0) return source.slice(start, index + 1);
  }
  process.exit(21);
}
const appended = [], body = {hidden:false, inert:false, parentElement:null, append(node){ appended.push(node); node.parentNode = this; node.parentElement = this; }, contains(node){ for (let current = node; current; current = current.parentElement) if (current === this) return true; return false; }}, document = {body}, shell = {inert:false}, skipLink = {inert:false};
const showModal = Function('document', 'shell', 'skipLink', 'return (' + extract('showModal') + ')')(document, shell, skipLink);
const hideModal = Function('shell', 'skipLink', 'return (' + extract('hideModal') + ')')(shell, skipLink);
const availableModalFocus = Function('document', 'return (' + extract('availableModalFocus') + ')')(document);
const restoreModalFocus = Function('availableModalFocus', 'return (' + extract('restoreModalFocus') + ')')(availableModalFocus);
const dialog = {hidden:true, parentNode:null};
showModal(dialog);
if (appended.length !== 1 || appended[0] !== dialog || dialog.parentNode !== document.body || dialog.hidden || !shell.inert || !skipLink.inert) process.exit(1);
hideModal(dialog);
if (!dialog.hidden || shell.inert || skipLink.inert) process.exit(2);
let focused = '';
const control = (name, extra = {}) => ({isConnected:true, disabled:false, hidden:false, inert:false, parentElement:body, focus(){focused = name}, ...extra});
const fallback = control('fallback'), opener = control('opener');
restoreModalFocus(opener, fallback); if (focused !== 'opener') process.exit(3);
focused = ''; restoreModalFocus(control('gone', {isConnected:false}), fallback); if (focused !== 'fallback') process.exit(4);
focused = ''; restoreModalFocus(control('disabled', {disabled:true}), fallback); if (focused !== 'fallback') process.exit(5);
focused = ''; restoreModalFocus(control('hidden', {hidden:true}), fallback); if (focused !== 'fallback') process.exit(6);
focused = ''; restoreModalFocus(null, control('bad-fallback', {disabled:true})); if (focused !== '') process.exit(7);
const hiddenAncestor = {hidden:true, inert:false, parentElement:body};
focused = ''; restoreModalFocus(control('hidden-child', {parentElement:hiddenAncestor}), fallback); if (focused !== 'fallback') process.exit(8);
const inertAncestor = {hidden:false, inert:true, parentElement:body};
focused = ''; restoreModalFocus(control('inert-child', {parentElement:inertAncestor}), fallback); if (focused !== 'fallback') process.exit(9);
focused = ''; restoreModalFocus(control('detached', {parentElement:null}), fallback); if (focused !== 'fallback') process.exit(10);
let deep = body; for (let index = 0; index < 64; index++) deep = {hidden:false, inert:false, parentElement:deep};
focused = ''; restoreModalFocus(control('too-deep', {parentElement:deep}), fallback); if (focused !== 'fallback') process.exit(11);`
	command := exec.Command(nodeBinary, "-e", script, "./assets/v1/workboard-mutations.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workboard modal helper behavior failed: %v\n%s", err, output)
	}
}

func TestEmbeddedOperationContractCoversWorkboardReconciliation(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/operation-contract.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, action := range []BoardAction{BoardCreate, BoardRevise, BoardArchive, CardCreate, CardRevise, CardMove, CardReorder,
		DependencyAdd, DependencyRemove, CriteriaRevise, AcceptanceAccept, AcceptanceReject, CardPauseRequest, CardResumeRequest, CardCancelRequest} {
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
