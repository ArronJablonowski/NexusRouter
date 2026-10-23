package webui

import (
	"strings"
	"testing"
)

func TestLiveSchedulerNoOverlapPauseReconnectAndDisposal(t *testing.T) {
	source := strings.Split(string(mustAsset(t, "assets/v1/live.js")), "// Styles can be replaced")[0]
	preamble := `let timerID=0;const timers=new Map(),listeners={};const navigator={onLine:true};
 const window={addEventListener(name,fn){listeners[name]=fn}},document={hidden:false,addEventListener(name,fn){listeners[name]=fn}};
 function setTimeout(fn,ms){timers.set(++timerID,{fn,ms});return timerID;}function clearTimeout(id){timers.delete(id);}
 async function fire(){const entries=[...timers.entries()];for(const [id,t]of entries){timers.delete(id);t.fn();}for(let i=0;i<8;i++)await Promise.resolve();}
 `
	steps := `(async()=>{
 let count=0,finish;const dispose=window.NexusLive.watch('slow',()=>{count++;return new Promise(r=>finish=r)},{interval:2000,immediate:true});
 await fire();window.NexusLive.wake();await fire();if(count!==1)throw Error('overlapping load');
 finish();await Promise.resolve();await fire();if(count!==2)throw Error('load did not resume');dispose();finish();await fire();if(timers.size)throw Error('disposed job scheduled again');
 let visible=0;window.NexusLive.watch('visible',()=>{visible++;},{interval:2000});document.hidden=true;listeners.visibilitychange();await fire();if(visible)throw Error('hidden tab polled');document.hidden=false;listeners.visibilitychange();await fire();if(visible!==1)throw Error('visible tab not reconciled');
 navigator.onLine=false;listeners.offline();await fire();if(visible!==1)throw Error('offline polling');navigator.onLine=true;listeners.online();await fire();if(visible!==2)throw Error('online recovery failed');
 let dirty=true,updates=0;window.NexusLive.watch('draft',()=>{updates++;},{ready:()=>!dirty});await fire();if(updates)throw Error('dirty form overwritten');dirty=false;await fire();if(updates!==1)throw Error('clean form not updated');
 listeners.pagehide();await fire();if(timers.size)throw Error('pagehide left timers');
 })().catch(e=>{console.error(e);process.exitCode=1});`
	runRoutingMapScript(t, preamble+source+steps)
}

func TestLiveFetchRejectsOversizedStreamBeforeBufferingWholeBody(t *testing.T) {
	source := strings.Split(string(mustAsset(t, "assets/v1/live.js")), "// Styles can be replaced")[0]
	preamble := `const window={addEventListener(){}},document={hidden:false,addEventListener(){}},navigator={onLine:true};let reads=0,canceled=false,signal;
 const fetch=async(url,options)=>{signal=options.signal;return new Response(new ReadableStream({pull(c){reads++;if(reads>20){c.close();return;}c.enqueue(new Uint8Array(1024*1024));},cancel(){canceled=true;}},{highWaterMark:0}));};`
	steps := `(async()=>{let rejected=false;try{await window.NexusLive.fetch('/oversize');}catch(e){rejected=e.message==='Live response too large';}if(!rejected||!signal.aborted||!canceled||reads!==9)throw Error('oversized stream was not stopped at byte bound: '+reads);})().catch(e=>{console.error(e);process.exitCode=1});`
	runRoutingMapScript(t, preamble+source+steps)
}

func TestAuthorizedFetchRefreshesGrantWithoutReplayingMutation(t *testing.T) {
	source := strings.Split(string(mustAsset(t, "assets/v1/live.js")), "// Styles can be replaced")[0]
	preamble := `let grantOK=true,mutationStatus=200,calls=[];
 const window={location:{href:'http://localhost/app/settings',origin:'http://localhost'},addEventListener(){}},document={body:{dataset:{basePath:'/app'}},addEventListener(){}},navigator={onLine:true};
 const fetch=async(url,options={})=>{calls.push({url,options});if(url.endsWith('/session/csrf'))return new Response(JSON.stringify({version:1,csrf_token:'fresh-token'}),{status:grantOK?200:403});return new Response('{}',{status:mutationStatus});};`
	steps := `(async()=>{
 const options={method:'POST',headers:{'X-Darwin-CSRF':'expired'},body:'exact-idempotent-body'};
 await window.NexusLive.authorizedFetch('/app/api/v1/settings',options);
 if(calls.length!==2||calls[1].options.body!==options.body||calls[1].options.headers.get('X-Darwin-CSRF')!=='fresh-token'||options.headers['X-Darwin-CSRF']!=='expired')throw Error('dispatch integrity');
 calls=[];grantOK=false;let rejected=false;try{await window.NexusLive.authorizedFetch('/app/api/v1/settings',options)}catch{rejected=true}if(!rejected||calls.length!==1)throw Error('dispatched without grant');
 for(const url of ['http://other.test/app/api/v1/settings','/outside/api/v1/settings']){calls=[];rejected=false;try{await window.NexusLive.authorizedFetch(url,options)}catch{rejected=true}if(!rejected||calls.length)throw Error('token scope leak');}
 calls=[];grantOK=true;mutationStatus=503;
 const failed=await window.NexusLive.authorizedFetch('/app/api/v1/settings',options);if(failed.status!==503||calls.length!==2)throw Error('mutation replay');
 calls=[];await window.NexusLive.authorizedFetch('/app/api/v1/settings');if(calls.length!==1)throw Error('read minted grant');
 })().catch(e=>{console.error(e);process.exitCode=1});`
	runRoutingMapScript(t, preamble+source+steps)
}
