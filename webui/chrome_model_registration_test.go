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

func TestChromeCloudModelConfigurationUsesCatalogAndExplicitSave(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(shell)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/models'});await eventually('typeof window.NexusLive!=="undefined"','Live client missing');
 await evaluate("window.registered=[];window.fetch=async(url,opts)=>{if(url.endsWith('/models'))return {ok:true,json:async()=>({..."+` + strconv.Quote(chromeModelsPopulatedFixture) + `+",models:[{id:'template',provider:'cloud',model:'old-model',locality:'cloud',configured:true,enabled:true,installed:false,usable:true,capabilities:['chat'],context_tokens:8192,health:'healthy',status_code:'available'},{id:'catalog',provider:'cloud',model:'new-model',locality:'cloud',configured:false,enabled:false,installed:false,usable:false,capabilities:[],health:'unknown',status_code:'catalog_only'}]})};if(url.endsWith('/settings'))return {ok:true,json:async()=>({digest:'a'.repeat(64)})};if(url.endsWith('/session/csrf'))return {ok:true,json:async()=>({version:1,csrf_token:'fixture'})};if(url.endsWith('/model-registration')){registered.push(JSON.parse(opts.body));return {ok:true,json:async()=>({version:1,restart_required:true,enabled:false})}}return {ok:false};};window.fixtureFetch=window.fetch;window.fetch=async(...args)=>{const r=await fixtureFetch(...args);return new Response(r.ok?JSON.stringify(await r.json()):'{}',{status:r.ok?200:503,headers:{'Content-Type':'application/json'}})};document.querySelector('#refresh-models').click();");
 await eventually('!!document.querySelector(".model-registration")','Configuration control missing');
 if(!await evaluate('document.querySelector(".model-registration").hidden&&getComputedStyle(document.querySelector(".model-registration")).display==="none"&&registered.length===0'))throw Error('Implicit configuration');
 await evaluate("document.querySelector('.model-registration').closest('li').querySelector('.model-card-toggle').click();document.querySelector('.model-registration').requestSubmit();document.querySelector('.model-registration').requestSubmit();");
 await eventually('document.querySelector(".model-registration").textContent.includes("Saved disabled")','Configuration receipt missing');
 if(!await evaluate("registered.length===1&&registered[0].catalog_id==='catalog'&&registered[0].template_id==='template'&&registered[0].context_tokens===4096"))throw Error('Unbound or duplicate configuration');
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});if(await evaluate('document.documentElement.scrollWidth>innerWidth'))throw Error('Configuration layout overflow');socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
