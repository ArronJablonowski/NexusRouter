"use strict";
(() => {
 const base=document.body.dataset.basePath || "";
 if (window.location.pathname !== base+"/stats") return;
 for (const node of document.querySelectorAll("main > section")) node.hidden=node.id!=="stats-view";
 const root=document.querySelector("#stats-meters"),status=document.querySelector("#stats-status"),dialog=document.querySelector("#stats-reset-dialog"),check=document.querySelector("#stats-reset-check"),confirm=document.querySelector("#stats-reset-confirm");
 let snapshot=null,pending=null,busy=false,timer;
 function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;}
 function counter(label,value){
  if(typeof value!=="string" || !/^[0-9]{1,19}$/.test(value))throw new Error("Invalid usage count");
  const box=node("div","stats-counter"),caption=node("p","eyebrow",label),display=node("output","odometer");display.setAttribute("aria-label",label+": "+BigInt(value).toLocaleString()+" tokens");
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
 function render(data){
  if(data.version!==1)throw new Error("Invalid stats");root.replaceChildren();
  for(const kind of ["cloud","local"]){const meter=data[kind];if(!meter||!Number.isSafeInteger(meter.revision))throw new Error("Invalid stats");
   const card=node("article","stats-card stats-"+kind);card.append(node("p","eyebrow",kind==="cloud"?"Cloud models":"Local models"),node("h2","","Lifetime odometer"));
   const totals=node("div","stats-counter-pair");totals.append(counter("Tokens in",meter.lifetime.input),counter("Tokens out",meter.lifetime.output));card.append(totals);
   const trip=node("div","stats-trip"),head=node("div","stats-trip-heading"),reset=node("button","secondary","Reset "+kind+" trip");reset.type="button";reset.addEventListener("click",()=>{pending={kind,revision:meter.revision};check.checked=false;confirm.disabled=true;document.querySelector("#stats-reset-description").textContent="Start a new "+kind+" trip now. Lifetime totals and usage history will stay intact.";dialog.showModal();});
   head.append(node("h3","","Trip meter"),reset);trip.append(head,node("p","stats-since",meter.reset_at?"Since "+new Date(meter.reset_at).toLocaleString():"Since usage recording began"));
   const pair=node("div","stats-counter-pair");pair.append(counter("Trip tokens in",meter.trip.input),counter("Trip tokens out",meter.trip.output));trip.append(pair);card.append(trip);
   card.append(node("p","stats-note",meter.lifetime.unknown+" lifetime records and "+meter.trip.unknown+" trip records have unavailable token counts."));root.append(card);
  }
  snapshot=data;status.textContent="Updated "+new Date(data.updated_at).toLocaleTimeString()+" · "+data.unclassified+" records have unknown model locality.";document.querySelector("#connection-state").textContent="Connected";
 }
 async function json(path,options={}){const response=await fetch(base+path,{credentials:"same-origin",cache:"no-store",...options});if(!response.ok)throw new Error(response.status===409?"This trip changed in another window. Refresh and try again.":"Stats unavailable. Refresh or reconnect your browser.");return response.json();}
 async function load(){if(busy||dialog.open)return;busy=true;try{render(await json("/api/v1/stats"));}catch(error){status.textContent=error.message;}finally{busy=false;}}
 check.addEventListener("change",()=>{confirm.disabled=!check.checked||busy;});
 confirm.addEventListener("click",async()=>{if(!pending||!check.checked||busy)return;busy=true;confirm.disabled=true;try{
  const auth=await json("/api/v1/session/csrf",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});
  const data=await json("/api/v1/stats",{method:"POST",headers:{"Content-Type":"application/json","X-Darwin-CSRF":auth.csrf_token},body:JSON.stringify({version:1,locality:pending.kind,revision:pending.revision,confirm:true})});dialog.close();pending=null;render(data);
 }catch(error){dialog.close();status.textContent=error.message;}finally{busy=false;}});
 document.querySelector("#stats-refresh").addEventListener("click",load);
 function poll(){window.clearTimeout(timer);if(!document.hidden)load();timer=window.setTimeout(poll,15000);}
 document.addEventListener("visibilitychange",poll);poll();
})();
