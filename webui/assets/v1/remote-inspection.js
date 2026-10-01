"use strict";
window.NexusRemoteInspection = (() => {
 function attach(card, peer, base, csrf) {
  const controls=document.createElement("div"), info=document.createElement("button"), tasks=document.createElement("button"), next=document.createElement("button"), status=document.createElement("output"), result=document.createElement("pre");
  info.type=tasks.type=next.type="button";info.textContent="Inspect capabilities";tasks.textContent="Inspect caller’s tasks";next.textContent="Next task page";next.hidden=true;
  status.setAttribute("role","status");status.setAttribute("aria-live","polite");
  let busy=false,cursor="";
  function lock(value){busy=value;info.disabled=value;tasks.disabled=value;next.disabled=value;}
  async function inspect(view,after="") {
   if(busy)return;
   lock(true);next.hidden=true;result.textContent="";status.textContent="Checking the trusted instance…";
   try {
    const response=await fetch(base+"/api/v1/remote-inspection",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({version:1,instance:peer.id,view,after})});
    if(!response.ok)throw Error();
    const value=await response.json(), payload=view==="info"?value.info:value.tasks;
    if(!value||value.version!==1||typeof value.observed_at!=="string"||!Number.isFinite(Date.parse(value.observed_at))||!payload||payload.version!==1||payload.instance!==peer.id)throw Error();
    if(view==="tasks") {
     if(payload.after!==after||!Array.isArray(payload.tasks)||payload.tasks.length>100||typeof payload.has_more!=="boolean"||typeof payload.next!=="string")throw Error();
     cursor=payload.next;next.hidden=!payload.has_more;
     status.textContent="Caller-owned task page checked at "+value.observed_at+". Refresh starts from the first page; this is a live traversal.";
    } else {
     if(typeof payload.available!=="boolean")throw Error();
     status.textContent="Checked at "+value.observed_at+". Instance reports "+(payload.available?"available":"unavailable")+". Capabilities and resource observations are advisory; admission is checked again for each task.";
    }
    result.textContent=JSON.stringify(payload,null,2);
   } catch {cursor="";next.hidden=true;result.textContent="";status.textContent="Inspection unavailable. The instance may be unreachable, denied, or not configured. No task was dispatched or retried.";}
   finally{lock(false);}
  }
  info.addEventListener("click",()=>inspect("info"));tasks.addEventListener("click",()=>inspect("tasks"));next.addEventListener("click",()=>inspect("tasks",cursor));
  controls.append(info,tasks,next);card.append(controls,status,result);
 }
 return {attach};
})();
