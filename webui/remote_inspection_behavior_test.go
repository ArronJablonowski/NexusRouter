package webui

import (
	"strconv"
	"testing"
)

func TestRemoteInspectionExplicitBoundedPagingAndFailure(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-inspection.js"))
	runRoutingMapScript(t, `const vm=require('vm');function element(){return {children:[],handlers:{},hidden:false,textContent:'',setAttribute(){},append(...x){this.children.push(...x)},replaceChildren(){this.children=[]},addEventListener(k,f){this.handlers[k]=f}}};
 const card=element(),document={createElement:element},window={};let requests=[],fail=false;
 const fetch=async(url,options)=>{const input=JSON.parse(options.body);requests.push({url,options,input});if(fail)return {ok:false};return {ok:true,json:async()=>({version:1,observed_at:'2026-10-01T18:00:00Z',...(input.view==='info'?{info:{version:1,instance:'node-a',available:true}}:{tasks:{version:1,instance:'node-a',after:input.after,next:'request-cursor-0001',has_more:!input.after,tasks:[]}})})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});window.NexusRemoteInspection.attach(card,{id:'node-a'},'/console','csrf');
 const [controls,status,result]=card.children,[info,tasks,next]=controls.children,tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{
 if(requests.length)throw Error('automatic network call');tasks.handlers.click();await tick();
 if(requests.length!==1||requests[0].input.after!==''||requests[0].options.headers['X-Darwin-CSRF']!=='csrf'||next.hidden)throw Error('first task page');
 next.handlers.click();await tick();if(requests[1].input.after!=='request-cursor-0001'||!next.hidden)throw Error('cursor not preserved');
 tasks.handlers.click();await tick();if(requests[2].input.after!=='')throw Error('refresh did not restart traversal');
 info.handlers.click();await tick();if(!status.textContent.includes('advisory')||!next.hidden||!result.textContent.includes('available'))throw Error('availability claim');
 fail=true;info.handlers.click();await tick();if(result.textContent!==''||!next.hidden||!status.textContent.includes('unavailable')||requests.length!==5)throw Error('failed inspection retained success or retried');
 await tick();if(requests.length!==5)throw Error('polling started');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
