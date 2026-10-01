"use strict";
window.NexusRemoteAutomatic=(()=>{
 function attach(parent,base,csrf,reviewEnabled=false,backgroundReviewEnabled=false){
  const card=document.createElement("section"),title=document.createElement("h3"),notice=document.createElement("output"),body=document.createElement("div"),another=document.createElement("button");
  title.textContent="Automatically choose a remote model and harness";notice.setAttribute("role","status");notice.setAttribute("aria-live","polite");another.type="button";another.textContent="Start another independent automatic request";another.hidden=true;
  let busy=false;
  async function post(path,payload){const response=await fetch(base+"/api/v1/"+path,{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify(payload)});if(!response.ok)throw Error();return response.json();}
  function valid(p,key){return p&&p.version===1&&p.request_id===key&&typeof p.instance==="string"&&/^[A-Za-z0-9_-]{1,64}$/.test(p.instance)&&typeof p.submission_id==="string"&&p.submission_id;}
  function recover(key,original){
   body.replaceChildren();another.hidden=false;
   const label=document.createElement("p"),inspect=document.createElement("button"),controls=document.createElement("div");label.textContent="Saved automatic request: "+key;inspect.type="button";inspect.textContent="Find saved destination and status";
   inspect.addEventListener("click",async()=>{if(busy)return;busy=true;inspect.disabled=another.disabled=true;controls.replaceChildren();notice.textContent="Inspecting the saved destination…";
    try{const p=await post("remote-recorded-status",{version:1,request_id:key});if(!valid(p,key))throw Error();notice.textContent="Saved destination: "+p.instance+". Current state: "+p.state+". No new selection or dispatch was made.";window.NexusRemoteTaskControls.attach(controls,{id:p.instance},{request_id:key},base,csrf);if(reviewEnabled&&p.state==="succeeded")window.NexusRemoteReview.attach(controls,key,base,csrf,original);}
    catch{notice.textContent="Status for "+key+" is unavailable. A choice or dispatch may be incomplete; this is not proof that no work ran. No retry or replacement was sent.";}
    finally{busy=false;inspect.disabled=another.disabled=false;}
   });body.append(label,inspect,controls);
  }
  function draft(){
   body.replaceChildren();another.hidden=true;
   const key=crypto.randomUUID(),form=document.createElement("form"),identity=document.createElement("p"),review=document.createElement("button"),confirm=document.createElement("button"),preview=document.createElement("pre"),fields={};
   identity.textContent="New request "+key+". Selection uses recorded task-specific accuracy evidence and current permissions, capabilities and capacity. Limited evidence is not a guarantee of the best result. Exploration is disabled.";
   if(backgroundReviewEnabled)identity.textContent+=" Background AI review is enabled. Original instructions are retained in the private local review queue before dispatch, for one automated review within the configured time limit. AI review is advisory, not human confirmation.";
   function field(name,label,value,options){const wrapper=document.createElement("label"),input=document.createElement(options?"select":name==="prompt"?"textarea":"input");wrapper.textContent=label;if(options)options.forEach(([v,text])=>{const o=document.createElement("option");o.value=v;o.textContent=text;input.append(o);});input.value=value;fields[name]=input;wrapper.append(input);form.append(wrapper);return input;}
   field("domain","Task domain","coding");field("profile","Routing profile","default");field("difficulty","Task difficulty","unknown",["unknown","easy","medium","hard"].map(x=>[x,x]));field("context","Context tokens","32768").type="number";
   const cost=field("cost","Maximum estimated cost","0");cost.type="number";cost.step="any";cost.min="0";
   field("private","Require private network and local inference","true",[["true","Required"],["false","Use explicitly authorized peer policy"]]);field("capabilities","Required capabilities (comma-separated)","");field("prompt","Instructions","");
   review.type="submit";review.textContent="Review automatic request";confirm.type="button";confirm.textContent="Confirm automatic selection and send once";confirm.hidden=true;let reviewed=null;
   function invalidate(){reviewed=null;confirm.hidden=true;preview.textContent="";}form.addEventListener("input",invalidate);form.addEventListener("change",invalidate);
   form.addEventListener("submit",event=>{event.preventDefault();if(busy)return;
    const payload={version:1,request_id:key,domain:fields.domain.value.trim(),profile:fields.profile.value.trim(),difficulty:fields.difficulty.value,context_tokens:Number(fields.context.value),max_cost:Number(fields.cost.value),private:fields.private.value==="true",capabilities:fields.capabilities.value.split(",").map(x=>x.trim()).filter(Boolean),prompt:fields.prompt.value};
    if(!payload.prompt.trim()||!payload.domain||!payload.profile||!fields.context.value||!fields.cost.value||!Number.isSafeInteger(payload.context_tokens)||payload.context_tokens<8192||payload.context_tokens>16777216||!Number.isFinite(payload.max_cost)||payload.max_cost<0){notice.textContent="Complete instructions, task classification and valid numeric limits (at least 8192 context tokens).";return;}
    reviewed=payload;preview.textContent=JSON.stringify(payload,null,2);confirm.hidden=false;notice.textContent="Review the requirements. NexusRouter will choose an eligible destination, model and harness when you confirm.";
   });
   confirm.addEventListener("click",async()=>{if(busy||!reviewed||confirm.hidden)return;
    try{const url=new URL(window.location.href);url.hash=new URLSearchParams({remote_auto_request:key}).toString();window.history.replaceState(null,"",url);}catch{notice.textContent="Cannot preserve the recovery link. No work was sent.";return;}
    busy=true;const payload=reviewed;invalidate();review.disabled=true;Object.values(fields).forEach(f=>f.disabled=true);notice.textContent="Selecting and sending request "+key+"…";
    try{const p=await post("remote-auto-dispatch",payload);if(!valid(p,key))throw Error();notice.textContent="Request "+key+" acknowledged by "+p.instance+". Inspect the saved request for status and controls.";}
    catch{notice.textContent="Delivery of "+key+" could not be confirmed. It may have been admitted. Inspect before creating replacement work; no retry was sent.";}
    finally{busy=false;recover(key,payload);}
   });form.append(identity,review,preview,confirm);body.append(form);
  }
  another.addEventListener("click",()=>{if(!busy){notice.textContent="This creates separate work and replaces the recovery link; it does not retry or cancel the previous request. Save its request ID first.";draft();}});
  card.append(title,notice,body,another);parent.append(card);
  try{const key=new URLSearchParams(new URL(window.location.href).hash.slice(1)).get("remote_auto_request");if(key){if(!/^[A-Za-z0-9_-]{16,64}$/.test(key))throw Error();notice.textContent="Recovered automatic request ID. Inspect its saved destination before creating replacement work.";recover(key);}else draft();}catch{notice.textContent="Request recovery is unavailable. No work was sent.";}
 }
 return {attach};
})();
