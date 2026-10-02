"use strict";
(() => {
 const base=document.body.dataset.basePath||"", relative=window.location.pathname.slice(base.length);
 if(!window.NexusRoutes||!window.NexusRoutes.routing(relative))return;
 const grid=document.querySelector('#remote-route-grid'),status=document.querySelector('#remote-route-status');
 let generation=0,timer=0,controller=null;const views=new Map();
 const text=(value,max=512)=>typeof value==='string'&&value.length>0&&value.length<=max&&!/[\x00-\x1f\x7f]/.test(value);
 function node(tag,cls,value){const el=document.createElement(tag);el.className=cls;if(value!==undefined)el.textContent=value;return el;}
 function redraw(){window.dispatchEvent(new Event('routing-remote-updated'));}
 function validPage(page){return page&&page.version===1&&typeof page.enabled==='boolean'&&(!page.enabled||(page.registry&&page.registry.version===1&&Array.isArray(page.registry.peers)&&page.registry.peers.length<=128&&new Set(page.registry.peers.map(p=>p.id)).size===page.registry.peers.length&&page.registry.peers.every(p=>p&&/^[A-Za-z0-9_-]{1,64}$/.test(p.id)&&text(p.endpoint,2048)&&(!p.transport||['https','ssh'].includes(p.transport))&&Array.isArray(p.operations)&&p.operations.every(x=>text(x,64)))));}
 function validInfo(value,id){return value&&value.version===1&&Number.isFinite(Date.parse(value.observed_at))&&value.info&&value.info.version===1&&value.info.instance===id&&typeof value.info.available==='boolean'&&Array.isArray(value.info.models)&&value.info.models.length<=4096&&value.info.models.every(m=>m&&text(m.id,128)&&text(m.model)&&text(m.provider,128)&&typeof m.local==='boolean');}
 async function json(path,options,signal){const timeout=new AbortController(),abort=()=>timeout.abort();signal.addEventListener('abort',abort,{once:true});if(signal.aborted)abort();const deadline=setTimeout(abort,12000);
  try{const response=await fetch(base+path,{credentials:'same-origin',cache:'no-store',...options,signal:timeout.signal});if(!response.ok)throw Error();return await response.json();}
  finally{clearTimeout(deadline);signal.removeEventListener('abort',abort);}
 }
 function card(peer){
  const el=node('article','remote-route-card'),heading=node('div','specialist-heading'),badge=node('span','remote-route-state','Paired · checking');
  el.dataset.instance=peer.id;heading.append(node('h3','',peer.id),badge);
  const path=node('p','remote-route-path','Commander → '+(peer.transport||'https').toUpperCase()+' → '+peer.id+' → models');
  const endpoint=node('p','remote-route-endpoint',peer.endpoint),detail=node('p','route-empty','Checking the paired system…'),models=node('ul','remote-route-models');
  el.append(heading,path,endpoint,detail,models);grid.append(el);return {el,badge,detail,models};
 }
 async function load(){
  const current=++generation;if(controller)controller.abort();controller=new AbortController();const signal=controller.signal;
  clearTimeout(timer);if(!grid.children.length)status.textContent='Loading paired remote systems…';redraw();
  try{
   const page=await json('/api/v1/remote-membership',{headers:{Accept:'application/json'}},signal);if(current!==generation)return;if(!validPage(page))throw Error();
   if(!page.enabled){views.clear();grid.replaceChildren();status.textContent='Remote connections are not configured. Pair systems in Settings to add routing paths.';return;}
   const peers=page.registry.peers;
   if(!peers.length){views.clear();grid.replaceChildren();status.textContent='No remote systems are paired. Add a connection in Settings.';return;}
   const keys=new Set(peers.map(p=>p.id));for(const [id,view] of views){if(!keys.has(id)){view.el.remove();views.delete(id);}}
   const cards=peers.map(peer=>{const key=JSON.stringify(peer);let view=views.get(peer.id);if(view&&view.key!==key){view.el.remove();views.delete(peer.id);view=null;}if(!view){view=card(peer);view.key=key;views.set(peer.id,view);}return view;});redraw();status.textContent=peers.length+' paired systems · checking connection status…';
   let csrf='';
   if(page.inspection_enabled){
    try{const session=await json('/api/v1/session/csrf',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({version:1})},signal);if(session.version===1&&text(session.csrf_token,4096))csrf=session.csrf_token;}catch{}
   }
   if(current!==generation)return;
   let next=0,connected=0;
   async function worker(){while(next<peers.length&&!signal.aborted){const index=next++,peer=peers[index],view=cards[index];
    if(!csrf||!peer.operations.includes('info')){view.badge.textContent='Paired · not checked';view.detail.textContent='Live inspection is not available for this connection.';continue;}
    try{
     const value=await json('/api/v1/remote-inspection',{method:'POST',headers:{'Content-Type':'application/json',Accept:'application/json','X-Darwin-CSRF':csrf},body:JSON.stringify({version:1,instance:peer.id,view:'info'})},signal);
     if(current!==generation)return;if(!validInfo(value,peer.id))throw Error();
     connected++;view.el.dataset.connected='true';view.badge.textContent=value.info.available?'Connected · available':'Connected · unavailable';
     view.detail.textContent='Checked '+new Date(value.observed_at).toLocaleTimeString()+'. '+(peer.operations.includes('dispatch')?'Task admission is checked again when routing.':'Inspection only; task dispatch is not permitted.');
     const modelKey=JSON.stringify(value.info.models);if(modelKey!==view.modelKey){const scroll=view.models.scrollTop;view.models.replaceChildren();for(const model of value.info.models){const item=node('li','');item.append(node('strong','',model.model),node('small','',model.provider+' · '+model.id+' · '+(model.local?'Runs on this system':'Cloud provider')));view.models.append(item);}
     if(!value.info.models.length)view.models.append(node('li','route-empty','No models visible to this connection.'));view.modelKey=modelKey;view.models.scrollTop=scroll;}
    }catch{if(current!==generation)return;view.el.dataset.connected='false';view.badge.textContent='Connection unavailable';view.detail.textContent='Unreachable, access denied, or invalid response. Availability is unknown.';view.models.replaceChildren();view.modelKey=null;}
    redraw();
   }}
   await Promise.all(Array.from({length:Math.min(4,peers.length)},worker));
   if(current===generation)status.textContent=peers.length+' paired systems · '+connected+' connected · refreshes automatically every 10 seconds. Connection status does not guarantee task capacity.';
  }catch{if(current===generation){views.clear();grid.replaceChildren();status.textContent='Remote routes could not be loaded. Check the browser session and remote settings.';redraw();}}
  finally{if(current===generation&&!document.hidden)timer=setTimeout(load,10000);}
 }
 document.querySelector('#refresh-routing').addEventListener('click',load);
 document.addEventListener('visibilitychange',()=>{clearTimeout(timer);if(document.hidden){generation++;if(controller)controller.abort();status.textContent='Remote connection check paused.';redraw();}else load();});
 window.addEventListener('online',load);window.addEventListener('focus',load);
 window.addEventListener('beforeunload',()=>{clearTimeout(timer);if(controller)controller.abort();});
 load();
})();
