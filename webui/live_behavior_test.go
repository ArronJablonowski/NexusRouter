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
