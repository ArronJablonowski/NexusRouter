"use strict";
window.NexusRemoteEvents=(()=>{
 function attach(parent,instance,request,task,base,csrf){
  const card=document.createElement("section"),title=document.createElement("h5"),refresh=document.createElement("button"),next=document.createElement("button"),status=document.createElement("output"),list=document.createElement("ul");
  title.textContent="Progress: "+task;refresh.type=next.type="button";refresh.textContent="Refresh progress";next.textContent="Next progress page";next.hidden=true;status.setAttribute("role","status");status.setAttribute("aria-live","polite");status.textContent="Load lifecycle events. Result text is available from task status.";
  const follow=document.createElement("button");follow.type="button";follow.textContent="Follow progress";
  let busy=false,cursor=0,following=false,timer=null,generation=0,until=0,head=0;
  function stop(message){following=false;generation++;if(timer!==null)clearTimeout(timer);timer=null;follow.textContent="Follow progress";if(message)status.textContent=message;}
  function visible(){return card.isConnected&&!document.hidden&&card.getClientRects().length>0;}
  function schedule(){
   if(!following)return;
   if(!visible()){stop("Progress following stopped because this view is no longer visible.");return;}
   if(Date.now()>=until){stop("Progress following stopped after 30 minutes. Follow again to continue.");return;}
   timer=setTimeout(()=>{timer=null;if(!following)return;if(!visible()||Date.now()>=until){schedule();return;}load(cursor,true);},5000);
  }
  async function load(after,live=false){
   if(busy)return;const epoch=generation;busy=true;refresh.disabled=next.disabled=true;next.hidden=true;list.replaceChildren();status.textContent="Loading progress…";
   try{
    const grant=await window.NexusLive.fetch(base+"/api/v1/session/csrf",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});
    if(!grant.ok)throw Error();const session=await grant.json();
    if(session.version!==1||typeof session.csrf_token!=="string"||!session.csrf_token||session.csrf_token.length>4096)throw Error();csrf=session.csrf_token;
    if(epoch!==generation)return;
    const response=await fetch(base+"/api/v1/remote-task-events",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({version:1,instance,request_id:request,task_id:task,after})});
    if(!response.ok)throw Error();const p=await response.json();
    if(epoch!==generation)return;
    if(live&&!visible()){stop("Progress following stopped because this view is no longer visible.");return;}
    if(!p||p.version!==1||p.instance!==instance||p.request_id!==request||p.task_id!==task||p.from_sequence!==after||!Number.isSafeInteger(p.next_sequence)||!Number.isSafeInteger(p.head_sequence)||p.head_sequence<1||p.head_sequence>10000||(live&&p.head_sequence<head)||p.next_sequence<after||p.next_sequence>p.head_sequence||p.has_more!==(p.next_sequence<p.head_sequence)||!["running","completed","failed","canceled"].includes(p.state)||!Array.isArray(p.events)||p.events.length>100||p.events.length!==p.next_sequence-after)throw Error();
    const rows=p.events.map((e,i)=>{if(e.sequence!==after+i+1||typeof e.kind!=="string"||typeof e.time!=="string"||!Number.isFinite(Date.parse(e.time)))throw Error();const row=document.createElement("li");row.textContent=e.sequence+" · "+e.kind+" · "+e.time;return row;});
    const finished=live&&p.state!=="running"&&!p.has_more;
    list.append(...rows);cursor=p.next_sequence;head=p.head_sequence;next.hidden=!p.has_more;status.textContent="State: "+p.state+". "+(rows.length?"Showing events "+(after+1)+"–"+cursor:"No newer events")+". Latest observed event: "+p.head_sequence+". "+(live?(finished?"Latest lifecycle page.":"Following new lifecycle events every 5 seconds; only the latest page is shown."):"Refresh starts from the first page.");
    if(finished)stop(status.textContent+" Following finished; load task status for the result.");
   }catch{if(epoch!==generation)return;stop();cursor=0;next.hidden=true;list.replaceChildren();status.textContent="Progress unavailable. Refresh to recheck access and connectivity. No work was dispatched or retried.";}
   finally{busy=false;refresh.disabled=next.disabled=false;if(epoch===generation&&live)schedule();}
  }
  refresh.addEventListener("click",()=>{if(!busy){stop();head=0;load(0);}});
  next.addEventListener("click",()=>{if(!busy&&!next.hidden){stop();load(cursor);}});
  follow.addEventListener("click",()=>{
   if(following){stop("Progress following stopped. This does not cancel the remote task.");return;}
   if(busy)return;following=true;generation++;until=Date.now()+30*60*1000;follow.textContent="Stop following";load(cursor,true);
  });
  card.append(title,refresh,next,status,list,follow);parent.append(card);
 }
 return {attach};
})();
