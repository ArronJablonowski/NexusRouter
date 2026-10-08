package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChromeModelInventory qualifies the model inventory's compact disclosure
// cards, partial provider accounting, stale-snapshot behavior, recovery, empty
// state, and narrow presentation in a real browser.
func TestChromeModelInventory(t *testing.T) {
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
	var requests atomic.Int32
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/fixture/models-stage/") {
			value, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/fixture/models-stage/"))
			if err != nil || value < 0 || value > 3 {
				http.Error(writer, "invalid fixture stage", 400)
				return
			}
			stage.Store(int32(value))
			return
		}
		if request.URL.Path != "/app/api/v1/models" {
			shell.ServeHTTP(writer, request)
			return
		}
		if cookie, err := request.Cookie("darwin_session"); err != nil || cookie.Value != "valid" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		// Automatic refreshes must not advance the fixture past the stage being asserted.
		switch stage.Load() {
		case 0:
			_, _ = writer.Write([]byte(chromeModelsPopulatedFixture))
		case 1:
			http.Error(writer, "private upstream failure", http.StatusServiceUnavailable)
		case 2:
			_, _ = writer.Write([]byte(chromeModelsRecoveredFixture))
		default:
			_, _ = writer.Write([]byte(chromeModelsEmptyFixture))
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stopChrome := startChromeForTest(t, ctx, chrome)
	defer stopChrome()
	output, err := exec.CommandContext(ctx, node, "-e", chromeModelsCDP, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("real Chrome model inventory qualification failed: %v\n%s", err, output)
	}
	if requests.Load() < 4 {
		t.Fatalf("model inventory did not exercise refresh, recovery, and empty state: %d requests", requests.Load())
	}
}

const chromeModelsPopulatedFixture = `{"version":1,"availability":"available","config_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","refreshed_at":"2026-09-19T12:00:00Z","local_total_bytes":3221225472,"local_total_kind":"logical_deduplicated","local_total_coverage":"partial","local_unknown_size_count":1,"refresh_interval_ms":5000,"local_providers":[{"provider":"ollama-worker","status":"available","status_code":"available","checked_at":"2026-09-19T12:00:00Z"},{"provider":"ollama-backup","status":"unavailable","status_code":"discovery_failed","checked_at":"2026-09-19T12:00:00Z"}],"models":[{"id":"local-muse","provider":"ollama-worker","model":"muse-glimmer:30b","locality":"local","configured":true,"enabled":true,"installed":true,"usable":true,"capabilities":["reasoning"],"health":"healthy","status_code":"available","size_bytes":3221225472,"digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","parameter_size":"30B","quantization":"Q4_K_M","health_checked_at":"2026-09-19T12:00:00Z"},{"id":"local-unknown","provider":"ollama-backup","model":"pending-local","locality":"local","configured":true,"enabled":true,"installed":false,"usable":false,"capabilities":[],"health":"unavailable","status_code":"discovery_failed","health_checked_at":"2026-09-19T12:00:00Z"},{"id":"cloud-coordinator","provider":"openai","model":"gpt-coordinator","locality":"cloud","configured":true,"enabled":true,"installed":false,"usable":true,"capabilities":["reasoning"],"health":"healthy","status_code":"available","health_checked_at":"2026-09-19T12:00:00Z"}]}`

const chromeModelsRecoveredFixture = `{"version":1,"availability":"available","config_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","refreshed_at":"2026-09-19T12:01:00Z","local_total_bytes":4294967296,"local_total_kind":"logical_deduplicated","local_total_coverage":"complete","local_unknown_size_count":0,"refresh_interval_ms":5000,"local_providers":[{"provider":"ollama-worker","status":"available","status_code":"available","checked_at":"2026-09-19T12:01:00Z"}],"models":[{"id":"local-muse","provider":"ollama-worker","model":"muse-glimmer:30b","locality":"local","configured":true,"enabled":true,"installed":true,"usable":true,"capabilities":["reasoning"],"health":"healthy","status_code":"available","size_bytes":4294967296,"digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","parameter_size":"30B","quantization":"Q4_K_M","health_checked_at":"2026-09-19T12:01:00Z"}]}`

const chromeModelsEmptyFixture = `{"version":1,"availability":"available","config_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","refreshed_at":"2026-09-19T12:02:00Z","local_total_bytes":0,"local_total_kind":"logical_deduplicated","local_total_coverage":"complete","local_unknown_size_count":0,"refresh_interval_ms":5000,"local_providers":[],"models":[]}`

const chromeModelsCDP = `
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
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
await cdp('Emulation.setDeviceMetricsOverride', {width:760,height:900,deviceScaleFactor:1,mobile:false});
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'});
if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/models'});
await eventually('document.readyState === "complete" && document.querySelectorAll("#local-model-list .model-card").length === 2 && document.querySelectorAll("#cloud-model-list .model-card").length === 1', 'populated model inventory did not load');
const initial = await evaluate('({total:document.querySelector("#local-model-total").textContent,detail:document.querySelector("#local-model-total-detail").textContent,overflow:document.documentElement.scrollWidth <= document.documentElement.clientWidth,expanded:document.querySelector("#local-model-list .model-card-toggle").getAttribute("aria-expanded")})');
if (!initial.total.includes('3') || !initial.detail.includes('Partial logical total.') || !initial.detail.includes('ollama-backup') || !initial.detail.includes('1 model size is unknown') || !initial.overflow || initial.expanded !== 'false') throw new Error('partial or narrow presentation is incorrect: ' + JSON.stringify(initial));
await evaluate('document.querySelector("#local-model-list .model-card-toggle").click()');
await eventually('document.querySelector("#local-model-list .model-card-toggle").getAttribute("aria-expanded") === "true" && !document.querySelector("#local-model-list .model-details").hidden && document.querySelector("#local-model-list .model-details").textContent.includes("Health checked")', 'compact model card did not disclose details');
await evaluate('fetch("/fixture/models-stage/1")');
await evaluate('document.querySelector("#refresh-models").click()');
await eventually('document.querySelector("#models-live-status").textContent.includes("Showing the last verified snapshot")', 'failed refresh did not preserve and label the verified snapshot');
const stale = await evaluate('({locals:document.querySelectorAll("#local-model-list .model-card").length,clouds:document.querySelectorAll("#cloud-model-list .model-card").length,private:document.body.textContent.includes("private upstream failure")})');
if (stale.locals !== 2 || stale.clouds !== 1 || stale.private) throw new Error('failed refresh lost data or leaked provider failure: ' + JSON.stringify(stale));
await evaluate('fetch("/fixture/models-stage/2")');
await evaluate('document.querySelector("#refresh-models").click()');
await eventually('document.querySelectorAll("#local-model-list .model-card").length === 1 && document.querySelectorAll("#cloud-model-list .model-card").length === 0 && document.querySelector("#local-model-total-detail").textContent.includes("Complete provider-reported logical total.") && !document.querySelector("#models-live-status").classList.contains("error")', 'inventory did not recover from a transient failure');
await evaluate('fetch("/fixture/models-stage/3")');
await evaluate('document.querySelector("#refresh-models").click()');
await eventually('document.querySelectorAll(".model-card").length === 0 && document.querySelector("#local-model-state").textContent.includes("No local models") && document.querySelector("#cloud-model-state").textContent.includes("No cloud models")', 'empty inventory state did not render');
socket.close();
`
