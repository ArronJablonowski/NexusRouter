"use strict";
window.NexusRemoteTaskControls = (()=>{
 function attach(parent,peer,task,base,csrf){
  const card=document.createElement("section"),title=document.createElement("h4"),refresh=document.createElement("button"),cancel=document.createElement("button"),status=document.createElement("output"),result=document.createElement("pre");
  title.textContent="Request "+task.request_id;refresh.type=cancel.type="button";refresh.textContent="Load status and result";cancel.textContent="Request cancellation";cancel.disabled=true;
  status.setAttribute("role","status");status.setAttribute("aria-live","polite");status.textContent="Load current status before requesting cancellation.";
  const confirmation=document.createElement("div"),warning=document.createElement("p"),confirm=document.createElement("button"),dismiss=document.createElement("button");
  confirmation.hidden=true;confirmation.setAttribute("role","group");confirmation.setAttribute("aria-label","Confirm task cancellation");
  warning.textContent="Request cancellation of "+task.request_id+" on "+peer.id+"? Work may finish before cancellation takes effect.";
  confirm.type=dismiss.type="button";confirm.textContent="Confirm cancellation";dismiss.textContent="Keep running";confirmation.append(warning,confirm,dismiss);
  const events=document.createElement("div");
  let current=null,busy=false,loaded=false,taskSignature="";
  function lock(value){confirmation.hidden=true;busy=value;refresh.disabled=value;cancel.disabled=value||!current||current.cancel_requested||!["queued","running"].includes(current.state);}
  async function control(action,background=false){
   if(busy||(action==="cancel"&&!current))return;
   const expected=current&&current.submission_id;if(!background)current=null;lock(true);if(!background)result.textContent="";status.textContent=action==="cancel"?"Requesting cancellation…":"Loading current remote status…";
   try{
    // Other tabs and inventory polls may evict an older bounded CSRF grant.
    // Acquire authority before this operation; never replay an uncertain cancel.
    const grant=await window.NexusLive.fetch(base+"/api/v1/session/csrf",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});
    if(!grant.ok)throw Error();const session=await grant.json();
    if(session.version!==1||typeof session.csrf_token!=="string"||!session.csrf_token||session.csrf_token.length>4096)throw Error();csrf=session.csrf_token;
    const body={version:1,instance:peer.id,request_id:task.request_id,action};if(action==="cancel")body.expected_submission_id=expected;
    const response=await window.NexusLive.fetch(base+"/api/v1/remote-task-control",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify(body)});
    if(!response.ok)throw Error();const value=await response.json();
    if(!value||value.version!==1||value.instance!==peer.id||value.request_id!==task.request_id||typeof value.submission_id!=="string"||!value.submission_id||!["queued","running","succeeded","failed","canceled"].includes(value.state)||typeof value.cancel_requested!=="boolean"||(action==="cancel"&&value.submission_id!==expected))throw Error();
    const taskIDs=value.task_ids===null?[]:value.task_ids;
    if(!Array.isArray(taskIDs)||taskIDs.length>128)throw Error();
    const signature=JSON.stringify(taskIDs);if(signature!==taskSignature){events.replaceChildren();taskIDs.forEach(id=>window.NexusRemoteEvents.attach(events,peer.id,task.request_id,id,base,csrf));taskSignature=signature;}
    loaded=true;
    current=value;status.textContent="Last loaded task state: "+value.state+(value.cancel_requested?" · cancellation requested":"")+". Updates automatically.";result.textContent=value.result_text||"";
   }catch{status.textContent=action==="cancel"?"Cancellation could not be confirmed. It may have taken effect. Load current status before deciding whether to try again.":"Current status is unavailable. No task was dispatched or retried.";current=null;return false;}
   finally{lock(false);}
  }
  refresh.addEventListener("click",()=>control("status"));
  cancel.addEventListener("click",()=>{if(!busy&&current&&!cancel.disabled){confirmation.hidden=false;confirm.focus();}});
  dismiss.addEventListener("click",()=>{confirmation.hidden=true;cancel.focus();});
  confirm.addEventListener("click",()=>{if(!confirmation.hidden&&!busy&&current&&!cancel.disabled)control("cancel");});
  card.append(title,refresh,cancel,status,result,confirmation,events);parent.append(card);
  window.NexusLive.watch("remote-task-"+peer.id+"-"+task.request_id,()=>control("status",true),{interval:3000,alive:()=>card.isConnected,immediate:true,ready:()=>!busy&&(!loaded||!current||["queued","running"].includes(current.state))&&confirmation.hidden&&Boolean(card.closest("details[open]"))});
 }
 return {attach};
})();
