"use strict";
window.NexusRemoteEvents=(()=>{
 function attach(parent,instance,request,task,base,csrf){
  const card=document.createElement("section"),title=document.createElement("h5"),refresh=document.createElement("button"),next=document.createElement("button"),status=document.createElement("output"),list=document.createElement("ul");
  title.textContent="Progress: "+task;refresh.type=next.type="button";refresh.textContent="Refresh progress";next.textContent="Next progress page";next.hidden=true;status.setAttribute("role","status");status.setAttribute("aria-live","polite");status.textContent="Load lifecycle events. Result text is available from task status.";
  let busy=false,cursor=0;
  async function load(after){
   if(busy)return;busy=true;refresh.disabled=next.disabled=true;next.hidden=true;list.replaceChildren();status.textContent="Loading progress…";
   try{
    const response=await fetch(base+"/api/v1/remote-task-events",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({version:1,instance,request_id:request,task_id:task,after})});
    if(!response.ok)throw Error();const p=await response.json();
    if(!p||p.version!==1||p.instance!==instance||p.request_id!==request||p.task_id!==task||p.from_sequence!==after||!Number.isSafeInteger(p.next_sequence)||!Number.isSafeInteger(p.head_sequence)||p.head_sequence<1||p.head_sequence>10000||p.next_sequence<after||p.next_sequence>p.head_sequence||p.has_more!==(p.next_sequence<p.head_sequence)||!["running","completed","failed","canceled"].includes(p.state)||!Array.isArray(p.events)||p.events.length>100||p.events.length!==p.next_sequence-after)throw Error();
    const rows=p.events.map((e,i)=>{if(e.sequence!==after+i+1||typeof e.kind!=="string"||typeof e.time!=="string"||!Number.isFinite(Date.parse(e.time)))throw Error();const row=document.createElement("li");row.textContent=e.sequence+" · "+e.kind+" · "+e.time;return row;});
    list.append(...rows);cursor=p.next_sequence;next.hidden=!p.has_more;status.textContent="State: "+p.state+". "+(rows.length?"Showing events "+(after+1)+"–"+cursor:"No newer events")+". Latest observed event: "+p.head_sequence+". Refresh starts from the first page.";
   }catch{cursor=0;next.hidden=true;list.replaceChildren();status.textContent="Progress unavailable. Refresh to recheck access and connectivity. No work was dispatched or retried.";}
   finally{busy=false;refresh.disabled=next.disabled=false;}
  }
  refresh.addEventListener("click",()=>load(0));next.addEventListener("click",()=>{if(!next.hidden)load(cursor);});card.append(title,refresh,next,status,list);parent.append(card);
 }
 return {attach};
})();
