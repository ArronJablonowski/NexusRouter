package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real Chrome loads the production embedded Settings assets. Only the API
// responses are fixtures: no network discovery, SSH, pairing or inference runs.
func TestChromeRemoteDiscoveryPairingHandoff(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(r *http.Request) bool { c, e := r.Cookie("darwin_session"); return e == nil && c.Value == "valid" }})
	if err != nil {
		t.Fatal(err)
	}
	var scans, pairs atomic.Int32
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/api/") {
			shell.ServeHTTP(w, r)
			return
		}
		c, e := r.Cookie("darwin_session")
		if e != nil || c.Value != "valid" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture-token"})
		case "/app/api/v1/remote-membership":
			if r.Method == "POST" {
				if r.Header.Get("X-Darwin-CSRF") != "fixture-token" {
					http.Error(w, "csrf", 403)
					return
				}
				var input map[string]any
				if decodeBoundedChromeJSON(w, r, 16384, &input) != nil {
					http.Error(w, "invalid", 400)
					return
				}
				if pairs.Add(1) != 1 {
					http.Error(w, "duplicate", 409)
					return
				}
				captured <- input
			}
			writeChromeJSON(w, map[string]any{"version": 1, "enabled": true, "discovery_enabled": true, "digest": strings.Repeat("a", 64), "registry": map[string]any{"version": 1, "peers": []any{}}})
		case "/app/api/v1/remote-discovery":
			if r.Method != "POST" || r.Header.Get("X-Darwin-CSRF") != "fixture-token" {
				http.Error(w, "authority", 403)
				return
			}
			var input struct {
				Version int `json:"version"`
			}
			if decodeBoundedChromeJSON(w, r, 1024, &input) != nil || input.Version != 1 {
				http.Error(w, "invalid", 400)
				return
			}
			if scans.Add(1) > 1 {
				http.Error(w, "private discovery failure", 503)
				return
			}
			time.Sleep(100 * time.Millisecond)
			writeChromeJSON(w, map[string]any{"version": 1, "candidates": []any{map[string]any{"version": 1, "verified": false, "instance": "node-a", "endpoint": "https://192.168.1.20:8443", "server_name": "node-a.local", "claimed_certificate_sha256": strings.Repeat("b", 64), "ssh_port": 2222, "observed_at": time.Now().UTC(), "expires_at": time.Now().Add(30 * time.Second).UTC()}}})
		default:
			http.Error(w, "unavailable", 404)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	out, err := exec.CommandContext(ctx, node, "-e", chromeRemoteDiscoveryCDP, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("Chrome discovery qualification: %v\n%s", err, out)
	}
	if scans.Load() != 2 || pairs.Load() != 1 {
		t.Fatal("unexpected requests", scans.Load(), pairs.Load())
	}
	select {
	case input := <-captured:
		peer, ok := input["peer"].(map[string]any)
		if !ok || input["identity_verified"] != true || input["expected_digest"] != strings.Repeat("a", 64) || peer["transport"] != "ssh" || peer["id"] != "node-a" {
			t.Fatal(input)
		}
		ssh, ok := peer["ssh"].(map[string]any)
		if !ok || ssh["port"] != float64(2222) || ssh["user"] != "fixture-user" {
			t.Fatal(peer)
		}
		b, _ := json.Marshal(peer["operations"])
		if string(b) != `["info"]` || peer["allow_cloud_inference"] != false {
			t.Fatal("pair scopes", peer)
		}
	default:
		t.Fatal("missing pairing fixture request")
	}
}

