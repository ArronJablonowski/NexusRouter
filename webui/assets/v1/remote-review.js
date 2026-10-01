"use strict";
window.NexusRemoteReview=(()=>{
 function attach(parent,key,base,csrf,original){
  const card=document.createElement("section"),title=document.createElement("h4"),explain=document.createElement("p"),form=document.createElement("form"),notice=document.createElement("output"),inspect=document.createElement("button"),prepare=document.createElement("button"),confirm=document.createElement("button"),preview=document.createElement("pre"),fields={};
  title.textContent="Review completed output";explain.textContent="AI reviews contribute advisory quality evidence for future routing. They do not count as human or deterministic verification. The configured reviewer and cost limit are set by the administrator. Re-enter the exact original requirements after a reload; changed requirements are rejected.";
  notice.setAttribute("role","status");notice.setAttribute("aria-live","polite");let busy=false,reviewed=null,attempted=false;
  function field(name,label,value,options){const wrapper=document.createElement("label"),input=document.createElement(options?"select":name==="prompt"?"textarea":"input");wrapper.textContent=label;if(options)options.forEach(([v,text])=>{const o=document.createElement("option");o.value=v;o.textContent=text;input.append(o);});input.value=value;fields[name]=input;wrapper.append(input);form.append(wrapper);return input;}
  field("domain","Original task domain",original?.domain||"");field("profile","Original routing profile",original?.profile||"");field("difficulty","Original difficulty",original?.difficulty||"unknown",["unknown","easy","medium","hard"].map(x=>[x,x]));
  field("context","Original context tokens",original?String(original.context_tokens):"").type="number";const cost=field("cost","Original maximum task cost",original?String(original.max_cost):"");cost.type="number";cost.step="any";
  field("private","Original privacy requirement",original?String(original.private):"true",[["true","Private network and local inference"],["false","Explicit peer policy"]]);field("capabilities","Original required capabilities (comma-separated)",original?.capabilities.join(",")||"");field("prompt","Original instructions",original?.prompt||"");
  inspect.type=prepare.type=confirm.type="button";inspect.textContent="Check current review";prepare.textContent="Review AI evaluation request";confirm.textContent="Confirm one AI evaluation";confirm.hidden=true;
  function read(){const request={version:1,request_id:key,domain:fields.domain.value.trim(),profile:fields.profile.value.trim(),difficulty:fields.difficulty.value,context_tokens:Number(fields.context.value),max_cost:Number(fields.cost.value),private:fields.private.value==="true",capabilities:fields.capabilities.value.split(",").map(x=>x.trim()).filter(Boolean),prompt:fields.prompt.value};if(!request.domain||!request.profile||!request.prompt.trim()||!fields.context.value||!fields.cost.value||!Number.isSafeInteger(request.context_tokens)||request.context_tokens<8192||!Number.isFinite(request.max_cost)||request.max_cost<0)throw Error();return request;}
  function invalidate(){reviewed=null;confirm.hidden=true;preview.textContent="";}form.addEventListener("input",invalidate);form.addEventListener("change",invalidate);form.addEventListener("submit",e=>e.preventDefault());
  function lock(value){busy=value;inspect.disabled=value;prepare.disabled=value||attempted;confirm.disabled=value;Object.values(fields).forEach(f=>f.disabled=value);}
  async function send(action,request){
   if(busy)return;lock(true);notice.textContent=action==="status"?"Checking current review evidence…":"Evaluating the authenticated completed output…";
   try{const r=await fetch(base+"/api/v1/remote-auto-review",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({action,request})});if(!r.ok)throw Error();const p=await r.json();if(!p||p.version!==1||p.request_id!==key||typeof p.status!=="string")throw Error();
    if(action==="status"&&p.status==="recorded"&&p.verdict)attempted=true;
    if(action==="status")notice.textContent=p.status==="unrecorded"?"No recorded quality evidence for this completed output.":"Current evidence: "+(p.classification||"unknown")+"; verdict: "+(p.verdict||"none")+"; method: "+(p.method||"none")+".";
    else notice.textContent="Evaluation attempt: "+p.status+". Review applied: "+(p.review_applied?"yes":"no")+". Check current review to see the latest evidence; no repeat evaluation was sent.";
   }catch{notice.textContent=action==="status"?"Review status is unavailable. Verify the original requirements and permissions. No evaluation was requested.":"Evaluation could not be confirmed. It may have started. Check current review; this page will not repeat the evaluation.";}
   finally{lock(false);}
  }
  inspect.addEventListener("click",()=>{if(busy)return;invalidate();try{send("status",read());}catch{notice.textContent="Supply the exact original task requirements first.";}});
  prepare.addEventListener("click",()=>{if(busy||attempted)return;try{reviewed=read();preview.textContent=JSON.stringify(reviewed,null,2);confirm.hidden=false;notice.textContent="Confirm evaluation of this original request using the configured AI reviewer. Its output is advisory and consumes the configured review budget.";}catch{notice.textContent="Supply the exact original task requirements first.";}});
  confirm.addEventListener("click",()=>{if(busy||attempted||!reviewed||confirm.hidden)return;const request=reviewed;attempted=true;invalidate();send("evaluate",request);});
  form.append(inspect,prepare,preview,confirm);card.append(title,explain,form,notice);parent.append(card);
 }
 return {attach};
})();
