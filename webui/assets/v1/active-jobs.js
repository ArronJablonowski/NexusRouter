"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(location.pathname.slice(base.length)!=="/active-jobs")return;
 const view=document.querySelector("#active-jobs-view");
 if(!view||!window.NexusLive)return;
 for(const section of document.querySelectorAll("#main > section"))section.hidden=section!==view;
 document.title="Active jobs · NexusRouter";
 const descriptions=new Map();
 function brief(text){const clean=text.replace(/\s+/g," ").trim();return clean.length>150?clean.slice(0,147)+"…":clean;}
 async function describe(item){
  if(!item.session_id)return item.description?{description:item.description,instructions:item.description}:null;
  const key=item.session_id+":"+item.task_id;
  if(descriptions.has(key))return descriptions.get(key);
  const response=await window.NexusLive.fetch(base+"/api/v1/chats/"+encodeURIComponent(item.session_id)+"/messages?limit=100",{credentials:"same-origin",cache:"no-store"});
  if(!response.ok)throw Error();const page=await response.json();
  if(page.version!==1||page.chat_id!==item.session_id||page.task_id!==item.task_id||!Array.isArray(page.messages)||page.messages.length>100)throw Error();
  const first=page.messages.find(m=>m.role==="user"&&typeof m.text==="string"&&m.text.trim());
  const detail=first?{description:brief(first.text),instructions:first.text.slice(0,16000)}:null;
  if(descriptions.size>=200)descriptions.delete(descriptions.keys().next().value);
  descriptions.set(key,detail);return detail;
 }
 const dialog=document.createElement("dialog"),heading=document.createElement("h2"),close=document.createElement("button"),content=document.createElement("div");
 heading.id="job-card-title";heading.textContent="Job details";dialog.setAttribute("aria-labelledby",heading.id);dialog.className="job-detail-dialog";
 close.type="button";close.textContent="Close";close.addEventListener("click",()=>dialog.close());dialog.append(heading,close,content);document.body.append(dialog);
 let cardGeneration=0,openJobKey="",openStatus=null,openEvidence=null,openObserved=null;
 const jobKey=item=>(item.remote||"local")+":"+item.task_id;
 const stateNames={running:"Running",queued:"Queued",failed:"Failed",canceled:"Canceled",succeeded:"Succeeded",completed:"Completed",unknown:"Needs attention — execution unconfirmed"};
 const stateText=item=>stateNames[item.current_state||item.state]||"Status unavailable";
 function text(node,value){if(node.textContent!==value)node.textContent=value;}
 const jobRows={local:new Map(),remote:new Map()};
 const streams=new Map();
 function syncStreams(items){
  if(!window.EventSource)return;
  const sessions=new Set(items.filter(item=>item.session_id&&item.current_state==="running").slice(0,16).map(item=>item.session_id));
  for(const [id,stream] of streams)if(!sessions.has(id)){stream.close();streams.delete(id);}
  for(const id of sessions)if(!streams.has(id)){
   const stream=new window.EventSource(base+"/api/v1/chats/"+encodeURIComponent(id)+"/events");
   for(const event of ["task.terminal","lifecycle.event","worker.changed"])stream.addEventListener(event,()=>window.NexusLive.wake());
   streams.set(id,stream);
  }
 }
 if(window.addEventListener)window.addEventListener("pagehide",()=>{for(const stream of streams.values())stream.close();streams.clear();});
 dialog.addEventListener("close",()=>{cardGeneration++;openJobKey="";openStatus=null;content.replaceChildren();});
 async function showJob(item){
  const generation=++cardGeneration;openJobKey=jobKey(item);openStatus=null;openEvidence=null;openObserved=null;content.replaceChildren();heading.textContent="Job details";
  const fields=document.createElement("dl"),instructions=document.createElement("p");instructions.textContent="Loading recorded instructions…";
  for(const [label,value] of [["Host",item.remote||"Local host"],["Current status",stateText(item)],["Status evidence",item.status_evidence],["Recorded task state",item.state],["Job ID",item.task_id],["Session",item.session_id],["Started / submitted",item.started_at],["Last observed",item.observed_at],["Task IDs",(item.task_ids||[]).join(", ")]]){if(!value)continue;const term=document.createElement("dt"),description=document.createElement("dd");term.textContent=label;description.textContent=value;if(label==="Current status")openStatus=description;if(label==="Status evidence")openEvidence=description;if(label==="Last observed")openObserved=description;fields.append(term,description);}
  content.append(fields,instructions);
  if(item.session_id){const link=document.createElement("a");link.href=base+"/chats/"+encodeURIComponent(item.session_id);link.textContent="Open full conversation";content.append(link);}
  if(!dialog.open)dialog.showModal();close.focus();
  try{const detail=await describe(item);if(generation!==cardGeneration)return;heading.textContent=detail?.description||"Job details";instructions.textContent=detail?.instructions||"Recorded instructions are unavailable for this job.";instructions.className="job-instructions";}catch{if(generation===cardGeneration)instructions.textContent="Recorded instructions could not be loaded.";}
  if(item.remote&&generation===cardGeneration){
   try{const response=await window.NexusLive.fetch(base+"/api/v1/session/csrf",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});if(!response.ok)throw Error();const session=await response.json();if(session.version!==1||typeof session.csrf_token!=="string"||!session.csrf_token)throw Error();if(generation!==cardGeneration)return;
    const controls=document.createElement("details"),summary=document.createElement("summary");summary.textContent="Live status, results and controls";controls.open=true;controls.append(summary);content.append(controls);window.NexusRemoteTaskControls.attach(controls,{id:item.remote},{request_id:item.task_id},base,session.csrf_token);
   }catch{if(generation===cardGeneration){const error=document.createElement("p");error.textContent="Remote details are unavailable. No work was changed.";content.append(error);}}
  }
 }
 async function labelJobs(entries){let index=0;await Promise.all(Array.from({length:Math.min(4,entries.length)},async()=>{while(index<entries.length){const {item,button}=entries[index++];if(!button.isConnected)continue;try{const detail=await describe(item);if(button.isConnected&&detail)button.textContent=detail.description;}catch{if(button.isConnected)button.textContent="Job description unavailable";}}}));}
 function render(source,items,incomplete){
  const list=document.querySelector("#"+source+"-jobs-list"),status=document.querySelector("#"+source+"-jobs-status"),count=document.querySelector("#"+source+"-jobs-count");
  const labels=[],keep=new Set(),records=jobRows[source];
  for(const [index,item] of items.entries()){
   const key=jobKey(item);keep.add(key);let record=records.get(key);
   if(!record){
    const row=document.createElement("li"),link=document.createElement("button"),meta=document.createElement("span");
    record={row,link,meta,item};records.set(key,record);
    link.type="button";link.className="job-summary-button";link.textContent=item.description||(item.session_id?"Loading job description…":"Job description unavailable");link.addEventListener("click",()=>showJob(record.item));row.append(link,meta);
    if(item.session_id)labels.push({item,button:link});
   }
   record.item=item;
   const label=item.remote?item.remote+" · "+item.state+" · checked "+new Date(item.observed_at).toLocaleTimeString():stateText(item)+" · started "+new Date(item.started_at).toLocaleString();
   text(record.meta,label);
   if(list.children[index]!==record.row)list.insertBefore(record.row,list.children[index]||null);
   if(openJobKey===key&&openStatus){text(openStatus,stateText(item));if(openEvidence)text(openEvidence,item.status_evidence||"Unavailable");if(openObserved)text(openObserved,item.observed_at||"Unavailable");}
  }
  for(const [key,record] of records)if(!keep.has(key)){list.removeChild(record.row);records.delete(key);if(openJobKey===key&&openStatus)text(openStatus,"No longer listed as active");}
  text(count,String(items.length));labelJobs(labels);
  if(source==="local")syncStreams(items);
  status.textContent=incomplete?"Inventory incomplete; some jobs may be missing.":items.length?(source==="local"?items.filter(item=>["running","queued"].includes(item.current_state)).length+" active · ":"")+"Updated "+new Date().toLocaleTimeString():"No active "+source+" jobs.";
  status.classList.toggle("error",incomplete);
 }
 function unavailable(source){const status=document.querySelector("#"+source+"-jobs-status");status.textContent="Jobs unavailable. Previously displayed jobs may be stale; retrying automatically.";status.classList.add("error");}
 const id=/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
 async function remoteJobs(signal){
  const request=async(path,body)=>{const response=await window.NexusLive.fetch(base+path,{signal,credentials:"same-origin",cache:"no-store",...(body?{method:"POST",headers:{"Content-Type":"application/json",...(body.csrf?{"X-Darwin-CSRF":body.csrf}:{})},body:JSON.stringify(body.value||body)}:{})});if(!response.ok)throw Error();return response.json();};
  const membership=await request("/api/v1/remote-membership");
  if(membership.version!==1||typeof membership.enabled!=="boolean")throw Error();
  if(!membership.enabled)return {items:[],incomplete:false};
  const peers=membership.registry&&membership.registry.peers;
  if(!Array.isArray(peers)||peers.length>128||new Set(peers.map(p=>p.id)).size!==peers.length||peers.some(p=>!p||!/^[A-Za-z0-9_-]{1,64}$/.test(p.id)||!Array.isArray(p.operations)))throw Error();
  const permitted=peers.filter(p=>p.operations.includes("inspect"));
  if(!permitted.length)return {items:[],incomplete:false};
  if(!membership.inspection_enabled)throw Error();
  const session=await request("/api/v1/session/csrf",{version:1});
  if(session.version!==1||typeof session.csrf_token!=="string"||!session.csrf_token||session.csrf_token.length>4096)throw Error();
  const items=[];let index=0,incomplete=false;
  async function worker(){while(index<permitted.length&&!signal.aborted){const peer=permitted[index++];let after="",finished=false;const found=[];
   try{for(let n=0;n<10;n++){
    const value=await request("/api/v1/remote-inspection",{csrf:session.csrf_token,value:{version:1,instance:peer.id,view:"tasks",after}}),page=value.tasks;
    if(value.version!==1||!Number.isFinite(Date.parse(value.observed_at))||!page||page.version!==1||page.instance!==peer.id||page.after!==after||!Array.isArray(page.tasks)||page.tasks.length>100||typeof page.has_more!=="boolean"||typeof page.next!=="string")throw Error();
    let last=after;
    for(const task of page.tasks){if(!task||!id.test(task.request_id)||task.request_id<=last||!["unknown","queued","running","succeeded","failed","canceled"].includes(task.state))throw Error();last=task.request_id;
     if(task.state==="queued"||task.state==="running")found.push({task_id:task.request_id,state:task.state,remote:peer.id,description:typeof task.description==="string"?brief(task.description):"",task_ids:Array.isArray(task.task_ids)?task.task_ids.filter(x=>typeof x==="string"&&id.test(x)):[],observed_at:value.observed_at});
    }
    if(page.next!==last||page.has_more&&page.tasks.length!==100)throw Error();
    if(!page.has_more){finished=true;break;}after=page.next;
   }
   items.push(...found);if(!finished)incomplete=true;
   }catch{incomplete=true;}
  }}
  await Promise.all(Array.from({length:Math.min(4,permitted.length)},worker));return {items,incomplete:incomplete||signal.aborted};
 }
 async function loadLocal(){
  const items=new Map();let complete=true;
  try{
   for(const kind of ["running","queued","admitted"]){
   let after="",finished=false;const cursors=new Set();
   for(let pageNumber=0;pageNumber<100;pageNumber++){
    const query=[];if(kind!=="running")query.push("kind="+kind);if(after)query.push("after="+encodeURIComponent(after));
    const response=await window.NexusLive.fetch(base+"/api/v1/jobs"+(query.length?"?"+query.join("&"):""),{credentials:"same-origin",cache:"no-store"});
    if(!response.ok)throw Error();const page=await response.json();
    if(page.version!==1||!Array.isArray(page.items)||page.items.length>100||typeof page.has_more!=="boolean"||typeof page.next_cursor!=="string"||page.has_more!==Boolean(page.next_cursor))throw Error();
    for(const raw of page.items){
     if(kind==="admitted"&&Array.isArray(raw.task_ids)&&raw.task_ids.some(task=>items.has("running:"+task)))continue;
     const item=kind!=="running"?{task_id:raw.id,state:raw.state,started_at:raw.created_at}:raw;
     if(!id.test(item.task_id)||(kind==="running"&&!id.test(item.session_id))||item.state!==(kind==="admitted"?"running":kind)||!Number.isFinite(Date.parse(item.started_at)))throw Error();
     if(kind==="running"){
      const execution=raw.execution;
      if(execution&&(!Object.hasOwn(stateNames,execution.state)||typeof execution.evidence!=="string"||!Number.isFinite(Date.parse(execution.observed_at))))throw Error();
      item.current_state=execution?.state||"unknown";item.status_evidence=execution?.evidence||"Execution evidence unavailable";item.observed_at=execution?.observed_at;
     }else{
      item.current_state=kind==="queued"?"queued":raw.lease_expired?"unknown":"running";
      item.status_evidence=kind==="queued"?"queued_submission":raw.lease_expired?"expired_submission_lease":"submission_running";
     }
     if(kind==="running"&&raw.execution?.dismissed===true&&item.current_state==="unknown")continue;
     items.set(kind+":"+item.task_id,item);
    }
    if(!page.has_more){finished=true;break;}
    if(!page.items.length||page.next_cursor.length>1024||cursors.has(page.next_cursor))throw Error();
    after=page.next_cursor;cursors.add(after);
   }
   complete=complete&&finished;
   }
   render("local",Array.from(items.values()),!complete);return true;
  }catch{unavailable("local");return false;}
 }
 async function loadRemote(){
  const controller=new AbortController(),deadline=setTimeout(()=>controller.abort(),15000);
  try{const result=await remoteJobs(controller.signal);render("remote",result.items,result.incomplete);return !result.incomplete;}
  catch{unavailable("remote");return false;}
  finally{clearTimeout(deadline);}
 }
 window.NexusLive.watch("local-active-jobs",loadLocal,{interval:2000,immediate:true});
 window.NexusLive.watch("remote-active-jobs",loadRemote,{interval:2000,immediate:true});
})();
