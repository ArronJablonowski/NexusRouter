"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(!/^\/workboards(?:\/[^/]+)?$/.test(location.pathname.slice(base.length)))return;
 const list=document.querySelector("#active-jobs-list"),status=document.querySelector("#active-jobs-status"),count=document.querySelector("#active-jobs-count");
 if(!list||!status||!count||!window.NexusLive)return;
 const id=/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
 async function load(){
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
   const nodes=[];
   for(const item of items.values()){
    const row=document.createElement("li"),link=document.createElement(item.session_id?"a":"strong"),meta=document.createElement("span");
    if(item.session_id)link.href=base+"/chats/"+encodeURIComponent(item.session_id);link.textContent=item.task_id;
    meta.textContent=(item.state==="queued"?"Queued · submitted ":"Running · started ")+new Date(item.started_at).toLocaleString();
    row.append(link,meta);nodes.push(row);
   }
   list.replaceChildren(...nodes);count.textContent=String(items.size);
   status.textContent=!complete?"Job limit reached; this list is incomplete.":items.size?"Updated "+new Date().toLocaleTimeString():"No active jobs.";
   status.classList.remove("error");return true;
  }catch{status.textContent="Active jobs unavailable. Previously displayed jobs may be stale; retrying automatically.";status.classList.add("error");return false;}
 }
 window.NexusLive.watch("active-jobs",load,{interval:5000,immediate:true});
})();
