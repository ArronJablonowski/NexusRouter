package webuiapp

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// TestChromeWorkboardConflictReconciliation qualifies mutation failure behavior
// through the authenticated browser BFF, rather than only exercising the
// browser model or HTTP adapter in isolation.
func TestChromeWorkboardConflictReconciliation(t *testing.T) {
	chrome := conflictChromeBinary(t)
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("DARWIN_REQUIRE_CHROME") == "1" {
			t.Fatal("Node with a built-in WebSocket is required for Chrome qualification")
		}
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}
	if output, probeErr := exec.Command(node, "-p", "typeof WebSocket").CombinedOutput(); probeErr != nil || strings.TrimSpace(string(output)) != "function" {
		if os.Getenv("DARWIN_REQUIRE_CHROME") == "1" {
			t.Fatal("Node with a built-in WebSocket is required for Chrome qualification")
		}
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}

	auth, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := auth.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	session, err := auth.Consume(challenge.ID, challenge.Cookie)
	if err != nil {
		t.Fatal(err)
	}

	var phase, mutations, snapshots atomic.Int32
	services := WorkboardServices{
		List: func(context.Context, string, contract.BoardListOptions) (contract.Page, error) {
			return contract.Page{Version: 1, Items: []contract.Board{conflictBoard(phase.Load())}}, nil
		},
		Read: func(_ context.Context, subject, board string, _ contract.BoardSnapshotOptions) (contract.BoardSnapshot, error) {
			if len(subject) != 64 || board != "board-a" {
				t.Fatalf("authenticated read binding changed: subject=%d board=%q", len(subject), board)
			}
			snapshots.Add(1)
			return conflictSnapshot(phase.Load()), nil
		},
		Mutate: func(_ context.Context, subject string, input contract.BoardRequest) (contract.OperationReceipt, error) {
			if len(subject) != 64 {
				t.Fatalf("mutation was not browser-session bound: subject=%d", len(subject))
			}
			switch mutations.Add(1) {
			case 1:
				if input.Action != contract.CardReorder || input.CardID != "card-b" || input.BeforeCardID != "card-a" || input.ExpectedCardRevision == nil || *input.ExpectedCardRevision != 1 {
					t.Fatalf("stale reorder fence changed: %+v", input)
				}
				time.Sleep(300 * time.Millisecond)
				phase.Store(1)
				return contract.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
			case 2:
				if input.Action != contract.DependencyAdd || input.CardID != "card-b" || input.DependencyID != "card-a" || input.ExpectedCardRevision == nil || *input.ExpectedCardRevision != 2 || input.ExpectedGraphRevision == nil || *input.ExpectedGraphRevision != 1 {
					t.Fatalf("cycle request fence changed: %+v", input)
				}
				return contract.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeCycle, Field: "dependency"}
			default:
				t.Fatalf("browser automatically replayed a rejected mutation: %+v", input)
				return contract.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeInvalid}
			}
		},
	}
	operations := MutationServices{Operations: func(_ context.Context, subject, after string, limit int) (contract.OperationPage, error) {
		if len(subject) != 64 || after != "" || limit != 25 {
			t.Fatalf("operation reconciliation binding changed: subject=%d after=%q limit=%d", len(subject), after, limit)
		}
		return contract.OperationPage{Version: 1, Items: []contract.OperationSummary{}}, nil
	}}

	server := httptest.NewUnstartedServer(nil)
	handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{server.Listener.Addr().String()}, Store: auth, Mutations: operations, Workboards: services})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stopChrome := startConflictChrome(t, ctx, chrome)
	defer stopChrome()
	command := exec.CommandContext(ctx, node, "-e", chromeConflictCDP, strconv.Itoa(port), server.URL, session.Token)
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("real Chrome conflict qualification failed: %v\n%s", commandErr, output)
	}
	if mutations.Load() != 2 {
		t.Fatalf("expected exactly one stale write and one cycle attempt, got %d", mutations.Load())
	}
	if snapshots.Load() < 3 {
		t.Fatalf("rejections did not trigger authoritative refetches: %d snapshots", snapshots.Load())
	}
}

func conflictBoard(phase int32) contract.Board {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	revision := int64(1 + phase)
	return contract.Board{Version: 1, ID: "board-a", Revision: revision, LayoutRevision: revision, EventSequence: revision,
		State: "active", Title: "Conflict qualification", Description: "", CardCount: 2, CreatedAt: now, UpdatedAt: now.Add(time.Duration(phase) * time.Second)}
}

