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
const make=()=>({textContent:'',children:[],classList:{add(){},remove(){},toggle(){}},setAttribute(){},addEventListener(){},isConnected:false,append(...x){this.children.push(...x)},insertBefore(n,b){const old=this.children.indexOf(n);if(old>=0)this.children.splice(old,1);const i=b?this.children.indexOf(b):this.children.length;this.children.splice(i,0,n)},removeChild(n){this.children.splice(this.children.indexOf(n),1)},replaceChildren(...x){this.children=x}});
const list=make(),status=make(),count=make();let run,fail=false,cycle=0,urls=[];
global.location={pathname:'/app/active-jobs'};global.document={body:{dataset:{basePath:'/app'},append(){}},querySelectorAll:()=>[],querySelector:s=>s==='#active-jobs-view'?make():s.endsWith('-list')?list:s.endsWith('-status')?status:count,createElement:make};
global.window={NexusLive:{watch:(n,f)=>{if(n==='local-active-jobs')run=f},fetch:async url=>{urls.push(url);if(url.endsWith('/remote-membership'))return {ok:true,json:async()=>({version:1,enabled:false})};if(fail)throw Error();let items=[];if(!url.includes('kind=')&&cycle===0)items=[{task_id:'task-a',session_id:'chat-a',state:'running',started_at:'2026-10-02T00:00:00Z'}];if(cycle===0&&url.includes('kind=queued'))items=[{id:'queued-a',state:'queued',created_at:'2026-10-02T00:00:00Z'}];if(cycle===0&&url.includes('after='))items=[{task_id:'task-b',session_id:'chat-b',state:'running',started_at:'2026-10-02T00:00:00Z'}];const more=cycle===0&&!url.includes('?');return {ok:true,json:async()=>({version:1,items,next_cursor:more?'cursor-1':'',has_more:more})}}}};
vm.runInThisContext(fs.readFileSync(process.argv[1],'utf8'));
(async()=>{await run();if(list.children.length!==3||count.textContent!=='3')throw Error('initial');fail=true;await run();if(list.children.length!==3||!status.textContent.includes('stale'))throw Error('stale');fail=false;cycle=1;await run();if(list.children.length!==0||status.textContent!=='No active local jobs.')throw Error('terminal retained');if(!urls.some(u=>u.includes('kind=queued')))throw Error('queue omitted')})().catch(e=>{console.error(e);process.exit(1)});
`
	out, e := exec.Command(node, "-e", script, "./assets/v1/active-jobs.js").CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}

func TestActiveJobsFederatesOnlyPermittedCallerInventory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script := `
const fs=require('fs'),vm=require('vm');
const make=()=>({textContent:'',children:[],classList:{add(){},remove(){},toggle(){}},setAttribute(){},addEventListener(){},isConnected:false,append(...x){this.children.push(...x)},insertBefore(n,b){const old=this.children.indexOf(n);if(old>=0)this.children.splice(old,1);const i=b?this.children.indexOf(b):this.children.length;this.children.splice(i,0,n)},removeChild(n){this.children.splice(this.children.indexOf(n),1)},replaceChildren(...x){this.children=x}});
const list=make(),status=make(),count=make();let run,broken=false,inspected=[];
global.location={pathname:'/app/active-jobs'};global.document={body:{dataset:{basePath:'/app'},append(){}},querySelectorAll:()=>[],querySelector:s=>s==='#active-jobs-view'?make():s.endsWith('-list')?list:s.endsWith('-status')?status:count,createElement:make};
global.window={NexusLive:{watch:(n,f)=>{if(n==='remote-active-jobs')run=f},fetch:async(url,options)=>{
 let value;
 if(url.endsWith('/remote-membership'))value={version:1,enabled:true,inspection_enabled:true,registry:{peers:[{id:'spark',operations:['inspect']},{id:'private',operations:['info']}]}};
 else if(url.endsWith('/session/csrf'))value={version:1,csrf_token:'test-token'};
 else if(url.endsWith('/remote-inspection')){const body=JSON.parse(options.body);inspected.push(body.instance);if(body.instance!=='spark'||options.headers['X-Darwin-CSRF']!=='test-token'||body.after!=='')throw Error('authority');value={version:1,observed_at:'2026-10-04T00:00:00Z',tasks:{version:1,instance:broken?'wrong':'spark',after:'',next:'req-b',has_more:false,tasks:[{request_id:'req-a',state:'running'},{request_id:'req-b',state:'succeeded'}]}};}
 else value={version:1,items:[],next_cursor:'',has_more:false};
 return {ok:true,json:async()=>value};
}}};
vm.runInThisContext(fs.readFileSync(process.argv[1],'utf8'));
(async()=>{await run();if(count.textContent!=='1'||!list.children[0].children[1].textContent.includes('spark · running'))throw Error('remote missing');broken=true;await run();if(count.textContent!=='0'||!status.textContent.includes('Inventory incomplete'))throw Error('invalid response accepted');if(inspected.some(x=>x!=='spark'))throw Error('permission bypass')})().catch(e=>{console.error(e);process.exit(1)});
`
	out, err := exec.Command(node, "-e", script, "./assets/v1/active-jobs.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}
