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
  let current=null,busy=false;
  function lock(value){confirmation.hidden=true;busy=value;refresh.disabled=value;cancel.disabled=value||!current||current.cancel_requested||!["queued","running"].includes(current.state);}
  async function control(action){
   if(busy||(action==="cancel"&&!current))return;
   const expected=current&&current.submission_id;current=null;lock(true);events.replaceChildren();result.textContent="";status.textContent=action==="cancel"?"Requesting cancellation…":"Loading current remote status…";
   try{
    const body={version:1,instance:peer.id,request_id:task.request_id,action};if(action==="cancel")body.expected_submission_id=expected;
    const response=await fetch(base+"/api/v1/remote-task-control",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify(body)});
    if(!response.ok)throw Error();const value=await response.json();
    if(!value||value.version!==1||value.instance!==peer.id||value.request_id!==task.request_id||typeof value.submission_id!=="string"||!value.submission_id||!["queued","running","succeeded","failed","canceled"].includes(value.state)||typeof value.cancel_requested!=="boolean"||(action==="cancel"&&value.submission_id!==expected))throw Error();
    if(!Array.isArray(value.task_ids)||value.task_ids.length>128)throw Error();
    value.task_ids.forEach(id=>window.NexusRemoteEvents.attach(events,peer.id,task.request_id,id,base,csrf));
    current=value;status.textContent="State: "+value.state+(value.cancel_requested?" · cancellation requested":"")+". Refresh to check for changes.";result.textContent=value.result_text||"";
   }catch{status.textContent=action==="cancel"?"Cancellation could not be confirmed. It may have taken effect. Load current status before deciding whether to try again.":"Current status is unavailable. No task was dispatched or retried.";}
   finally{lock(false);}
  }
  refresh.addEventListener("click",()=>control("status"));
  cancel.addEventListener("click",()=>{if(!busy&&current&&!cancel.disabled){confirmation.hidden=false;confirm.focus();}});
  dismiss.addEventListener("click",()=>{confirmation.hidden=true;cancel.focus();});
  confirm.addEventListener("click",()=>{if(!confirmation.hidden&&!busy&&current&&!cancel.disabled)control("cancel");});
  card.append(title,refresh,cancel,status,result,confirmation,events);parent.append(card);
 }
 return {attach};
})();
