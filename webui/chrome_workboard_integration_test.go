package webui

import (
	"context"
	"encoding/json"
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
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}
	if output, probeErr := exec.Command(node, "-p", "typeof WebSocket").CombinedOutput(); probeErr != nil || strings.TrimSpace(string(output)) != "function" {
		t.Skip("Node with a built-in WebSocket is required for Chrome qualification")
	}

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
async function enter() { const common = {key:'Enter', code:'Enter', windowsVirtualKeyCode:13, nativeVirtualKeyCode:13}; await cdp('Input.dispatchKeyEvent', {type:'keyDown', ...common}); await cdp('Input.dispatchKeyEvent', {type:'keyUp', ...common}); }
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
