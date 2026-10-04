"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(location.pathname.slice(base.length)!=="/active-jobs")return;
 const view=document.querySelector("#active-jobs-view");
 if(!view||!window.NexusLive)return;
 for(const section of document.querySelectorAll("#main > section"))section.hidden=section!==view;
 document.title="Active jobs · NexusRouter";
 function render(source,items,incomplete){
  const list=document.querySelector("#"+source+"-jobs-list"),status=document.querySelector("#"+source+"-jobs-status"),count=document.querySelector("#"+source+"-jobs-count");
  const nodes=[];
  for(const item of items){
   const row=document.createElement("li"),link=document.createElement(item.session_id||item.remote?"a":"strong"),meta=document.createElement("span");
   if(item.session_id)link.href=base+"/chats/"+encodeURIComponent(item.session_id);
   if(item.remote)link.href=base+"/settings#"+new URLSearchParams({remote_peer:item.remote,remote_request:item.task_id}).toString();
   link.textContent=item.task_id;
   meta.textContent=item.remote?item.remote+" · "+item.state+" · checked "+new Date(item.observed_at).toLocaleTimeString():(item.state==="queued"?"Queued · submitted ":"Running · started ")+new Date(item.started_at).toLocaleString();
   row.append(link,meta);nodes.push(row);
  }
  list.replaceChildren(...nodes);count.textContent=String(items.length);
  status.textContent=incomplete?"Inventory incomplete; some jobs may be missing.":items.length?"Updated "+new Date().toLocaleTimeString():"No active "+source+" jobs.";
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
     if(task.state==="queued"||task.state==="running")found.push({task_id:task.request_id,state:task.state,remote:peer.id,observed_at:value.observed_at});
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
 window.NexusLive.watch("local-active-jobs",loadLocal,{interval:5000,immediate:true});
 window.NexusLive.watch("remote-active-jobs",loadRemote,{interval:5000,immediate:true});
})();
