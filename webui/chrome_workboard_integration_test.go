package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChromeWorkboardNavigation qualifies the checked-in shell in a real
// browser without introducing a browser automation dependency. Node's built-in
// WebSocket speaks the small subset of CDP needed for navigation and keyboard
// input; the application and API fixture remain same-origin and authenticated.
func TestChromeWorkboardNavigation(t *testing.T) {
	chrome := chromeTestBinary(t)
	node := chromeTestNode(t)

	shell, err := NewShellHandler(ShellOptions{
		BasePath:    "/app",
		HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") },
		Authenticated: func(request *http.Request) bool {
			cookie, err := request.Cookie("darwin_session")
			return err == nil && cookie.Value == "valid"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshots atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/app/api/") {
			if cookie, err := request.Cookie("darwin_session"); err != nil || cookie.Value != "valid" {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			switch request.URL.Path {
			case "/app/api/v1/workboards":
				_, _ = writer.Write([]byte(workboardPageFixture))
			case "/app/api/v1/workboards/board-a":
				snapshots.Add(1)
				time.Sleep(350 * time.Millisecond)
				_, _ = writer.Write([]byte(workboardSnapshotFixture))
			case "/app/api/v1/workboards/board-a/events":
				writer.WriteHeader(http.StatusNoContent)
			default:
				http.Error(writer, "unavailable", http.StatusNotFound)
			}
			return
		}
		shell.ServeHTTP(writer, request)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	command := exec.CommandContext(ctx, node, "-e", chromeWorkboardCDP, strconv.Itoa(port), server.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real Chrome workboard qualification failed: %v\n%s", err, output)
	}
	if snapshots.Load() < 2 {
		t.Fatalf("keyboard refresh did not fetch a second authoritative snapshot: %d requests", snapshots.Load())
	}
}

// TestChromePopulatedWorkboard qualifies the populated presentation and the
// optimistic card-position path in a real browser. The fixture intentionally
// holds the mutation response long enough for Chrome to expose the provisional
// position, then advances the same-origin snapshot to the committed order.
func TestChromePopulatedWorkboard(t *testing.T) {
	chrome := chromeTestBinary(t)
	node := chromeTestNode(t)

	initialPage, initialSnapshot := populatedChromeWorkboardFixture(false)
	committedPage, committedSnapshot := populatedChromeWorkboardFixture(true)
	var committed atomic.Bool
	var snapshots atomic.Int32
	var mutations atomic.Int32
	requests := make(chan BoardRequest, 1)

	shell, err := NewShellHandler(ShellOptions{
		BasePath:    "/app",
		HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") },
		Authenticated: func(request *http.Request) bool {
			cookie, err := request.Cookie("darwin_session")
			return err == nil && cookie.Value == "valid"
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
		if cookie, err := request.Cookie("darwin_session"); err != nil || cookie.Value != "valid" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app/api/v1/session/csrf":
			if request.Method != http.MethodPost || request.URL.RawQuery != "" || request.ContentLength < 1 || request.ContentLength > 64 {
				http.Error(writer, "invalid csrf request", http.StatusBadRequest)
				return
			}
			var input BrowserCSRFRequest
			if err := decodeBoundedChromeJSON(writer, request, 64, &input); err != nil || input.Validate() != nil {
				http.Error(writer, "invalid csrf request", http.StatusBadRequest)
				return
			}
			writeChromeJSON(writer, BrowserSessionResponse{Version: 1, CSRFToken: strings.Repeat("c", BrowserCSRFTokBytes), ExpiresAt: time.Now().Add(time.Hour)})
		case "/app/api/v1/operations":
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "25" || len(request.URL.Query()) != 1 {
				http.Error(writer, "invalid operation query", http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"version":1,"items":[],"next_cursor":"","has_more":false}`))
		case "/app/api/v1/workboards":
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "25" || request.URL.Query().Get("state") != "active" || len(request.URL.Query()) != 2 {
				http.Error(writer, "invalid board query", http.StatusBadRequest)
				return
			}
			if committed.Load() {
				writeChromeJSON(writer, committedPage)
			} else {
				writeChromeJSON(writer, initialPage)
			}
		case "/app/api/v1/workboards/board-a":
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "100" || len(request.URL.Query()) != 1 {
				http.Error(writer, "invalid snapshot query", http.StatusBadRequest)
				return
			}
			snapshots.Add(1)
			if committed.Load() {
				writeChromeJSON(writer, committedSnapshot)
			} else {
				writeChromeJSON(writer, initialSnapshot)
			}
		case "/app/api/v1/workboards/board-a/events":
			if request.Method != http.MethodGet || request.URL.Query().Get("after") == "" || len(request.URL.Query()) != 2 || request.URL.Query().Get("limit") != "100" {
				http.Error(writer, "invalid event query", http.StatusBadRequest)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		case "/app/api/v1/workboards/board-a/operations":
			if request.Method != http.MethodPost || request.URL.RawQuery != "" || request.Header.Get("X-Darwin-CSRF") != strings.Repeat("c", BrowserCSRFTokBytes) || request.ContentLength < 1 || request.ContentLength > 64<<10 || mutations.Add(1) != 1 {
				http.Error(writer, "invalid mutation request", http.StatusBadRequest)
				return
			}
			var input BoardRequest
			if err := decodeBoundedChromeJSON(writer, request, 64<<10, &input); err != nil || input.Validate() != nil {
				http.Error(writer, "invalid mutation request", http.StatusBadRequest)
				return
			}
			requests <- input
			time.Sleep(600 * time.Millisecond)
			committed.Store(true)
			cardRevision := int64(4)
			writeChromeJSON(writer, OperationReceipt{Version: 1, BoardID: "board-a", OperationID: input.IdempotencyKey, RequestDigest: strings.Repeat("d", 64), ResponseDigest: strings.Repeat("e", 64), FirstSequence: 12, LastSequence: 12, EventCount: 1, TransactionBytes: 512, BoardRevision: 8, CardID: "card-ready-a", CardRevision: &cardRevision, Outcome: "committed", CreatedAt: workboardTime()})
		default:
			http.Error(writer, "unavailable", http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	output, err := exec.CommandContext(ctx, node, "-e", chromePopulatedWorkboardCDP, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("real Chrome populated workboard qualification failed: %v\n%s", err, output)
	}
	select {
	case input := <-requests:
		if input.Action != CardReorder || input.BoardID != "board-a" || input.CardID != "card-ready-a" || input.AfterCardID != "card-ready-b" || input.BeforeCardID != "" || input.ExpectedBoardRevision == nil || *input.ExpectedBoardRevision != 7 || input.ExpectedLayoutRevision == nil || *input.ExpectedLayoutRevision != 5 || input.ExpectedCardRevision == nil || *input.ExpectedCardRevision != 3 {
			t.Fatalf("unexpected bounded reorder request: %+v", input)
		}
	default:
		t.Fatal("browser did not submit the card reorder request")
	}
	if snapshots.Load() < 2 || mutations.Load() != 1 {
		t.Fatalf("authoritative reconciliation requests: snapshots=%d mutations=%d", snapshots.Load(), mutations.Load())
	}
}

func chromeTestNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}
	if output, probeErr := exec.Command(node, "-p", "typeof WebSocket").CombinedOutput(); probeErr != nil || strings.TrimSpace(string(output)) != "function" {
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}
	return node
}

func decodeBoundedChromeJSON(writer http.ResponseWriter, request *http.Request, limit int64, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrContract
	}
	return nil
}

func writeChromeJSON(writer http.ResponseWriter, value any) {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(err)
	}
}

func populatedChromeWorkboardFixture(committed bool) (Page, BoardSnapshot) {
	now := workboardTime()
	board := Board{Version: 1, ID: "board-a", Revision: 7, LayoutRevision: 5, EventSequence: 11, State: "active", Title: "Populated browser board", Description: "Real browser fixture", CardCount: 3, ActiveClaims: 1, CreatedAt: now, UpdatedAt: now}
	objective := AcceptanceCriterion{Version: 1, ID: "checks", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "validator-ci", Description: "Automated checks pass", Required: true}
	subjective := AcceptanceCriterion{Version: 1, ID: "rollout", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator-review", Description: "Operator approves rollout", Required: true}
	budget := WorkBudget{AttemptLimit: 3, TimeLimitMS: 3_600_000, TokenLimit: 50_000, CostMicros: 1_000_000}
	readyA := Card{Version: 1, ID: "card-ready-a", BoardID: board.ID, Revision: 3, CriteriaRevision: 1, State: "ready", Rank: "a1", Title: "Prepare release", Description: "First ready item.", Priority: "high", Labels: []string{"release"}, Dependencies: []string{}, AssigneeID: "worker-b", Budget: budget, Criteria: []AcceptanceCriterion{objective}, CreatedAt: now, UpdatedAt: now}
	readyB := Card{Version: 1, ID: "card-ready-b", BoardID: board.ID, Revision: 2, CriteriaRevision: 1, State: "ready", Rank: "a2", Title: "Publish release", Description: "Second ready item.", Priority: "normal", Labels: []string{"release"}, Dependencies: []string{}, Budget: budget, Criteria: []AcceptanceCriterion{objective}, CreatedAt: now, UpdatedAt: now}
	blocked := Card{Version: 1, ID: "card-blocked", BoardID: board.ID, Revision: 6, CriteriaRevision: 2, State: "blocked", Rank: "d1", Title: "Deploy production", Description: "Awaiting an operator decision.", Priority: "urgent", Labels: []string{"production", "attention"}, Dependencies: []string{"card-ready-a"}, RemainingDependencies: 1, AssigneeID: "worker-a", AttemptCount: 1, CurrentAttemptID: "attempt-blocked", CurrentClaimID: "claim-blocked", BlockReason: "operator_input", Budget: budget, Criteria: []AcceptanceCriterion{objective, subjective}, CreatedAt: now, UpdatedAt: now}
	claim := Claim{Version: 1, ID: blocked.CurrentClaimID, BoardID: board.ID, CardID: blocked.ID, AttemptID: blocked.CurrentAttemptID, Revision: 4, State: "attention", OwnerID: "worker-a", OwnerType: "worker", TaskID: "task-deploy", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
	criteriaDigest := AcceptanceCriteriaDigest(blocked.Criteria)
	attempt := Attempt{Version: 1, ID: blocked.CurrentAttemptID, BoardID: board.ID, CardID: blocked.ID, Ordinal: 1, Revision: 4, State: "running", WorkerID: "worker-a", CriteriaRevision: blocked.CriteriaRevision, CriteriaDigest: criteriaDigest, PolicyDigest: strings.Repeat("b", 64), Budget: budget, Criteria: blocked.Criteria, TaskIDs: []string{"task-deploy"}, SessionIDs: []string{"session-deploy"}, Claim: &claim, Evidence: []EvidenceRecord{}, StartedAt: now}
	lifecycle := CardLifecycle{Version: 1, CardID: blocked.ID, Attempt: attempt, Checkpoints: []WorkCheckpoint{}, CheckpointCount: 0}
	if committed {
		board.Revision, board.LayoutRevision, board.EventSequence = 8, 6, 12
		board.UpdatedAt = now.Add(time.Minute)
		readyA.Revision, readyA.Rank, readyA.UpdatedAt = 4, "a3", now.Add(time.Minute)
	}
	cards := []Card{readyA, readyB, blocked}
	if committed {
		cards[0], cards[1] = cards[1], cards[0]
	}
	page := Page{Version: 1, Items: []Board{board}}
	snapshot := BoardSnapshot{Version: 1, Board: board, Columns: chromeWorkboardColumns(board.ID), Cards: cards, Lifecycle: []CardLifecycle{lifecycle}, GraphRevision: 3, GraphDigest: strings.Repeat("a", 64)}
	if page.Validate() != nil || snapshot.Validate() != nil {
		panic("invalid populated Chrome workboard fixture")
	}
	return page, snapshot
}

func chromeWorkboardColumns(boardID string) []Column {
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]Column, len(states))
	for index := range states {
		columns[index] = Column{Version: 1, ID: states[index], BoardID: boardID, State: states[index], Title: titles[index], Rank: string(rune('a' + index))}
	}
	return columns
}

func chromeTestBinary(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("DARWIN_CHROME"); configured != "" {
		if filepath.IsAbs(configured) {
			return configured
		}
		t.Fatal("DARWIN_CHROME must be an absolute path")
	}
	for _, name := range []string{"chrome", "google-chrome", "chromium", "chromium-browser"} {
		if binary, err := exec.LookPath(name); err == nil {
			return binary
		}
	}
	for _, binary := range []string{
		"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if info, err := os.Stat(binary); err == nil && !info.IsDir() {
			return binary
		}
	}
	t.Skip("Chrome or Chrome for Testing is not installed")
	return ""
}

func startChromeForTest(t *testing.T, ctx context.Context, binary string) (int, func()) {
	t.Helper()
	profile := t.TempDir()
	command := exec.CommandContext(ctx, binary,
		"--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-component-update", "--disable-default-apps",
		"--disable-sync", "--metrics-recording-only", "about:blank")
	if err := command.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}
	t.Cleanup(stop)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			lines := strings.Split(string(body), "\n")
			port, conversionErr := strconv.Atoi(lines[0])
			if conversionErr == nil && port > 0 && port <= 65535 {
				return port, stop
			}
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatal("Chrome exited before exposing its debugging port")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatal("Chrome did not expose a debugging port within five seconds")
	return 0, func() {}
}

const workboardPageFixture = `{"version":1,"items":[{"version":1,"id":"board-a","title":"Browser qualification","description":"","state":"active","revision":1,"layout_revision":1,"event_sequence":1,"card_count":0,"active_claims":0,"created_at":"2026-09-10T12:00:00Z","updated_at":"2026-09-10T12:00:00Z"}],"has_more":false}`

var workboardSnapshotFixture = func() string {
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]map[string]any, len(states))
	for index, state := range states {
		columns[index] = map[string]any{"version": 1, "board_id": "board-a", "id": state, "state": state, "title": titles[index], "rank": string(rune('a' + index))}
	}
	page := map[string]any{}
	if err := json.Unmarshal([]byte(workboardPageFixture), &page); err != nil {
		panic(err)
	}
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 {
		panic("invalid browser workboard fixture")
	}
	snapshot := map[string]any{"version": 1, "board": items[0], "columns": columns, "cards": []any{}, "has_more": false, "graph_revision": 1, "graph_digest": strings.Repeat("a", 64)}
	body, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	return string(body)
}()

const chromeWorkboardCDP = `
const port = process.argv[1], origin = process.argv[2];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
const target = targets.find(item => item.type === 'page');
if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(); let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data); if (!message.id) return; const waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 5000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (_) {} await new Promise(resolve => setTimeout(resolve, 20)); } throw new Error(label + ' (last value: ' + JSON.stringify(last) + ')'); }
async function enter() { const common = {key:'Enter', code:'Enter', windowsVirtualKeyCode:13, nativeVirtualKeyCode:13}; await cdp('Input.dispatchKeyEvent', {type:'rawKeyDown', ...common}); await cdp('Input.dispatchKeyEvent', {type:'char', text:'\r', unmodifiedText:'\r', ...common}); await cdp('Input.dispatchKeyEvent', {type:'keyUp', ...common}); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'});
if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/chats'});
await eventually('document.readyState === "complete" && document.querySelector(\'[data-view="workboards"]\')', 'chat shell did not load');
await evaluate('document.querySelector(\'[data-view="workboards"]\').focus(); document.activeElement.dataset.view === "workboards"');
await enter();
await eventually('location.pathname === "/app/workboards" && document.querySelector("#workboard-view").hidden === false && document.querySelector(\'[data-view="workboards"]\').getAttribute("aria-current") === "page"', 'keyboard workboard navigation failed');
await eventually('document.querySelector(\'a[href="/app/workboards/board-a"]\')', 'accessible board link did not render');
await evaluate('document.querySelector(\'a[href="/app/workboards/board-a"]\').focus()'); await enter();
await eventually('location.pathname === "/app/workboards/board-a" && document.querySelector("#workboard-state").textContent === "Loading cards and lanes…" && document.querySelector("#kanban").getAttribute("aria-busy") === "true"', 'direct workboard loading state was not exposed');
await eventually('document.querySelector("#selected-board-title").textContent === "Browser qualification" && document.querySelector("#workboard-state").textContent === "No cards match the current filters." && document.querySelector("#workboard-state").getAttribute("role") === "status" && document.querySelector("#kanban").getAttribute("aria-label") === "Workboard lanes" && document.querySelector("#kanban").getAttribute("aria-busy") === "false"', 'accessible empty workboard did not render');
await evaluate('document.querySelector("#refresh-workboards").focus()'); await enter();
await eventually('performance.getEntriesByName(location.origin + "/app/api/v1/workboards/board-a?limit=100").length >= 2', 'Enter did not activate workboard refresh');
await eventually('document.querySelector("#workboard-state").textContent === "No cards match the current filters." && document.querySelector("#kanban").getAttribute("aria-busy") === "false" && document.activeElement.id === "refresh-workboards"', 'refresh did not restore stable keyboard focus');
socket.close();
`

const chromePopulatedWorkboardCDP = `
const port = process.argv[1], origin = process.argv[2];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
const target = targets.find(item => item.type === 'page');
if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(); let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data); if (!message.id) return; const waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 5000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (_) {} await new Promise(resolve => setTimeout(resolve, 20)); } throw new Error(label + ' (last value: ' + JSON.stringify(last) + ')'); }
async function enter() { const common = {key:'Enter', code:'Enter', windowsVirtualKeyCode:13, nativeVirtualKeyCode:13}; await cdp('Input.dispatchKeyEvent', {type:'rawKeyDown', ...common}); await cdp('Input.dispatchKeyEvent', {type:'char', text:'\r', unmodifiedText:'\r', ...common}); await cdp('Input.dispatchKeyEvent', {type:'keyUp', ...common}); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'});
if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/workboards/board-a'});
await eventually('document.readyState === "complete" && document.querySelector("#selected-board-title").textContent === "Populated browser board" && document.querySelector("#kanban").getAttribute("aria-busy") === "false"', 'populated workboard did not load');
await eventually('document.querySelector("#workboard-mutation-status").textContent === "Workboard actions are ready." && !document.querySelector("[data-card-id=card-ready-a] [data-position=down]").disabled', 'authenticated position controls did not become ready');
const initial = await evaluate('(() => { const ids = selector => Array.from(document.querySelectorAll(selector), node => node.dataset.cardId); const blocked = document.querySelector("[data-card-id=card-blocked]"); return {ready:ids("ol[aria-label=\\"Ready cards\\"] > li"),blocked:ids("ol[aria-label=\\"Blocked cards\\"] > li"),readyCount:document.querySelector("#lane-ready .count").textContent,blockedCount:document.querySelector("#lane-blocked .count").textContent,blockedText:blocked.textContent,blockerAlert:Array.from(blocked.querySelectorAll(".card-alert"), node => node.textContent)};})()');
if (JSON.stringify(initial.ready) !== JSON.stringify(['card-ready-a','card-ready-b']) || JSON.stringify(initial.blocked) !== JSON.stringify(['card-blocked']) || initial.readyCount !== '2' || initial.blockedCount !== '1') throw new Error('cards were not rendered in canonical lanes: ' + JSON.stringify(initial));
for (const expected of ['blocked state','1 dependencies remaining','blocked: operator_input','Criteria revision 2 · 2 required of 2','Attempt 1 · running · criteria revision 2','Claim owner worker-a · attention','stale/orphan attention required','Required: Automated checks pass','Required: Operator approves rollout']) if (!initial.blockedText.includes(expected)) throw new Error('missing populated card metadata: ' + expected);
if (!initial.blockerAlert.some(value => value.includes('blocked: operator_input')) || !initial.blockerAlert.some(value => value.includes('stale/orphan claim attention'))) throw new Error('blocker or lease attention was not emphasized');
await evaluate('document.querySelector("#show-list").click()');
await eventually('document.querySelector("#workboard-card-list").hidden === false && document.querySelector("#kanban").hidden === true', 'list presentation switch failed');
const listed = await evaluate('Array.from(document.querySelectorAll("#workboard-card-list > li"), node => node.dataset.cardId)');
if (JSON.stringify(listed) !== JSON.stringify(['card-ready-a','card-ready-b','card-blocked'])) throw new Error('list presentation lost canonical card order: ' + JSON.stringify(listed));
await evaluate('document.querySelector("#show-kanban").click()');
await eventually('document.querySelector("#kanban").hidden === false && document.querySelector("#workboard-card-list").hidden === true', 'kanban presentation switch failed');
await evaluate('document.querySelector("[data-card-id=card-ready-a] [data-position=down]").focus()'); await enter();
await eventually('(() => { const ready = Array.from(document.querySelectorAll("ol[aria-label=\\"Ready cards\\"] > li"), node => node.dataset.cardId); const card = document.querySelector("[data-card-id=card-ready-a]"); return JSON.stringify(ready) === JSON.stringify(["card-ready-b","card-ready-a"]) && card.classList.contains("provisional-position") && !card.querySelector(".card-provisional").hidden && document.querySelector("#workboard-mutation-status").textContent.includes("Pending position preview");})()', 'keyboard reorder did not expose provisional state');
await eventually('(() => { const ready = Array.from(document.querySelectorAll("ol[aria-label=\\"Ready cards\\"] > li"), node => node.dataset.cardId); const card = document.querySelector("[data-card-id=card-ready-a]"); return JSON.stringify(ready) === JSON.stringify(["card-ready-b","card-ready-a"]) && !card.classList.contains("provisional-position") && card.querySelector(".card-provisional").hidden && card.textContent.includes("revision 4") && document.querySelector("#kanban").getAttribute("aria-busy") === "false";})()', 'authoritative snapshot did not reconcile the provisional reorder');
socket.close();
`
