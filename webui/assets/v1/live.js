"use strict";
// Shared lifecycle for read-only reconciliation. Never repeats a user mutation.
window.NexusLive = (() => {
 const jobs=new Map();let stopped=false,inFlight=0;
 const visible=()=>!stopped&&!document.hidden&&navigator.onLine!==false;
 function schedule(job,delay){clearTimeout(job.timer);if(job.active&&visible())job.timer=setTimeout(()=>run(job),delay);}
 async function run(job){
  if(!job.active||!visible()||job.running)return;
  if(job.alive&&!job.alive()){job.active=false;clearTimeout(job.timer);jobs.delete(job.name);return;}
  if(job.ready&&!job.ready()){schedule(job,job.interval);return;}
  if(inFlight>=4){schedule(job,250);return;}
  job.running=true;inFlight++;
  try{const result=await job.load();job.failures=result===false?Math.min(job.failures+1,4):0;}
  catch{job.failures=Math.min(job.failures+1,4);}
  finally{inFlight--;job.running=false;schedule(job,Math.min(60000,job.interval*2**job.failures));}
 }
 function watch(name,load,{interval=5000,ready,alive,immediate=false}={}){
  if(jobs.has(name)){const prior=jobs.get(name);prior.active=false;clearTimeout(prior.timer);jobs.delete(name);}
  const job={name,active:true,load,ready,alive,interval:Math.max(2000,interval),timer:0,running:false,failures:0};jobs.set(name,job);
  schedule(job,immediate?0:job.interval);
  return ()=>{job.active=false;clearTimeout(job.timer);if(jobs.get(name)===job)jobs.delete(name);};
 }
 function wake(){for(const job of jobs.values()){clearTimeout(job.timer);if(visible())schedule(job,100);}}
 async function request(url,options={}){
  const controller=new AbortController(),abort=()=>controller.abort();
  const signal=options.signal;if(signal){signal.addEventListener("abort",abort,{once:true});if(signal.aborted)abort();}
  const timeout=setTimeout(abort,12000);
  try{
   const response=await fetch(url,{...options,signal:controller.signal}),limit=8*1024*1024;
   // Bound bytes while streaming, including decoded/compressed responses whose
   // Content-Length is absent or smaller than their delivered body.
   if(Number(response.headers.get("Content-Length"))>limit){controller.abort();throw Error("Live response too large");}
   let body="",size=0;
   if(response.body){
    const reader=response.body.getReader(),decoder=new TextDecoder();
    try{while(true){const chunk=await reader.read();if(chunk.done)break;size+=chunk.value.byteLength;if(size>limit){controller.abort();void reader.cancel().catch(()=>{});throw Error("Live response too large");}body+=decoder.decode(chunk.value,{stream:true});}body+=decoder.decode();}
    finally{reader.releaseLock();}
   }
   return new Response([204,205,304].includes(response.status)?null:body,{status:response.status,statusText:response.statusText,headers:response.headers});
  }
  finally{clearTimeout(timeout);if(signal)signal.removeEventListener("abort",abort);}
 }
 document.addEventListener("visibilitychange",wake);window.addEventListener("online",wake);window.addEventListener("offline",wake);window.addEventListener("focus",wake);
 window.addEventListener("pagehide",()=>{stopped=true;wake();});window.addEventListener("pageshow",()=>{stopped=false;wake();});
 function chats(options){
  const {base,list,stateLabel,renderChat,ready,busy,total,added,max,done,reconnect}=options;
  watch("chat-list",async()=>{
   if(!ready())return;busy(true);
   try{
    const response=await request(base+"/api/v1/chats?limit=25",{credentials:"same-origin",cache:"no-store"});if(!response.ok)throw Error();const page=await response.json();if(page.version!==1||!Array.isArray(page.items)||page.items.length>25)throw Error();
    for(const item of [...page.items].reverse()){
     const button=Array.from(list.querySelectorAll("button[data-chat-id]")).find(n=>n.dataset.chatId===item.chat_id);
     if(button){renderChat(item);}
     else if(total()<max&&renderChat(item)){added();list.prepend(list.lastElementChild);}
    }
    if(window.NexusChatDescriptions)window.NexusChatDescriptions.order(list,page.items.map(item=>item.chat_id));
    done();
   }catch{return false;}finally{busy(false);}
  });
  watch("inspector",()=>window.NexusInspector.loadGlobals());watch("chat-reconnect",reconnect);
 }
 return Object.freeze({watch,wake,fetch:request,chats});
})();

// Styles can be replaced atomically without reloading the document or forms.
(() => {
 const base=document.body.dataset.basePath||"";
 let link=document.querySelector('link[rel="stylesheet"][data-live-style]');if(!link)return;
 window.NexusLive.watch("stylesheet",async()=>{
  const response=await window.NexusLive.fetch(base+"/assets/v1/app.css",{method:"HEAD",credentials:"same-origin",cache:"no-store"});if(!response.ok)return false;
  const tag=response.headers.get("ETag");if(!tag||tag===link.dataset.liveStyle)return;
  const next=link.cloneNode();next.dataset.liveStyle=tag;
  await new Promise((resolve,reject)=>{const timer=setTimeout(()=>{next.remove();reject(Error("Stylesheet update timed out"));},12000);next.onload=()=>{clearTimeout(timer);link.replaceWith(next);link=next;resolve();};next.onerror=()=>{clearTimeout(timer);next.remove();reject(Error("Stylesheet unavailable"));};link.after(next);});
 },{interval:15000});
})();