func conflictSnapshot(phase int32) contract.BoardSnapshot {
	board := conflictBoard(phase)
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]contract.Column, len(states))
	for index := range states {
		columns[index] = contract.Column{Version: 1, ID: states[index], BoardID: board.ID, State: states[index], Title: titles[index], Rank: string(rune('a' + index))}
	}
	now := board.CreatedAt
	criterion := contract.AcceptanceCriterion{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Focused tests pass.", Required: true}
	card := func(id, rank string, revision int64, dependencies []string, remaining int) contract.Card {
		return contract.Card{Version: 1, ID: id, BoardID: board.ID, Revision: revision, CriteriaRevision: 1, State: "backlog", Rank: rank,
			Title: strings.ToUpper(id), Description: "", Priority: "normal", Labels: []string{}, Dependencies: dependencies, RemainingDependencies: remaining,
			Budget: contract.WorkBudget{AttemptLimit: 1}, Criteria: []contract.AcceptanceCriterion{criterion}, CreatedAt: now, UpdatedAt: now.Add(time.Duration(phase) * time.Second)}
	}
	cards := []contract.Card{card("card-a", "a", 1, []string{"card-b"}, 1), card("card-b", "b", 1, []string{}, 0)}
	if phase == 1 {
		cards = []contract.Card{card("card-b", "a", 2, []string{}, 0), card("card-a", "b", 1, []string{"card-b"}, 1)}
	}
	return contract.BoardSnapshot{Version: 1, Board: board, Columns: columns, Cards: cards, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64)}
}

func conflictChromeBinary(t *testing.T) string {
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
	for _, binary := range []string{"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if info, err := os.Stat(binary); err == nil && !info.IsDir() {
			return binary
		}
	}
	if os.Getenv("DARWIN_REQUIRE_CHROME") == "1" {
		t.Fatal("Chrome or Chrome for Testing is required for Chrome qualification")
	}
	t.Skip("Chrome or Chrome for Testing is not installed")
	return ""
}

