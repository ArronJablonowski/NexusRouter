package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestChromeHostileContentRemainsInert qualifies the checked-in presentation
// code against model- and operator-controlled markup in a real browser. The
// payload crosses JSON and DOM boundaries in chat, board, card, criterion, and
// candidate fields without becoming executable HTML. Artifact identities are
// deliberately restricted to inert IDs by the public contract.
func TestChromeHostileContentRemainsInert(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	const hostile = `<img data-darwin-xss src=x onerror="window.__darwinXSS=(window.__darwinXSS||0)+1">`
	now := workboardTime()
	criterion := AcceptanceCriterion{Version: 1, ID: "security", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "browser-security", Description: hostile, Required: true}
	criteria := []AcceptanceCriterion{criterion}
	criteriaDigest := AcceptanceCriteriaDigest(criteria)
	policyDigest, candidateDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	budget := WorkBudget{AttemptLimit: 1, TimeLimitMS: 60_000, TokenLimit: 1_000, CostMicros: 1_000}
	board := Board{Version: 1, ID: "board-xss", Revision: 3, LayoutRevision: 1, EventSequence: 3, State: "active", Title: hostile, Description: hostile, CardCount: 1, CreatedAt: now, UpdatedAt: now}
	card := Card{Version: 1, ID: "card-xss", BoardID: board.ID, Revision: 3, CriteriaRevision: 1, State: "review", Rank: "a1", Title: hostile, Description: hostile, Priority: "high", Labels: []string{"security"}, Dependencies: []string{}, AttemptCount: 1, CurrentAttemptID: "attempt-xss", Budget: budget, Criteria: criteria, CreatedAt: now, UpdatedAt: now}
	candidate := Candidate{Version: 1, ID: "candidate-xss", BoardID: board.ID, CardID: card.ID, AttemptID: card.CurrentAttemptID, Revision: 1, Digest: candidateDigest, CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, Summary: hostile, ArtifactRefs: []string{"artifact-xss"}, SubmittedBy: "worker-xss", CreatedAt: now}
	evidence := []EvidenceRecord{{Version: 1, ID: "evidence-xss", Revision: 1, BoardID: board.ID, CardID: card.ID, AttemptID: card.CurrentAttemptID, CandidateID: candidate.ID, CriterionID: criterion.ID, Source: "deterministic", Outcome: "passed", ActorID: "browser-security", ActorType: "validator", Reference: "security-report", CandidateDigest: candidateDigest, CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, CreatedAt: now}}
	candidate.EvidenceCount, candidate.EvidenceDigest = len(evidence), EvidenceDigest(evidence)
	endedAt := now.Add(time.Second)
	releasedAt := now.Add(time.Second)
	claim := Claim{Version: 1, ID: "claim-xss", BoardID: board.ID, CardID: card.ID, AttemptID: card.CurrentAttemptID, Revision: 2, State: "released", OwnerID: "worker-xss", OwnerType: "worker", TaskID: "task-xss", ExpiresAt: now.Add(time.Minute), LastHeartbeat: now, ReleasedAt: &releasedAt}
	attempt := Attempt{Version: 1, ID: card.CurrentAttemptID, BoardID: board.ID, CardID: card.ID, Ordinal: 1, Revision: 3, State: "review", WorkerID: "worker-xss", CriteriaRevision: 1, CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, Budget: budget, Criteria: criteria, TaskIDs: []string{"task-xss"}, SessionIDs: []string{"chat-xss"}, Claim: &claim, Candidate: &candidate, Evidence: evidence, StartedAt: now, EndedAt: &endedAt}
	snapshot := BoardSnapshot{Version: 1, Board: board, Columns: chromeWorkboardColumns(board.ID), Cards: []Card{card}, Lifecycle: []CardLifecycle{{Version: 1, CardID: card.ID, Attempt: attempt, Checkpoints: []WorkCheckpoint{}, CheckpointCount: 0}}, GraphRevision: 1, GraphDigest: strings.Repeat("c", 64)}
	detail := AttemptDetailPage{Version: 1, Attempt: attempt, Checkpoints: []WorkCheckpoint{}}
	page := Page{Version: 1, Items: []Board{board}}
	history := HistoryPage{Version: 1, ChatID: "chat-xss", TaskID: "task-xss", HeadRevision: 2, Messages: []HistoryMessage{{ID: "message-user", Role: "user", Text: hostile, Revision: 1, SourceRevision: 1}, {ID: "message-assistant", Role: "assistant", Text: hostile, Revision: 2, SourceRevision: 2}}}
	for name, valid := range map[string]bool{"board": board.Validate() == nil, "card": card.Validate() == nil, "snapshot": snapshot.Validate() == nil, "detail": detail.Validate() == nil, "history": history.Validate() == nil} {
		if !valid {
			t.Fatalf("invalid hostile-content fixture: %s", name)
		}
	}

	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(request *http.Request) bool {
		cookie, cookieErr := request.Cookie("darwin_session")
		return cookieErr == nil && cookie.Value == "valid"
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/app/api/") {
			shell.ServeHTTP(writer, request)
			return
		}
		if cookie, cookieErr := request.Cookie("darwin_session"); cookieErr != nil || cookie.Value != "valid" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app/api/v1/session/csrf":
			writeChromeJSON(writer, BrowserSessionResponse{Version: 1, CSRFToken: strings.Repeat("d", BrowserCSRFTokBytes), ExpiresAt: time.Now().Add(time.Hour)})
		case "/app/api/v1/operations":
			_, _ = writer.Write([]byte(`{"version":1,"items":[],"next_cursor":"","has_more":false}`))
		case "/app/api/v1/chats":
			_, _ = writer.Write([]byte(`{"version":1,"items":[{"version":1,"chat_id":"chat-xss","latest_task_id":"task-xss","state":"completed","revision":2,"started_at":"2026-09-13T12:00:00Z"}],"next_cursor":"","has_more":false}`))
		case "/app/api/v1/chats/chat-xss/messages":
			writeChromeJSON(writer, history)
		case "/app/api/v1/chats/chat-xss/events":
			writer.WriteHeader(http.StatusNoContent)
		case "/app/api/v1/workboards":
			writeChromeJSON(writer, page)
		case "/app/api/v1/workboards/board-xss":
			writeChromeJSON(writer, snapshot)
		case "/app/api/v1/workboards/board-xss/events":
			writer.WriteHeader(http.StatusNoContent)
		case "/app/api/v1/workboards/board-xss/cards/card-xss/attempts/attempt-xss":
			writeChromeJSON(writer, detail)
		default:
			http.Error(writer, "unavailable", http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	output, err := exec.CommandContext(ctx, node, "-e", chromeHostileContentCDP, strconv.Itoa(port), server.URL, hostile).CombinedOutput()
	if err != nil {
		t.Fatalf("real Chrome hostile-content qualification failed: %v\n%s", err, output)
	}
}

const chromeHostileContentCDP = `
const port = process.argv[1], origin = process.argv[2], hostile = process.argv[3];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
const target = targets.find(item => item.type === 'page'); if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(); let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data); if (!message.id) return; const waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 5000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (error) { last = error.message; } await new Promise(resolve => setTimeout(resolve, 20)); } const diagnostic = await evaluate('({path:location.pathname,title:document.querySelector("#selected-board-title")?.textContent,state:document.querySelector("#workboard-state")?.textContent,body:document.body?.textContent?.slice(0,500)})'); throw new Error(label + ' (last value: ' + JSON.stringify(last) + ', diagnostic: ' + JSON.stringify(diagnostic) + ')'); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
await cdp('Page.addScriptToEvaluateOnNewDocument', {source:'Object.defineProperty(window, "EventSource", {value:class { addEventListener(){} close(){} }, configurable:false});'});
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'}); if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/chats/chat-xss'});
await eventually('document.querySelectorAll("#transcript .message").length === 2', 'hostile chat messages did not render');
let result = await evaluate('({executed:window.__darwinXSS, injected:document.querySelector("[data-darwin-xss]") !== null, count:Array.from(document.querySelectorAll("#transcript .message p"), node => node.textContent).filter(text => text === ' + JSON.stringify(hostile) + ').length})');
if (result.executed !== undefined || result.injected || result.count !== 2) throw new Error('hostile chat content became active: ' + JSON.stringify(result));
for(const width of [1440,1024,390,319]) {
 await cdp('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false});
 if(!await evaluate('document.documentElement.scrollWidth<=innerWidth+1'))throw Error('chat horizontal overflow at '+width);
 if(!await evaluate('document.querySelector("#transcript").getBoundingClientRect().height>=250'))throw Error('transcript collapsed at '+width);
}
await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false});
await cdp('Page.navigate', {url:origin + '/app/workboards/board-xss'});
await eventually('document.querySelector("[data-card-id=card-xss]") && document.querySelector("#selected-board-title").textContent.length > 0', 'hostile workboard did not render');
await evaluate('document.querySelector("[data-card-id=card-xss] .card-toggle").click()');
await evaluate('document.querySelector("[data-card-id=card-xss] .card-review").click()');
await eventually('document.querySelector("[data-card-id=card-xss] .candidate-review").getAttribute("aria-busy") === "false"', 'hostile candidate detail did not render');
result = await evaluate('({executed:window.__darwinXSS, injected:document.querySelector("[data-darwin-xss]") !== null, occurrences:(document.body.textContent.match(/data-darwin-xss/g)||[]).length})');
if (result.executed !== undefined || result.injected || result.occurrences < 6) throw new Error('hostile workboard content became active or disappeared: ' + JSON.stringify(result));
socket.close();
`
