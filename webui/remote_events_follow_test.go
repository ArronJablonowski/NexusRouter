package webui

import (
	"strconv"
	"testing"
)

func TestRemoteEventsFollowLifecycleAndStopBoundaries(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-events.js"))
	runRoutingMapScript(t, `const vm=require('vm');
 function el(){return {children:[],handlers:{},isConnected:true,hidden:false,getClientRects(){return this.hidden?[]:[{}]},setAttribute(){},replaceChildren(){this.children=[]},append(...x){this.children.push(...x)},addEventListener(k,f){this.handlers[k]=f}}}
 function fixture(){
  const parent=el(),document={createElement:el,hidden:false},window={NexusLive:{fetch:async()=>({ok:true,json:async()=>({version:1,csrf_token:'csrf'})})}},calls=[],timers=new Map();let now=0,serial=0,head=1,state='running',failure=false,hold=null,release=null;
  const fetch=async(url,opts)=>{const b=JSON.parse(opts.body);calls.push({url,opts,b});if(hold)await new Promise(r=>release=r);if(failure)return {ok:false};const end=Math.min(head,b.after+1);return {ok:true,json:async()=>({version:1,instance:'node-a',request_id:'request-existing-01',task_id:'task-1',state,from_sequence:b.after,next_sequence:end,head_sequence:head,has_more:end<head,events:Array.from({length:Math.max(0,end-b.after)},(_,i)=>({sequence:b.after+i+1,kind:'task.progress',time:'2026-10-01T00:00:00Z'}))})}};
  vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch,Date:class extends Date{static now(){return now}},setTimeout:(f,ms)=>{if(ms!==5000)throw Error('poll cadence');const id=++serial;timers.set(id,f);return id},clearTimeout:id=>timers.delete(id)});
  window.NexusRemoteEvents.attach(parent,'node-a','request-existing-01','task-1','/app','csrf');
  const card=parent.children[0],[title,refresh,next,status,list,follow]=card.children;
  return {card,document,calls,timers,refresh,next,status,list,follow,set(v){if(v.head!==undefined)head=v.head;if(v.state)state=v.state;if(v.failure!==undefined)failure=v.failure;if(v.hold!==undefined)hold=v.hold;if(v.now!==undefined)now=v.now},release(){release()},poll(){if(timers.size!==1)throw Error('overlapping/missing timer');const [id,f]=timers.entries().next().value;timers.delete(id);f()}};
 }
 const tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{
  const f=fixture();if(f.calls.length||f.timers.size)throw Error('implicit following');
  f.follow.handlers.click();await tick();if(f.calls.length!==1||f.calls[0].b.after!==0||f.timers.size!==1)throw Error('start');
  f.poll();await tick();if(f.calls[1].b.after!==1||f.list.children.length||f.timers.size!==1)throw Error('unchanged head');
  f.set({head:3,state:'completed'});f.poll();await tick();if(f.calls[2].b.after!==1||f.timers.size!==1)throw Error('terminal backlog discarded');
  f.poll();await tick();if(f.calls[3].b.after!==2||f.timers.size||f.follow.textContent!=='Follow progress'||!f.status.textContent.includes('Following finished'))throw Error('terminal drain');
  for(const c of f.calls)if(c.url!=='/app/api/v1/remote-task-events'||c.opts.credentials!=='same-origin'||c.opts.headers['X-Darwin-CSRF']!=='csrf'||c.b.request_id!=='request-existing-01'||c.b.task_id!=='task-1')throw Error('wrong endpoint/authority');
  for(const mode of ['hidden','collapsed','detached','budget']){
   const x=fixture();x.follow.handlers.click();await tick();
   if(mode==='hidden')x.document.hidden=true;if(mode==='collapsed')x.card.hidden=true;if(mode==='detached')x.card.isConnected=false;if(mode==='budget')x.set({now:30*60*1000});
   x.poll();await tick();if(x.calls.length!==1||x.timers.size||x.follow.textContent!=='Follow progress')throw Error(mode+' continued');
  }
  for(const mode of ['failure','regression']){
   const x=fixture();x.set({head:2});x.follow.handlers.click();await tick();x.set(mode==='failure'?{failure:true}:{head:1});x.poll();await tick();
   if(x.calls.length!==2||x.timers.size||x.list.children.length||!x.status.textContent.includes('unavailable'))throw Error(mode+' retry/stale rendering');
  }
  const x=fixture();x.set({hold:true});x.follow.handlers.click();await tick();x.follow.handlers.click();x.follow.handlers.click();
  if(x.calls.length!==1)throw Error('stop/start overlapped read');x.release();await tick();if(x.timers.size||x.list.children.length||!x.status.textContent.includes('does not cancel'))throw Error('late response after stop');
  x.set({hold:false});x.follow.handlers.click();await tick();if(x.calls.length!==2||x.timers.size!==1)throw Error('explicit resume');
  x.refresh.handlers.click();await tick();if(x.calls.length!==3||x.calls[2].b.after!==0||x.timers.size)throw Error('manual refresh retained timer');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
