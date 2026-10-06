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
 await evaluate("window.sent=[];window.fetch=async(url,opts)=>{const b=opts?.body?JSON.parse(opts.body):null;if(url.endsWith('session/csrf'))return {ok:true,json:async()=>({version:1,csrf_token:'fixture'})};if(url.endsWith('remote-membership'))return {ok:true,json:async()=>({version:1,enabled:true,dispatch_enabled:true,registry:{peers:[{id:'mini',models:['m'],operations:['info','dispatch','inspect'],allow_private:true,max_context_tokens:8192}]}})};if(url.endsWith('remote-inspection'))return {ok:true,json:async()=>({version:1,info:{instance:'mini',hostname:'mini.local',available:true,models:[{id:'m',model:'Local model',local:true,estimated_cost:0,context_tokens:8192}]}})};if(url.endsWith('remote-dispatch')){sent.push(b);return {ok:false}};return {ok:false};};window.NexusRemoteTaskControls.attach=(parent,peer,task)=>{window.recovered={peer,task}};window.box=document.querySelector('#composer-text');box.disabled=false;box.value='@mi';box.setSelectionRange(3,3);box.dispatchEvent(new Event('input',{bubbles:true}));");
 await eventually('document.querySelector("#host-options").textContent.includes("mini.local")','host lookup failed');
 await evaluate("box.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true}));");
 if(!await evaluate("box.value==='@mini.local '&&sent.length===0&&document.querySelector('#host-options').hidden"))throw Error('Tab did not complete or submitted work');
 await evaluate("box.value='@mi';box.setSelectionRange(3,3);box.dispatchEvent(new Event('input'));box.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));");
 if(!await evaluate("document.querySelector('#host-options').hidden"))throw Error('Escape failed');
 if(!await evaluate("NexusHostMentions.prepare('hello user@example.com','', 'fixture')==='hello user@example.com'&&NexusHostMentions.prepare('@unknown task','','fixture')===null&&NexusHostMentions.prepare('@bad! task','','fixture')===null&&sent.length===0"))throw Error('mention boundary or fail closed broken');
 await evaluate("box.value='@mini.local test task';box.dispatchEvent(new Event('input'));document.querySelector('#host-model').value='m';NexusHostMentions.prepare(box.value,'existing-chat','fixture');");
 if(!await evaluate('sent.length===0'))throw Error('rerouted existing local chat');
 await evaluate("NexusHostMentions.prepare(box.value,'','fixture');NexusHostMentions.prepare(box.value,'','fixture');");
 await eventually('sent.length===1&&Boolean(window.recovered)','dispatch recovery missing');
 if(!await evaluate("sent[0].instance==='mini'&&sent[0].task.version===1&&sent[0].task.prompt==='test task'&&sent[0].task.private&&sent[0].task.model_id==='m'&&location.hash.includes(sent[0].request_id)&&recovered.task.request_id===sent[0].request_id"))throw Error('incorrect bound payload/recovery');
 await evaluate("NexusHostMentions.prepare(box.value,'','fixture');");if(!await evaluate('sent.length===1'))throw Error('uncertain request replayed');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
