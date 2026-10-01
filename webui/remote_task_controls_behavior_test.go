package webui

import (
	"strconv"
	"testing"
)

func TestRemoteTaskControlsRequireRefreshAfterUncertainCancel(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-task-controls.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},disabled:false,textContent:'',setAttribute(){},append(...x){this.children.push(...x)},addEventListener(k,f){this.handlers[k]=f}}};
 const parent=el(),document={createElement:el},window={confirm:()=>true};let requests=[],failCancel=true,state='running';
 const fetch=async(url,opts)=>{const input=JSON.parse(opts.body);requests.push(input);if(input.action==='cancel'&&failCancel)return {ok:false};return {ok:true,json:async()=>({version:1,instance:'node-a',request_id:'request-existing-0001',submission_id:'submission-a',state,cancel_requested:input.action==='cancel',result_text:state==='succeeded'?'<script>output</script>':''})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});window.NexusRemoteTaskControls.attach(parent,{id:'node-a'},{request_id:'request-existing-0001'},'/app','csrf');
 const [title,refresh,cancel,status,result]=parent.children[0].children,tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{
 if(requests.length||!cancel.disabled)throw Error('implicit control');cancel.handlers.click();await tick();if(requests.length)throw Error('cancel without status');
 refresh.handlers.click();await tick();if(cancel.disabled||requests.length!==1)throw Error('status not loaded');
 cancel.handlers.click();cancel.handlers.click();await tick();if(requests.length!==2||requests[1].expected_submission_id!=='submission-a'||!cancel.disabled||!status.textContent.includes('may have taken effect'))throw Error('uncertain cancel not fenced');
 cancel.handlers.click();await tick();if(requests.length!==2)throw Error('cancel replayed');
 failCancel=false;refresh.handlers.click();await tick();cancel.handlers.click();await tick();if(requests.length!==4||!cancel.disabled||!status.textContent.includes('cancellation requested'))throw Error('confirmed cancellation request');
 state='succeeded';refresh.handlers.click();await tick();if(!cancel.disabled||result.textContent!=='<script>output</script>')throw Error('terminal result/control');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
