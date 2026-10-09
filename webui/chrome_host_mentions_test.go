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

func TestChromeHostMentionsRouting(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(shell)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/chats'});
 await eventually('Boolean(window.NexusHostMentions)','mention script unavailable');
 await evaluate("window.sent=[];window.fetch=async(url,opts)=>{const b=opts?.body?JSON.parse(opts.body):null;if(url.endsWith('model-targets'))return {ok:true,json:async()=>({version:1,hostname:'this-host',models:[{id:'local-coder',model:'Local coder',local:true},{id:'shared-a',model:'Shared',local:true},{id:'shared-b',model:'Shared',local:true}]})};if(url.endsWith('session/csrf'))return {ok:true,json:async()=>({version:1,csrf_token:'fixture'})};if(url.endsWith('remote-membership'))return {ok:true,json:async()=>({version:1,enabled:true,dispatch_enabled:true,registry:{peers:[{id:'mini',models:['m'],operations:['info','dispatch','inspect'],allow_private:true,max_context_tokens:8192},{id:'mini2',models:['m'],operations:['info','dispatch','inspect'],allow_private:true,max_context_tokens:8192}]}})};if(url.endsWith('remote-inspection'))return {ok:true,json:async()=>({version:1,info:{instance:b.instance,...(b.instance==='mini'?{hostname:'mini.local'}:{}),targeting_version:1,available:true,models:[{id:'m',model:'Local model',local:true,estimated_cost:0,context_tokens:8192}]}})};if(url.endsWith('remote-dispatch')){sent.push(b);return {ok:false}};return {ok:false};};window.NexusRemoteTaskControls.attach=(parent,peer,task)=>{window.recovered={peer,task}};window.box=document.querySelector('#composer-text');box.disabled=false;box.value='@mi';box.setSelectionRange(3,3);box.dispatchEvent(new Event('input',{bubbles:true}));");
 await eventually('document.querySelector("#host-options").textContent.includes("mini.local")','host lookup failed');
 await evaluate("box.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true}));");
 if(!await evaluate("box.value==='@mini/'&&sent.length===0&&document.querySelector('#host-options').textContent.includes('Local model')"))throw Error('Tab did not complete or submitted work');
 await evaluate("box.value='@mi';box.setSelectionRange(3,3);box.dispatchEvent(new Event('input'));box.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));");
 if(!await evaluate("document.querySelector('#host-options').hidden"))throw Error('Escape failed');
 if(!await evaluate("NexusHostMentions.prepare('hello user@example.com','', 'fixture')==='hello user@example.com'&&NexusHostMentions.prepare('@unknown task','','fixture')===null&&NexusHostMentions.prepare('@bad! task','','fixture')===null&&sent.length===0"))throw Error('mention boundary or fail closed broken');
 if(!await evaluate("NexusHostMentions.prepare('@this-host/local-coder task','','fixture').modelID==='local-coder'&&NexusHostMentions.prepare('@local/Local%20coder task','','fixture').text==='task'&&NexusHostMentions.prepare('@local/Shared task','','fixture')===null&&NexusHostMentions.prepare('@local/shared-a task','','fixture').modelID==='shared-a'&&NexusHostMentions.prepare('@local/auto task','','fixture')===null&&NexusHostMentions.prepare('@auto/m task','','fixture')===null&&sent.length===0"))throw Error('local model resolution or reserved forms broken');
 await evaluate("box.value='@local/Local';box.setSelectionRange(box.value.length,box.value.length);box.dispatchEvent(new Event('input'));box.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));");
 if(!await evaluate("box.value==='@local/local-coder '&&sent.length===0&&document.querySelector('#host-notice').textContent.includes('Local coder')"))throw Error('model completion submitted work or lost selection');
 for(const width of [1440,390]){await cdp('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false});if(!await evaluate('document.documentElement.scrollWidth<=innerWidth+1'))throw Error('assignment layout overflow');if(process.env.NEXUS_TARGET_SCREENSHOT){await evaluate('document.querySelector("#host-routing").scrollIntoView({block:"center"})');const fs=await import('node:fs');const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_TARGET_SCREENSHOT+'-'+width+'.png',Buffer.from(shot.data,'base64'));}}
 await evaluate("box.value='@mini.local test task';box.dispatchEvent(new Event('input'));document.querySelector('#host-model').value='m';NexusHostMentions.prepare(box.value,'existing-chat','fixture');");
 if(!await evaluate('sent.length===0'))throw Error('rerouted existing local chat');
 await evaluate("window.originalNow=Date.now;Date.now=()=>originalNow()+20000;box.dispatchEvent(new Event('input')); ");
 await eventually("document.querySelector('#host-model').value==='m'",'host refresh lost selected model');
 await evaluate("Date.now=originalNow");
 await evaluate("box.value='@mini.local/Local%20model test task';box.dispatchEvent(new Event('input'));NexusHostMentions.prepare(box.value,'','fixture');NexusHostMentions.prepare(box.value,'','fixture');");
 await eventually('sent.length===1&&Boolean(window.recovered)','dispatch recovery missing');
 if(!await evaluate("sent[0].instance==='mini'&&sent[0].task.version===1&&sent[0].task.prompt==='test task'&&sent[0].task.private&&sent[0].task.model_id==='m'&&sent[0].task.expected_model==='Local model'&&location.hash.includes(sent[0].request_id)&&recovered.task.request_id===sent[0].request_id"))throw Error('incorrect bound payload/recovery');
 await evaluate("box.dispatchEvent(new Event('input'))");if(!await evaluate("document.querySelector('#host-notice').textContent.includes('Delivery could not be confirmed')"))throw Error('input refresh erased uncertain delivery notice');
 await evaluate("NexusHostMentions.prepare(box.value,'','fixture');");if(!await evaluate('sent.length===1'))throw Error('uncertain request replayed');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