func startConflictChrome(t *testing.T, ctx context.Context, binary string) (int, func()) {
	t.Helper()
	profile := t.TempDir()
	command := exec.CommandContext(ctx, binary, "--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-component-update", "--disable-default-apps", "--disable-sync", "--metrics-recording-only", "about:blank")
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
		body, readErr := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if readErr == nil {
			lines := strings.Split(string(body), "\n")
			port, conversionErr := strconv.Atoi(lines[0])
			if conversionErr == nil && port > 0 && port <= 65535 {
				return port, stop
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatal("Chrome did not expose its debugging port within five seconds")
	return 0, func() {}
}

const chromeConflictCDP = `
const port = process.argv[1], origin = process.argv[2], token = process.argv[3];
const targets = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
const target = targets.find(item => item.type === 'page'); if (!target) throw new Error('Chrome did not expose a page target');
const socket = new WebSocket(target.webSocketDebuggerUrl), pending = new Map(), networkResponses = []; let sequence = 0;
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
socket.addEventListener('message', event => { const message = JSON.parse(event.data); if (message.method === 'Network.responseReceived') networkResponses.push({url:message.params.response.url,status:message.params.response.status}); if (!message.id) return; const waiter = pending.get(message.id); if (!waiter) return; pending.delete(message.id); message.error ? waiter.reject(new Error(message.error.message)) : waiter.resolve(message.result); });
function cdp(method, params = {}) { const id = ++sequence; return new Promise((resolve, reject) => { pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params})); }); }
async function evaluate(expression) { const result = await cdp('Runtime.evaluate', {expression, returnByValue:true, awaitPromise:true}); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; }
async function eventually(expression, label) { const deadline = Date.now() + 6000; let last; while (Date.now() < deadline) { try { last = await evaluate(expression); if (last) return; } catch (_) {} await new Promise(resolve => setTimeout(resolve, 20)); } throw new Error(label + ' (last value: ' + JSON.stringify(last) + ')'); }
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
const cookie = await cdp('Network.setCookie', {name:'darwin_browser_session', value:token, url:origin + '/app/'}); if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/workboards/board-a'});
await eventually('document.querySelectorAll(".kanban-card").length === 2 && !document.querySelector(".card-position[data-card-id=card-b][data-position=up]").disabled', 'populated authenticated workboard did not become mutable');
await evaluate('document.querySelector(".card-position[data-card-id=card-b][data-position=up]").click()');
await eventually('!document.querySelector("[data-card-id=card-b] .card-provisional").hidden && document.querySelector("#workboard-mutation-status").textContent.includes("not saved")', 'stale write did not expose its provisional state');
await eventually('document.querySelector("#workboard-mutation-status").textContent.includes("rejected") && document.querySelector("#workboard-mutation-status").textContent.includes("authoritative state was refreshed") && document.querySelector("[data-card-id=card-b] .card-provisional").hidden && document.querySelector("[data-card-id=card-b] .card-meta").textContent.includes("revision 2")', 'conflict did not remove provisional UI and refetch authority');
await eventually('document.querySelectorAll(".kanban-card").length === 2 && document.querySelector("[data-card-id=card-b] .card-meta").textContent.includes("revision 2")', 'authoritative board did not reload after conflict');
await evaluate('document.querySelector("[data-card-id=card-b] .card-toggle").click()');
await eventually('document.querySelector("[data-card-id=card-b]").classList.contains("selected-card") && document.querySelector("[data-card-id=card-b] .card-toggle").getAttribute("aria-pressed") === "true" && !document.querySelector("#open-dependency-change").disabled', 'card B did not become the selected editable card');
const openState = await evaluate('(()=>{const button=document.querySelector("#open-dependency-change");button.click();const context=window.DarwinWorkboards.context();return {hidden:document.querySelector("#dependency-change-dialog").hidden,disabled:button.disabled,selectedCard:context.card&&context.card.id};})()');
if (openState.hidden) throw new Error('dependency editor did not open: ' + JSON.stringify(openState));
const dependencyEditor = await evaluate('(()=>{const context=window.DarwinWorkboards.context();return {modal:document.querySelector("#dependency-change-dialog [role=dialog]").getAttribute("aria-modal"),cardRevision:document.querySelector("#dependency-card-revision").textContent,graphRevision:document.querySelector("#dependency-graph-revision").textContent,options:Array.from(document.querySelector("#dependency-change-id").options).map(option=>option.value),selectedCard:context.card&&context.card.id};})()');
if (dependencyEditor.modal !== 'true' || dependencyEditor.cardRevision !== '2' || dependencyEditor.graphRevision !== '1' || !dependencyEditor.options.includes('card-a') || dependencyEditor.selectedCard !== 'card-b') throw new Error('dependency editor did not offer card A as a prerequisite under the captured fences: ' + JSON.stringify(dependencyEditor));
const cycleResponsesBefore = networkResponses.filter(item => item.url === origin + '/app/api/v1/workboards/board-a/operations' && item.status === 422).length;
const submitted = await evaluate('(()=>{const select=document.querySelector("#dependency-change-id"),button=document.querySelector("#submit-dependency-change");select.value="card-a";select.dispatchEvent(new Event("change",{bubbles:true}));button.click();return {value:select.value,disabled:button.disabled,status:document.querySelector("#dependency-change-form .mutation-form-status").textContent};})()');
if (submitted.value !== 'card-a' || !submitted.status.includes('Submitting')) throw new Error('dependency editor did not submit the selected prerequisite: ' + JSON.stringify(submitted));
await eventually('(()=>{const status=document.querySelector("#workboard-mutation-status");return status.getAttribute("role") === "status" && status.getAttribute("aria-live") === "polite" && status.textContent.includes("rejected") && status.textContent.includes("authoritative state was refreshed");})()', 'cycle rejection and authoritative refresh were not exposed in the accessible workboard status');
await eventually('(()=>{const context=window.DarwinWorkboards.context();return context.card && context.card.id === "card-b" && context.card.revision === 2 && context.card.dependencies.length === 0 && context.graphRevision === 1 && document.querySelector("[data-card-id=card-b] .card-meta").textContent.includes("revision 2");})()', 'authoritative card and graph state were not restored after the cycle rejection');
const responseDeadline = Date.now() + 2000;
while (Date.now() < responseDeadline && networkResponses.filter(item => item.url === origin + '/app/api/v1/workboards/board-a/operations' && item.status === 422).length !== cycleResponsesBefore + 1) await new Promise(resolve => setTimeout(resolve, 20));
const settledCycleResponses = networkResponses.filter(item => item.url === origin + '/app/api/v1/workboards/board-a/operations' && item.status === 422).length;
if (settledCycleResponses !== cycleResponsesBefore + 1) throw new Error('dependency editor did not receive exactly one HTTP 422 response: before=' + cycleResponsesBefore + ' after=' + settledCycleResponses);
await new Promise(resolve => setTimeout(resolve, 400));
socket.close();
`
