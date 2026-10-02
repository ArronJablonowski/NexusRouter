"use strict";
(() => {
 const base=document.body.dataset.basePath || "";
 if (window.location.pathname !== base+"/stats") return;
 for (const node of document.querySelectorAll("main > section")) node.hidden=node.id!=="stats-view";
 const root=document.querySelector("#stats-meters"),status=document.querySelector("#stats-status"),dialog=document.querySelector("#stats-reset-dialog"),check=document.querySelector("#stats-reset-check"),confirm=document.querySelector("#stats-reset-confirm");
 let snapshot=null,pending=null,busy=false,timer;
 function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;}
 function counter(label,value,unavailable=false){
  if(typeof value!=="string" || !/^[0-9]{1,19}$/.test(value))throw new Error("Invalid usage count");
  const box=node("div","stats-counter"),caption=node("p","eyebrow",label),display=node("output","odometer");display.setAttribute("aria-label",label+": "+BigInt(value).toLocaleString()+" tokens");
  if(unavailable){display.textContent="Unavailable";display.classList.add("odometer-unavailable");display.setAttribute("aria-label",label+": unavailable");box.append(caption,display);return box;}
  const digits=BigInt(value).toString();
  display.dataset.digits=String(digits.length);
  const groups=digits.replace(/\B(?=(\d{3})+(?!\d))/g,",").split(",");
  groups.forEach((group,index)=>{
   if(index){const separator=node("span","odometer-separator",",");separator.setAttribute("aria-hidden","true");display.append(separator);}
   const cell=node("span","odometer-group",group);cell.setAttribute("aria-hidden","true");display.append(cell);
  });
  if(digits.length>=13)display.classList.add("odometer-large");
  box.append(caption,display);return box;
 }
 function counters(target,count,prefix=""){
  const unavailable=count.measured===0&&count.unknown>0;
  target.append(counter(prefix+"Recorded tokens in",count.input,unavailable),counter(prefix+"Recorded tokens out",count.output,unavailable));
 }
 function coverage(count){
  if(count.unknown>0)return "Partial total: "+count.unknown+" records have missing token counts. Recorded portions from "+count.partial+" of these records are included; actual usage may be higher.";
  return count.measured>0?"All recorded usage has token counts.":"No usage recorded in this period.";
 }
 function render(data){
  const signature=JSON.stringify({...data,updated_at:"",remote:data.remote?{...data.remote,observed_at:""}:undefined});
  if(signature===render.signature){status.textContent="Live · checked "+new Date(data.updated_at).toLocaleTimeString();return;}

  if(data.version!==1)throw new Error("Invalid stats");root.replaceChildren();
  for(const kind of ["cloud","local"]){const meter=data[kind];if(!meter||!Number.isSafeInteger(meter.revision))throw new Error("Invalid stats");
   const card=node("article","stats-card stats-"+kind);card.append(node("p","eyebrow",kind==="cloud"?"Cloud models":"Local models"),node("h2","","Lifetime odometer"));
   const totals=node("div","stats-counter-pair");counters(totals,meter.lifetime);card.append(totals,node("p","stats-note",coverage(meter.lifetime)));
   const trip=node("div","stats-trip"),head=node("div","stats-trip-heading"),reset=node("button","secondary","Reset "+kind+" trip");reset.type="button";reset.addEventListener("click",()=>{pending={kind,revision:meter.revision};check.checked=false;confirm.disabled=true;document.querySelector("#stats-reset-description").textContent="Start a new "+kind+" trip now. Lifetime totals and usage history will stay intact.";dialog.showModal();});
   head.append(node("h3","","Trip meter"),reset);trip.append(head,node("p","stats-since",meter.reset_at?"Since "+new Date(meter.reset_at).toLocaleString():"Since usage recording began"));
   const pair=node("div","stats-counter-pair");counters(pair,meter.trip,"Trip ");trip.append(pair,node("p","stats-note",coverage(meter.trip)));card.append(trip);
   root.append(card);
  }
  const remote=node("article","stats-card stats-remote");remote.append(node("p","eyebrow","Remote routers"),node("h2","","Remote router odometer"));
  if(data.remote_unavailable){remote.append(node("p","stats-note","Remote usage unavailable. Previously recorded totals have not been reset."));}
  else if(!data.remote){remote.append(node("p","stats-note","Pair a remote NexusRouter to record its token usage here."));}
  else {
   const meter=data.remote;
   const total=node("div","stats-counter-pair");counters(total,meter.total);remote.append(total,node("p","stats-note",coverage(meter.total)));
   for(const kind of ["local","cloud"]){const group=node("div","stats-trip");group.append(node("h3","",kind==="local"?"Local models on remote routers":"Cloud models on remote routers"));const pair=node("div","stats-counter-pair");counters(pair,meter[kind]);group.append(pair,node("p","stats-note",coverage(meter[kind])));remote.append(group);}
   remote.append(node("p","stats-note",meter.requests+" remote requests recorded · "+meter.pending+" pending · "+meter.unavailable+" tasks missing usage evidence · "+meter.unclassified+" records with unknown locality."));
   remote.append(node("p","stats-note",meter.last_sync?(meter.sync_error?"Reconciliation incomplete; retained totals may lag. Last attempt ":"Last reconciled ")+new Date(meter.last_sync).toLocaleString():"Waiting for the first remote reconciliation."));
   remote.append(node("p","stats-note","Lifetime tokens for work requested by this caller, across paired NexusRouter instances. Includes reported failed attempts and auxiliary model calls. Missing measurements are not estimated. These totals are separate from this router’s local and cloud odometers."));
  }
  root.append(remote);
  render.signature=signature;snapshot=data;status.textContent="Updated "+new Date(data.updated_at).toLocaleTimeString()+" · "+data.unclassified+" records have unknown model locality. Counts cover usage recorded by this NexusRouter instance; external app usage is not included.";document.querySelector("#connection-state").textContent="Connected";
 }
 async function json(path,options={}){const response=await window.NexusLive.fetch(base+path,{credentials:"same-origin",cache:"no-store",...options});if(!response.ok)throw new Error(response.status===409?"This trip changed in another window. Refresh and try again.":"Stats unavailable. Refresh or reconnect your browser.");return response.json();}
 async function load(){if(busy||dialog.open)return;busy=true;try{render(await json("/api/v1/stats"));}catch(error){status.textContent=error.message;}finally{busy=false;}}
 check.addEventListener("change",()=>{confirm.disabled=!check.checked||busy;});
 confirm.addEventListener("click",async()=>{if(!pending||!check.checked||busy)return;busy=true;confirm.disabled=true;try{
  const auth=await json("/api/v1/session/csrf",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});
  const data=await json("/api/v1/stats",{method:"POST",headers:{"Content-Type":"application/json","X-Darwin-CSRF":auth.csrf_token},body:JSON.stringify({version:1,locality:pending.kind,revision:pending.revision,confirm:true})});dialog.close();pending=null;render(data);
 }catch(error){dialog.close();status.textContent=error.message;}finally{busy=false;}});
 document.querySelector("#stats-refresh").addEventListener("click",load);
 window.NexusLive.watch("stats",load,{interval:3000,ready:()=>!busy&&!dialog.open});load();
})();
