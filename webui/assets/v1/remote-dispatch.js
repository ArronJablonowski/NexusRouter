"use strict";
window.NexusRemoteDispatch=(()=>{
 function attach(parent,peer,base,csrf){
  const card=document.createElement("section"),title=document.createElement("h4"),notice=document.createElement("output"),body=document.createElement("div"),another=document.createElement("button");
  title.textContent="Send work to "+peer.id;notice.setAttribute("role","status");notice.setAttribute("aria-live","polite");another.type="button";another.textContent="Start another independent request";another.hidden=true;

  let busy=false;
  function recover(key){body.replaceChildren();window.NexusRemoteTaskControls.attach(body,peer,{request_id:key},base,csrf);another.hidden=false;}
  function draft(){
   body.replaceChildren();another.hidden=true;
   const key=crypto.randomUUID(),form=document.createElement("form"),identity=document.createElement("p"),review=document.createElement("button"),confirm=document.createElement("button"),preview=document.createElement("pre");
   identity.textContent="Request ID: "+key+". This is new work, not a retry of another request.";
   const fields={};
   function field(name,label,value,options){
    const wrapper=document.createElement("label"),input=document.createElement(options?"select":name==="prompt"?"textarea":"input");wrapper.textContent=label;
    if(options)options.forEach(([value,text])=>{const o=document.createElement("option");o.value=value;o.textContent=text;input.append(o);});
    input.value=value;fields[name]=input;wrapper.append(input);form.append(wrapper);return input;
   }
   field("execution","Execution mode","direct",[["direct","Direct specialist (default)"],["commander","Remote Commander — bounded local specialists"]]);
   field("specialists","Permitted specialist model IDs (comma separated)","");
   field("calls","Maximum specialist calls","4").type="number";
   field("minutes","Assignment deadline in minutes","5").type="number";
   field("model","Model","",[["","Choose a permitted model"],...(peer.models||[]).map(x=>[x,x])]);
   field("harness","Harness","",[["","Destination native harness"],...(peer.harnesses||[]).map(x=>[x,x])]);
   field("difficulty","Task difficulty","unknown",["unknown","easy","medium","hard"].map(x=>[x,x]));
   field("domain","Task domain","coding");field("profile","Routing profile","default");
   field("context","Context tokens","32768").type="number";const cost=field("cost","Maximum estimated cost","0");cost.type="number";cost.step="any";cost.min="0";
   field("private","Require private network and local inference","true",[["true","Required"],["false","Use the peer's explicitly authorized policy"]]);
   field("prompt","Instructions","");
   review.type="submit";review.textContent="Review request";confirm.type="button";confirm.textContent="Confirm and send once";confirm.hidden=true;
   let reviewed=null;
   form.addEventListener("input",()=>{reviewed=null;confirm.hidden=true;preview.textContent="";});form.addEventListener("change",()=>{reviewed=null;confirm.hidden=true;preview.textContent="";});
   form.addEventListener("submit",event=>{
    event.preventDefault();if(busy)return;
    const task={version:1,model_id:fields.model.value,prompt:fields.prompt.value,domain:fields.domain.value.trim(),profile:fields.profile.value.trim(),context_tokens:Number(fields.context.value),max_cost:Number(fields.cost.value),private:fields.private.value==="true"};
    if(fields.execution.value==="commander"){
     const ids=fields.specialists.value.split(",").map(x=>x.trim()).filter(Boolean),calls=Number(fields.calls.value),minutes=Number(fields.minutes.value);
     if(fields.harness.value||!task.private||task.max_cost!==0||ids.length<1||ids.length>8||new Set(ids).size!==ids.length||ids.some(id=>id===task.model_id||!(peer.models||[]).includes(id))||!Number.isInteger(calls)||calls<1||calls>16||!Number.isInteger(minutes)||minutes<1||minutes>60){notice.textContent="Commander mode requires private local inference, zero estimated cost, 1–8 permitted specialists, 1–16 calls, and a 1–60 minute deadline. Select the destination's configured Commander as the model; leave harness empty.";return;}
     task.execution={mode:"commander",depth:1,specialist_ids:ids,max_calls:calls,deadline:new Date(Date.now()+minutes*60000).toISOString()};
    }
    if(fields.harness.value){task.harness_id=fields.harness.value;task.harness_difficulty=fields.difficulty.value;}
    if(!task.model_id||!task.prompt.trim()||!task.domain||!task.profile||!fields.context.value||!fields.cost.value||!Number.isSafeInteger(task.context_tokens)||task.context_tokens<1||!Number.isFinite(task.max_cost)||task.max_cost<0){notice.textContent="Complete the instructions, model, task domain, profile and numeric limits.";return;}
    reviewed=task;preview.textContent=JSON.stringify({instance:peer.id,request_id:key,task},null,2);confirm.hidden=false;notice.textContent="Review the destination, instructions and permissions before sending. Admission is checked by the remote instance.";
   });
   confirm.addEventListener("click",async()=>{
    if(busy||!reviewed||confirm.hidden)return;
    try{const url=new URL(window.location.href);url.hash=new URLSearchParams({remote_peer:peer.id,remote_request:key}).toString();window.history.replaceState(null,"",url);}catch{notice.textContent="Cannot preserve the request recovery link. No work was sent.";return;}
    busy=true;const task=reviewed;reviewed=null;confirm.hidden=true;review.disabled=true;Object.values(fields).forEach(f=>f.disabled=true);notice.textContent="Sending request "+key+"…";
    try{
     const response=await fetch(base+"/api/v1/remote-dispatch",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({version:1,instance:peer.id,request_id:key,task})});
     if(!response.ok)throw Error();const p=await response.json();if(!p||p.version!==1||p.instance!==peer.id||p.request_id!==key||typeof p.submission_id!=="string"||!p.submission_id)throw Error();
     notice.textContent="Request "+key+" acknowledged. Load status to inspect progress and results.";
    }catch{notice.textContent="Delivery of "+key+" could not be confirmed. It may have been admitted. Inspect this request before creating any replacement; no retry was sent.";}
    finally{busy=false;recover(key);}
   });
   form.append(identity,review,preview,confirm);body.append(form);
  }
  another.addEventListener("click",()=>{if(!busy){notice.textContent="Creating a separate request does not retry or cancel previous work.";draft();}});
  card.append(title,notice,body,another);parent.append(card);
  try{const params=new URLSearchParams(new URL(window.location.href).hash.slice(1)),key=params.get("remote_peer")===peer.id?params.get("remote_request"):null;if(key){if(!/^[A-Za-z0-9_-]{16,64}$/.test(key))throw Error();notice.textContent="Recovered request ID. Inspect its status before starting replacement work.";recover(key);}else draft();}catch{notice.textContent="Request recovery is unavailable. No work was sent.";}
 }
 return {attach};
})();
