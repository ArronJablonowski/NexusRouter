package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestChromeWorkboardCRUD drives the checked-in controls in an authenticated
// Chrome session. The fixture is deliberately stateful: each validated receipt
// advances the authoritative read model, so every DOM assertion after a write
// also proves that the UI refetched rather than trusting its mutation response.
func TestChromeWorkboardCRUD(t *testing.T) {
	chrome := chromeTestBinary(t)
	node := chromeTestNode(t)
	fixture := newChromeWorkboardCRUDFixture()
	shell, err := NewShellHandler(ShellOptions{
		BasePath:    "/app",
		HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") },
		Authenticated: func(request *http.Request) bool {
			cookie, cookieErr := request.Cookie("darwin_session")
			return cookieErr == nil && cookie.Value == "valid"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/app/api/") {
			shell.ServeHTTP(writer, request)
			return
		}
		fixture.serve(writer, request)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	output, err := exec.CommandContext(ctx, node, "-e", chromeWorkboardCRUDCDP, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		fixture.mu.Lock()
		state := "stage=" + strconv.Itoa(fixture.stage) + " violation=" + fixture.violation + " pages=" + strconv.Itoa(fixture.pageReads[fixture.stage]) + " snapshots=" + strconv.Itoa(fixture.snapshotReads[fixture.stage])
		fixture.mu.Unlock()
		t.Fatalf("real Chrome workboard CRUD qualification failed (%s): %v\n%s", state, err, output)
	}
	fixture.assert(t)
}

type chromeWorkboardCRUDFixture struct {
	mu            sync.Mutex
	stage         int
	requests      []BoardRequest
	csrfRequests  int
	operationGets int
	pageReads     map[int]int
	snapshotReads map[int]int
	violation     string
	now           time.Time
}

func newChromeWorkboardCRUDFixture() *chromeWorkboardCRUDFixture {
	return &chromeWorkboardCRUDFixture{
		pageReads: make(map[int]int), snapshotReads: make(map[int]int),
		now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
}

func (f *chromeWorkboardCRUDFixture) serve(writer http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie("darwin_session"); err != nil || cookie.Value != "valid" {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/app/api/v1/session/csrf":
		f.serveCSRF(writer, request)
	case "/app/api/v1/operations":
		f.serveOperations(writer, request)
	case "/app/api/v1/workboards":
		if request.Method == http.MethodPost {
			f.serveMutation(writer, request)
		} else {
			f.serveBoards(writer, request)
		}
	case "/app/api/v1/workboards/board-dar83":
		f.serveSnapshot(writer, request)
	case "/app/api/v1/workboards/board-dar83/operations":
		f.serveMutation(writer, request)
	default:
		http.Error(writer, "unavailable", http.StatusNotFound)
	}
}

func (f *chromeWorkboardCRUDFixture) fail(writer http.ResponseWriter, message string) {
	f.mu.Lock()
	if f.violation == "" {
		f.violation = message
	}
	f.mu.Unlock()
	http.Error(writer, message, http.StatusBadRequest)
}

func (f *chromeWorkboardCRUDFixture) serveCSRF(writer http.ResponseWriter, request *http.Request) {
	var input BrowserCSRFRequest
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || request.ContentLength < 1 || request.ContentLength > 64 ||
		decodeBoundedChromeJSON(writer, request, 64, &input) != nil || input.Validate() != nil {
		f.fail(writer, "invalid csrf request")
		return
	}
	f.mu.Lock()
	f.csrfRequests++
	f.mu.Unlock()
	writeChromeJSON(writer, BrowserSessionResponse{Version: 1, CSRFToken: strings.Repeat("c", BrowserCSRFTokBytes), ExpiresAt: time.Now().Add(time.Hour)})
}

func (f *chromeWorkboardCRUDFixture) serveOperations(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "25" || len(request.URL.Query()) != 1 {
		f.fail(writer, "invalid operation scan")
		return
	}
	f.mu.Lock()
	f.operationGets++
	f.mu.Unlock()
	_, _ = writer.Write([]byte(`{"version":1,"items":[],"next_cursor":"","has_more":false}`))
}

func (f *chromeWorkboardCRUDFixture) serveBoards(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "25" || request.URL.Query().Get("state") != "active" || len(request.URL.Query()) != 2 {
		f.fail(writer, "invalid board query")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageReads[f.stage]++
	page := Page{Version: 1, Items: []Board{}}
	if f.stage > 0 && f.stage < 7 {
		page.Items = []Board{f.boardLocked()}
	}
	writeChromeJSON(writer, page)
}

func (f *chromeWorkboardCRUDFixture) serveSnapshot(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "100" || len(request.URL.Query()) != 1 {
		f.fail(writer, "invalid snapshot query")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stage < 1 {
		f.violation = "snapshot requested before board creation"
		http.Error(writer, f.violation, http.StatusBadRequest)
		return
	}
	f.snapshotReads[f.stage]++
	writeChromeJSON(writer, f.snapshotLocked())
}

func (f *chromeWorkboardCRUDFixture) serveMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || request.Header.Get("X-Darwin-CSRF") != strings.Repeat("c", BrowserCSRFTokBytes) || request.ContentLength < 1 || request.ContentLength > 64<<10 {
		f.fail(writer, "invalid authenticated mutation envelope")
		return
	}
	var input BoardRequest
	if decodeBoundedChromeJSON(writer, request, 64<<10, &input) != nil || input.Validate() != nil {
		f.fail(writer, "invalid bounded mutation body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.stage + 1
	if next > 7 || input.Action != chromeCRUDAction(next) || (next == 1) != (request.URL.Path == "/app/api/v1/workboards") {
		if f.violation == "" {
			f.violation = "duplicate or out-of-order mutation: " + string(input.Action)
		}
		http.Error(writer, f.violation, http.StatusBadRequest)
		return
	}
	for _, prior := range f.requests {
		if prior.IdempotencyKey == input.IdempotencyKey {
			f.violation = "duplicate idempotency key"
			http.Error(writer, f.violation, http.StatusBadRequest)
			return
		}
	}
	f.requests = append(f.requests, input)
	f.stage = next
	cardRevision := int64(0)
	receipt := OperationReceipt{
		Version: 1, BoardID: "board-dar83", OperationID: input.IdempotencyKey,
		RequestDigest: strings.Repeat("d", 64), ResponseDigest: strings.Repeat("e", 64),
		FirstSequence: int64(next), LastSequence: int64(next), EventCount: 1,
		TransactionBytes: 512, BoardRevision: int64(next), Outcome: "committed", CreatedAt: f.now.Add(time.Duration(next) * time.Minute),
	}
	if next >= 3 && next <= 6 {
		cardRevision = int64(next - 2)
		receipt.CardID, receipt.CardRevision = "card-dar83", &cardRevision
	}
	writeChromeJSON(writer, receipt)
}

func chromeCRUDAction(stage int) BoardAction {
	return []BoardAction{"", BoardCreate, BoardRevise, CardCreate, CardRevise, CardMove, CardMove, BoardArchive}[stage]
}

func (f *chromeWorkboardCRUDFixture) boardLocked() Board {
	title := "DAR-83 browser board"
	if f.stage >= 2 {
		title = "DAR-83 browser board edited"
	}
	state, count := "active", 0
	if f.stage >= 3 {
		count = 1
	}
	if f.stage >= 7 {
		state = "archived"
	}
	layout := int64(1)
	if f.stage >= 3 {
		layout = 2
	}
	if f.stage >= 5 {
		layout = int64(f.stage - 2)
	}
	return Board{Version: 1, ID: "board-dar83", Revision: int64(f.stage), LayoutRevision: layout, EventSequence: int64(f.stage), State: state, Title: title, Description: "Chrome-authenticated fixture", CardCount: count, CreatedAt: f.now, UpdatedAt: f.now.Add(time.Duration(f.stage) * time.Minute)}
}

func (f *chromeWorkboardCRUDFixture) snapshotLocked() BoardSnapshot {
	snapshot := BoardSnapshot{Version: 1, Board: f.boardLocked(), Columns: chromeWorkboardColumns("board-dar83"), Cards: []Card{}, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64)}
	if f.stage < 3 {
		return snapshot
	}
	title, priority, state, rank := "Created in Chrome", "high", "backlog", "a1"
	if f.stage >= 4 {
		title, priority = "Edited in Chrome", "urgent"
	}
	if f.stage == 5 {
		state, rank = "ready", "b1"
	}
	snapshot.GraphRevision = 2
	snapshot.Cards = []Card{{
		Version: 1, ID: "card-dar83", BoardID: "board-dar83", Revision: int64(f.stage - 2), CriteriaRevision: 1,
		State: state, Rank: rank, Title: title, Description: "Created through the real form", Priority: priority,
		Labels: []string{"browser", "crud"}, Dependencies: []string{}, Budget: WorkBudget{AttemptLimit: 2, TimeLimitMS: 60000, TokenLimit: 1000, CostMicros: 2500},
		Criteria:  []AcceptanceCriterion{{Version: 1, ID: "chrome-proof", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "browser-test", Description: "Chrome flow passes", Required: true}},
		CreatedAt: f.now.Add(3 * time.Minute), UpdatedAt: f.now.Add(time.Duration(f.stage) * time.Minute),
	}}
	return snapshot
}

func (f *chromeWorkboardCRUDFixture) assert(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.violation != "" {
		t.Fatal(f.violation)
	}
	// Opening the board link is a full authenticated document navigation, so
	// each of the two documents rotates CSRF and scans its operation barrier once.
	if f.stage != 7 || len(f.requests) != 7 || f.csrfRequests != 2 || f.operationGets != 2 {
		t.Fatalf("unexpected request counts: stage=%d mutations=%d csrf=%d operation_scans=%d", f.stage, len(f.requests), f.csrfRequests, f.operationGets)
	}
	for stage := 1; stage <= 7; stage++ {
		if f.pageReads[stage] < 1 {
			t.Fatalf("mutation stage %d was not authoritatively refetched in the board list", stage)
		}
		if stage >= 2 && f.snapshotReads[stage] < 1 {
			t.Fatalf("mutation stage %d was not authoritatively refetched as a snapshot", stage)
		}
	}
	assertChromeCRUDRequests(t, f.requests)
}

func assertChromeCRUDRequests(t *testing.T, requests []BoardRequest) {
	t.Helper()
	text := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	revision := func(value *int64) int64 {
		if value == nil {
			return 0
		}
		return *value
	}
	if got := requests[0]; got.Action != BoardCreate || text(got.Title) != "DAR-83 browser board" || text(got.Description) != "Chrome-authenticated fixture" {
		t.Fatalf("unexpected board.create request: %+v", got)
	}
	if got := requests[1]; got.Action != BoardRevise || got.BoardID != "board-dar83" || revision(got.ExpectedBoardRevision) != 1 || text(got.Title) != "DAR-83 browser board edited" || got.Description != nil {
		t.Fatalf("unexpected board.revise action or fence: %+v", got)
	}
	if got := requests[2]; got.Action != CardCreate || got.BoardID != "board-dar83" || revision(got.ExpectedBoardRevision) != 2 || revision(got.ExpectedGraphRevision) != 1 || text(got.Title) != "Created in Chrome" || got.Priority != "high" || got.Budget == nil || got.Budget.AttemptLimit != 2 || len(got.Criteria) != 1 || got.Criteria[0].ID != "chrome-proof" || strings.Join(got.Labels, ",") != "browser,crud" {
		t.Fatalf("unexpected card.create action or fences: %+v", got)
	}
	if got := requests[3]; got.Action != CardRevise || got.BoardID != "board-dar83" || got.CardID != "card-dar83" || revision(got.ExpectedCardRevision) != 1 || got.ExpectedGraphRevision != nil || text(got.Title) != "Edited in Chrome" || got.Priority != "urgent" {
		t.Fatalf("unexpected card.revise action or fence: %+v", got)
	}
	for index, expected := range []struct {
		board, layout, card int64
		target              string
	}{{4, 2, 2, "ready"}, {5, 3, 3, "backlog"}} {
		got := requests[index+4]
		if got.Action != CardMove || got.BoardID != "board-dar83" || got.CardID != "card-dar83" || got.TargetState != expected.target || revision(got.ExpectedBoardRevision) != expected.board || revision(got.ExpectedLayoutRevision) != expected.layout || revision(got.ExpectedCardRevision) != expected.card {
			t.Fatalf("unexpected card.move action or fences: %+v", got)
		}
	}
	if got := requests[6]; got.Action != BoardArchive || got.BoardID != "board-dar83" || revision(got.ExpectedBoardRevision) != 6 {
		t.Fatalf("unexpected board.archive action or fence: %+v", got)
	}
}

const chromeWorkboardCRUDCDP = `
const port = process.argv[1], origin = process.argv[2];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json(), target = targets.find(item => item.type === 'page');
if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(); let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data), waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 7000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (_) {} await new Promise(resolve => setTimeout(resolve, 20)); } throw new Error(label + ' (last value: ' + JSON.stringify(last) + ')'); }
async function setValue(selector, value) { await evaluate("(() => { const node = document.querySelector(" + JSON.stringify(selector) + "); node.value = " + JSON.stringify(value) + "; node.dispatchEvent(new Event('input', {bubbles:true})); node.dispatchEvent(new Event('change', {bubbles:true})); })()"); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'}); if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/workboards'});
await eventually('document.readyState === "complete" && document.querySelector("#workboard-mutation-status").textContent === "Workboard actions are ready." && !document.querySelector("#open-board-create").disabled', 'authenticated mutation controls did not become ready');
await evaluate('document.querySelector("#open-board-create").click()'); await setValue('#board-create-name', 'DAR-83 browser board'); await setValue('#board-create-description', 'Chrome-authenticated fixture'); await evaluate('document.querySelector("#submit-board-create").click()');
await eventually('document.querySelector("a[href=\\"/app/workboards/board-dar83\\"]") && document.body.textContent.includes("DAR-83 browser board")', 'board.create was not authoritatively refetched');
await evaluate('document.querySelector("a[href=\\"/app/workboards/board-dar83\\"]").click()');
await eventually('document.querySelector("#selected-board-title").textContent === "DAR-83 browser board" && document.querySelector("#kanban").getAttribute("aria-busy") === "false"', 'created board snapshot did not load');
await evaluate('document.querySelector("#open-board-revise").click()'); await setValue('#board-revise-name', 'DAR-83 browser board edited'); await evaluate('document.querySelector("#submit-board-revise").click()');
await eventually('document.querySelector("#selected-board-title").textContent === "DAR-83 browser board edited" && document.querySelector("#selected-board-meta").textContent.includes("active board")', 'board.revise was not authoritatively refetched');
await evaluate('document.querySelector("#open-card-create").click()');
await setValue('#card-create-name', 'Created in Chrome'); await setValue('#card-create-description', 'Created through the real form'); await setValue('#card-create-priority', 'high'); await setValue('#card-create-labels', 'browser\ncrud'); await setValue('#card-create-attempt-limit', '2'); await setValue('#card-create-time-limit', '60000'); await setValue('#card-create-token-limit', '1000'); await setValue('#card-create-cost-limit', '2500');
await setValue('#card-create-criteria [name="criteria.id"]', 'chrome-proof'); await setValue('#card-create-criteria [name="criteria.validator_id"]', 'browser-test'); await setValue('#card-create-criteria [name="criteria.description"]', 'Chrome flow passes'); await evaluate('document.querySelector("#card-create-criteria [name=\\"criteria.required\\"]").click(); document.querySelector("#submit-card-create").click()');
await eventually('document.querySelector("[data-card-id=card-dar83]") && document.querySelector("#lane-backlog .count").textContent === "1" && document.querySelector("[data-card-id=card-dar83]").textContent.includes("revision 1")', 'card.create was not authoritatively refetched into Backlog');
await evaluate('document.querySelector("[data-card-id=card-dar83] .card-toggle").click()');
await eventually('!document.querySelector("#open-card-revise").disabled', 'created card could not be selected for editing');
await evaluate('document.querySelector("#open-card-revise").click()'); await setValue('#card-revise-name', 'Edited in Chrome'); await setValue('#card-revise-priority', 'urgent'); await evaluate('document.querySelector("#submit-card-revise").click()');
await eventually('document.querySelector("[data-card-id=card-dar83]").textContent.includes("Edited in Chrome") && document.querySelector("[data-card-id=card-dar83]").textContent.includes("revision 2")', 'card.revise was not authoritatively refetched');
await eventually('!document.querySelector("[data-card-id=card-dar83] [data-position=ready]").disabled', 'Backlog to Ready control did not become available'); await evaluate('document.querySelector("[data-card-id=card-dar83] [data-position=ready]").click()');
await eventually('document.querySelector("#lane-ready .count").textContent === "1" && document.querySelector("#lane-backlog .count").textContent === "0" && document.querySelector("[data-card-id=card-dar83]").textContent.includes("revision 3") && !document.querySelector("[data-card-id=card-dar83]").classList.contains("provisional-position")', 'Backlog to Ready transition was not authoritatively refetched');
await eventually('!document.querySelector("[data-card-id=card-dar83] [data-position=backlog]").disabled', 'Ready to Backlog control did not become available'); await evaluate('document.querySelector("[data-card-id=card-dar83] [data-position=backlog]").click()');
await eventually('document.querySelector("#lane-backlog .count").textContent === "1" && document.querySelector("#lane-ready .count").textContent === "0" && document.querySelector("[data-card-id=card-dar83]").textContent.includes("revision 4") && !document.querySelector("[data-card-id=card-dar83]").classList.contains("provisional-position")', 'Ready to Backlog transition was not authoritatively refetched');
await eventually('!document.querySelector("#open-board-archive").disabled', 'archive control did not become available'); await evaluate('document.querySelector("#open-board-archive").click(); document.querySelector("#board-archive-confirm").click(); document.querySelector("#submit-board-archive").click()');
await eventually('document.querySelector("#selected-board-meta").textContent.includes("archived board") && document.querySelector("#selected-board-title").textContent === "DAR-83 browser board edited" && document.querySelector("#open-board-archive").disabled', 'board.archive was not authoritatively refetched');
socket.close();
`
