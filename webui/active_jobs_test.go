package webui

import (
	"os/exec"
	"testing"
)

func TestActiveJobsReconcileAndPreserveOnFailure(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node unavailable")
	}
	script := `
const fs=require('fs'),vm=require('vm');
const make=()=>({textContent:'',children:[],classList:{add(){},remove(){}},append(...x){this.children.push(...x)},replaceChildren(...x){this.children=x}});
const list=make(),status=make(),count=make();let run,fail=false,cycle=0,urls=[];
global.location={pathname:'/app/workboards'};global.document={body:{dataset:{basePath:'/app'}},querySelector:s=>({'#active-jobs-list':list,'#active-jobs-status':status,'#active-jobs-count':count}[s]),createElement:make};
global.window={NexusLive:{watch:(n,f)=>run=f,fetch:async url=>{urls.push(url);if(fail)throw Error();let items=[];if(!url.includes('kind=')&&cycle===0)items=[{task_id:'task-a',session_id:'chat-a',state:'running',started_at:'2026-10-02T00:00:00Z'}];if(cycle===0&&url.includes('kind=queued'))items=[{id:'queued-a',state:'queued',created_at:'2026-10-02T00:00:00Z'}];if(cycle===0&&url.includes('after='))items=[{task_id:'task-b',session_id:'chat-b',state:'running',started_at:'2026-10-02T00:00:00Z'}];const more=cycle===0&&!url.includes('?');return {ok:true,json:async()=>({version:1,items,next_cursor:more?'cursor-1':'',has_more:more})}}}};
vm.runInThisContext(fs.readFileSync(process.argv[1],'utf8'));
(async()=>{await run();if(list.children.length!==3||count.textContent!=='3'||list.children[0].children[0].href!=='/app/chats/chat-a')throw Error('initial');fail=true;await run();if(list.children.length!==3||!status.textContent.includes('stale'))throw Error('stale');fail=false;cycle=1;await run();if(list.children.length!==0||status.textContent!=='No active jobs.')throw Error('terminal retained');if(!urls.some(u=>u.includes('kind=queued')))throw Error('queue omitted')})().catch(e=>{console.error(e);process.exit(1)});
`
	out, e := exec.Command(node, "-e", script, "./assets/v1/active-jobs.js").CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
