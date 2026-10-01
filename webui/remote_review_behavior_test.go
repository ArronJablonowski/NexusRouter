package webui

import (
	"strconv"
	"testing"
)

func TestRemoteReviewBrowserCurrentHeadAndOneAttempt(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-review.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},hidden:false,disabled:false,value:'',textContent:'',setAttribute(){},append(...x){this.children.push(...x)},addEventListener(k,f){this.handlers[k]=f}}};const document={createElement:el},window={},calls=[];
 const original={version:1,request_id:'review-request-00001',domain:'coding',profile:'default',difficulty:'hard',context_tokens:32768,max_cost:0,private:true,capabilities:[],prompt:'Original task'};
 const fetch=async(url,opts)=>{const b=JSON.parse(opts.body);calls.push(b);if(b.action==='evaluate')return {ok:false};return {ok:true,json:async()=>({version:1,request_id:original.request_id,status:'recorded',classification:'advisory',verdict:'passed',method:'automated_ai'})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});const parent=el(),tick=()=>new Promise(r=>setImmediate(r));window.NexusRemoteReview.attach(parent,original.request_id,'/app','csrf',original);const form=parent.children[0].children[2],notice=parent.children[0].children[3],inspect=form.children[8],prepare=form.children[9],confirm=form.children[11];
 (async()=>{if(calls.length||!confirm.hidden)throw Error('automatic judging');prepare.handlers.click();if(confirm.hidden)throw Error('review not offered');form.handlers.input();confirm.handlers.click();await tick();if(calls.length)throw Error('stale review evaluated');prepare.handlers.click();confirm.handlers.click();confirm.handlers.click();await tick();if(calls.length!==1||calls[0].action!=='evaluate'||JSON.stringify(calls[0].request)!==JSON.stringify(original)||!notice.textContent.includes('may have started')||!prepare.disabled)throw Error('uncertain evaluator replayed or intent drifted');prepare.handlers.click();confirm.handlers.click();await tick();if(calls.length!==1)throw Error('repeated evaluation');inspect.handlers.click();await tick();if(calls.length!==2||calls[1].action!=='status'||!notice.textContent.includes('advisory')||!notice.textContent.includes('automated_ai')||!prepare.disabled)throw Error('current head lost or retry reenabled');
 const reloaded=el();window.NexusRemoteReview.attach(reloaded,original.request_id,'/app','csrf');const f=reloaded.children[0].children[2];f.children[8].handlers.click();f.children[9].handlers.click();await tick();if(calls.length!==2||!reloaded.children[0].children[3].textContent.includes('original'))throw Error('invented original requirements');
 const resumed=el();window.NexusRemoteReview.attach(resumed,original.request_id,'/app','csrf',original);const rf=resumed.children[0].children[2];rf.children[8].handlers.click();await tick();if(!rf.children[9].disabled)throw Error('existing review offered duplicate evaluation');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
