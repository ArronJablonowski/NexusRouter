package webui

import (
	"strconv"
	"testing"
)

func TestRemoteTaskControlsRequireRefreshAfterUncertainCancel(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-task-controls.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},disabled:false,textContent:'',replaceChildren(){this.children=[]},focus(){},setAttribute(){},append(...x){this.children.push(...x)},addEventListener(k,f){this.handlers[k]=f}}};
 const parent=el(),document={createElement:el},window={NexusRemoteEvents:{attach(){}}};let requests=[],failCancel=true,state='running',ids=['task-a'];
 const fetch=async(url,opts)=>{const input=JSON.parse(opts.body);requests.push(input);if(input.action==='cancel'&&failCancel)return {ok:false};return {ok:true,json:async()=>({version:1,instance:'node-a',request_id:'request-existing-0001',submission_id:'submission-a',task_ids:ids,state,cancel_requested:input.action==='cancel',result_text:state==='succeeded'?'<script>output</script>':''})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});window.NexusRemoteTaskControls.attach(parent,{id:'node-a'},{request_id:'request-existing-0001'},'/app','csrf');
 const [title,refresh,cancel,status,result,confirmation]=parent.children[0].children,tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{
 if(requests.length||!cancel.disabled)throw Error('implicit control');cancel.handlers.click();await tick();if(requests.length)throw Error('cancel without status');
 refresh.handlers.click();await tick();if(cancel.disabled||requests.length!==1)throw Error('status not loaded');
 cancel.handlers.click();await tick();if(requests.length!==1||confirmation.hidden)throw Error('confirmation dispatched');confirmation.children[2].handlers.click();confirmation.children[1].handlers.click();await tick();if(requests.length!==1)throw Error('dismiss dispatched');cancel.handlers.click();refresh.handlers.click();await tick();confirmation.children[1].handlers.click();await tick();if(requests.length!==2||!confirmation.hidden)throw Error('stale confirmation');cancel.handlers.click();confirmation.children[1].handlers.click();confirmation.children[1].handlers.click();await tick();if(requests.length!==3||requests[2].expected_submission_id!=='submission-a'||!cancel.disabled||!status.textContent.includes('may have taken effect'))throw Error('uncertain cancel not fenced');
 cancel.handlers.click();await tick();if(requests.length!==3)throw Error('cancel replayed');
 failCancel=false;refresh.handlers.click();await tick();cancel.handlers.click();confirmation.children[1].handlers.click();await tick();if(requests.length!==5||!cancel.disabled||!status.textContent.includes('cancellation requested'))throw Error('confirmed cancellation request');
 state='succeeded';refresh.handlers.click();await tick();if(!cancel.disabled||result.textContent!=='<script>output</script>')throw Error('terminal result/control');
 state='queued';ids=null;refresh.handlers.click();await tick();if(cancel.disabled||!status.textContent.includes('queued'))throw Error('queued request without task IDs');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