const chromeRemoteDiscoveryCDP = `
const port = process.argv[1], origin = process.argv[2];
const firefox=process.env.NEXUS_TEST_BROWSER==='firefox';
const target=firefox?{webSocketDebuggerUrl:'ws://127.0.0.1:'+port+'/session'}:(await (await fetch('http://127.0.0.1:'+port+'/json/list')).json()).find(item=>item.type==='page');
if(!target)throw Error('Browser did not expose a page target');
const socket=new WebSocket(target.webSocketDebuggerUrl),pending=new Map();let sequence=0,context;
await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
socket.addEventListener('message',event=>{const m=JSON.parse(event.data),w=pending.get(m.id);if(!w)return;pending.delete(m.id);m.error?w.reject(Error(m.message||m.error.message||m.error)):w.resolve(m.result);});
function protocol(method,params={}){const id=++sequence;return new Promise((resolve,reject)=>{pending.set(id,{resolve,reject});socket.send(JSON.stringify({id,method,params}));});}
if(firefox){await protocol('session.new',{capabilities:{alwaysMatch:{acceptInsecureCerts:false}}});context=(await protocol('browsingContext.create',{type:'tab',background:false})).context;await protocol('browsingContext.activate',{context});}
async function cdp(method,p={}){
 if(!firefox)return protocol(method,p);
 // CDP domain enable calls only register observation; the BiDi session is already active.
 if(['Runtime.enable','Page.enable','Network.enable','DOM.enable'].includes(method))return {};
 if(method==='Page.navigate')return protocol('browsingContext.navigate',{context,url:p.url,wait:'complete'});
 if(method==='Page.reload'){if(p.ignoreCache)await protocol('network.setCacheBehavior',{cacheBehavior:'bypass',contexts:[context]});try{return await protocol('browsingContext.reload',{context,wait:'complete'});}finally{if(p.ignoreCache)await protocol('network.setCacheBehavior',{cacheBehavior:'default',contexts:[context]});}}
 if(method==='Runtime.evaluate'){
  const r=await protocol('script.evaluate',{target:{context},expression:'(async()=>JSON.stringify(await (0,eval)('+JSON.stringify(p.expression)+')))()',awaitPromise:true});
  if(r.type==='exception')return {exceptionDetails:{text:r.exceptionDetails.text}};
  return {result:{value:r.result.type==='string'?JSON.parse(r.result.value):undefined}};
 }
 if(method==='Emulation.setDeviceMetricsOverride')return protocol('browsingContext.setViewport',{context,viewport:{width:p.width,height:p.height},devicePixelRatio:p.deviceScaleFactor||1});
 if(method==='Emulation.setEmulatedMedia'){if(!await evaluate('matchMedia("(prefers-reduced-motion: reduce)").matches'))throw Error('Firefox reduced-motion preference missing');return {};}
 if(method==='Network.setCookie'){const u=new URL(p.url);await protocol('storage.setCookie',{cookie:{name:p.name,value:{type:'string',value:p.value},domain:u.hostname,path:'/',httpOnly:!!p.httpOnly,secure:u.protocol==='https:',sameSite:'strict'}});return {success:true};}
 if(method==='Page.addScriptToEvaluateOnNewDocument')return protocol('script.addPreloadScript',{functionDeclaration:'()=>{'+p.source+'}'});
 if(method==='Page.captureScreenshot')return protocol('browsingContext.captureScreenshot',{context,origin:'viewport',format:{type:'image/png'}});
 if(method==='Browser.setDownloadBehavior')return protocol('browser.setDownloadBehavior',{downloadBehavior:{type:'allowed',destinationFolder:p.downloadPath}});
 if(method==='Input.dispatchKeyEvent'){
  if(p.type==='char')return {}; // BiDi keyDown already generates the text input event.
  await protocol('browsingContext.activate',{context});
  if(p.key==='Tab'&&p.type==='keyDown'){
   // Tab probes begin in page content, not Firefox's URL bar after a previous traversal.
   await protocol('input.performActions',{context,actions:[{type:'pointer',id:'focus-page',parameters:{pointerType:'mouse'},actions:[{type:'pointerMove',x:1,y:1},{type:'pointerDown',button:0},{type:'pointerUp',button:0}]}]});
  }
  const keys={Tab:'\uE004',Enter:'\uE007',Escape:'\uE00C'};return protocol('input.performActions',{context,actions:[{type:'key',id:'keyboard',actions:[{type:p.type==='keyUp'?'keyUp':'keyDown',value:keys[p.key]||p.key}]}]});}
 if(method==='DOM.getDocument')return {root:{nodeId:'document'}};
 if(method==='DOM.querySelector'){const r=await protocol('browsingContext.locateNodes',{context,locator:{type:'css',value:p.selector},maxNodeCount:1});if(!r.nodes.length)throw Error('Missing file picker');return {nodeId:r.nodes[0].sharedId};}
 if(method==='DOM.setFileInputFiles')return protocol('input.setFiles',{context,element:{sharedId:p.nodeId},files:p.files});
 throw Error('Unimplemented Firefox test command '+method);
}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(r.exceptionDetails.text);return r.result.value;}
async function eventually(expression,label){const deadline=Date.now()+5000;let last;while(Date.now()<deadline){try{last=await evaluate(expression);if(last)return;}catch(_){}await new Promise(r=>setTimeout(r,20));}throw Error(label+' (last value: '+JSON.stringify(last)+')');}
await cdp('Runtime.enable'); await cdp('Page.enable'); await cdp('Network.enable');
await cdp('Emulation.setDeviceMetricsOverride', {width:760,height:900,deviceScaleFactor:1,mobile:false});
const cookie = await cdp('Network.setCookie', {name:'darwin_session', value:'valid', url:origin + '/app/'});
if (!cookie.success) throw new Error('could not establish authenticated browser fixture');
await cdp('Page.navigate', {url:origin + '/app/settings'});
await eventually('document.readyState === "complete" && !document.querySelector("#remote-discovery").hidden && !document.querySelector("#remote-pair-form").hidden','discovery panel did not mount');
if(await evaluate('document.querySelector("#remote-discovery-results").children.length'))throw Error('unexpected automatic discovery');
await evaluate('document.querySelector("#remote-peer-cloud").checked=true;document.querySelector("#remote-identity-verified").checked=true;document.querySelector("#remote-peer-ssh-key").value="/old/key";document.querySelector("#remote-discover").click();document.querySelector("#remote-discover").click()');
await eventually('document.querySelectorAll("#remote-discovery-results button").length===1','scan result missing');
await cdp('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:false});
const result=await evaluate('({text:document.querySelector("#remote-discovery-results").textContent,overflow:document.documentElement.scrollWidth>document.documentElement.clientWidth})');
if(!result.text.includes('Unverified')||!result.text.includes('2222')||result.overflow)throw Error('discovery result presentation '+JSON.stringify(result));
await evaluate('document.querySelector("#remote-discovery-results button").click()');
const form=await evaluate('({id:document.querySelector("#remote-peer-id").value,verified:document.querySelector("#remote-identity-verified").checked,cloud:document.querySelector("#remote-peer-cloud").checked,key:document.querySelector("#remote-peer-ssh-key").value,transport:document.querySelector("#remote-peer-transport").value,port:document.querySelector("#remote-peer-ssh-port").value})');
if(form.id!=='node-a'||form.verified||form.cloud||form.key||form.transport!=='https'||form.port!=='2222')throw Error('pairing handoff retained authority '+JSON.stringify(form));
await evaluate('document.querySelector("#remote-pair-form").dispatchEvent(new Event("submit",{cancelable:true,bubbles:true}))');
await evaluate('document.querySelector("#remote-peer-transport").value="ssh";document.querySelector("#remote-peer-transport").dispatchEvent(new Event("change",{bubbles:true}));document.querySelector("#remote-peer-ssh-user").value="fixture-user";document.querySelector("#remote-peer-ssh-key").value="/private/fixture-key";document.querySelector("#remote-peer-ssh-hosts").value="/private/fixture-hosts";for(const op of ["info","inspect","dispatch","cancel"])document.querySelector("#remote-op-"+op).checked=op==="info";document.querySelector("#remote-peer-cost").value="0";document.querySelector("#remote-peer-context").value="32768";document.querySelector("#remote-identity-verified").checked=true;document.querySelector("#remote-pair-form").dispatchEvent(new Event("submit",{cancelable:true,bubbles:true}))');
await eventually('document.querySelector("#remote-peer-id").value==="" && !document.querySelector("#remote-identity-verified").checked','pairing receipt did not reset form');
await evaluate('document.querySelector("#remote-discover").click()');
await eventually('document.querySelector("#remote-discovery-status").textContent.includes("could not")','discovery failure missing');
if(await evaluate('document.querySelector("#remote-discovery-results").children.length || document.body.textContent.includes("private discovery failure")'))throw Error('failure retained actionable results or leaked details');
socket.close();
`
