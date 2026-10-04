"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(window.location.pathname!==base+"/models")return;
 const root=document.querySelector('#remote-model-hosts'),state=document.querySelector('#remote-models-state'),hosts=new Map();let busy=false;
 const text=(v,n=512)=>typeof v==='string'&&v.length>0&&v.length<=n&&!/[\x00-\x1f\x7f-\x9f]/.test(v);
 const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
 const node=(tag,value)=>{const e=document.createElement(tag);if(value!==undefined)e.textContent=value;return e;};
 const set=(e,v)=>{if(e.textContent!==v)e.textContent=v;};
 async function json(path,options={}) {
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),12000);
  try{const r=await window.NexusLive.fetch(base+path,{credentials:'same-origin',cache:'no-store',...options,signal:controller.signal});if(!r.ok)throw Error();return await r.json();}finally{clearTimeout(timer);}
 }
 function validInfo(v,peer){
  const i=v?.info;
  return v?.version===1&&Number.isFinite(Date.parse(v.observed_at))&&i?.version===1&&i.instance===peer&&typeof i.available==='boolean'&&
   (i.hostname===undefined||typeof i.hostname==='string'&&/^[A-Za-z0-9._-]{0,253}$/.test(i.hostname))&&Array.isArray(i.models)&&i.models.length<=4096&&new Set(i.models.map(m=>m?.id)).size===i.models.length&&i.models.every(m=>m&&text(m.id,128)&&text(m.provider,128)&&text(m.model)&&typeof m.local==='boolean'&&Number.isSafeInteger(m.context_tokens)&&m.context_tokens>=0&&(m.capabilities==null||Array.isArray(m.capabilities)&&m.capabilities.length<=128&&m.capabilities.every(c=>text(c,128)))&&(!m.observation||['present','absent','unknown'].includes(m.observation.state)&&Number.isFinite(Date.parse(m.observation.checked_at))));
 }
 function host(peer){
  let h=hosts.get(peer.id);if(h)return h;
  const el=node('section'),title=node('h3',peer.id),status=node('p','Checking availability…'),list=node('ul');list.className='model-cards';el.append(title,status,list);root.append(el);h={el,title,status,list,rows:new Map()};hosts.set(peer.id,h);return h;
 }
 function render(h,v){
  const info=v.info;set(h.title,info.hostname||info.instance);
  set(h.status,(info.available?'Connected':'Host unavailable')+' · '+info.models.length+' shared models · Checked '+new Date(v.observed_at).toLocaleString());
  const keep=new Set();
  for(const m of info.models){
   keep.add(m.id);let row=h.rows.get(m.id);
   if(!row){const el=node('li'),details=node('details'),summary=node('summary'),facts=node('p');el.className='model-card';details.append(summary,facts);el.append(details);h.list.append(el);row={el,summary,facts};h.rows.set(m.id,row);}
   const observation=m.observation?.state||'unknown';
   set(row.summary,m.model+' · '+(m.local?'Remote local model':'Remote cloud model')+' · '+(observation==='present'?'Present':observation==='absent'?'Not present':'Availability unknown'));
   set(row.facts,'Provider: '+m.provider+' · Model ID: '+m.id+' · Context: '+(m.context_tokens||'Unknown')+' · Capabilities: '+(m.capabilities?.join(', ')||'None declared')+(m.observation?' · Observed '+new Date(m.observation.checked_at).toLocaleString():''));
  }
  for(const [key,row]of h.rows)if(!keep.has(key)){row.el.remove();h.rows.delete(key);}
  if(!info.models.length)set(h.status,'No models shared with this connection. Checked '+new Date(v.observed_at).toLocaleString());
 }
 async function load(){
  if(busy||document.hidden)return;busy=true;
  try{
   const page=await json('/api/v1/remote-membership');
   if(page?.version!==1||typeof page.enabled!=='boolean'||page.enabled&&(!Array.isArray(page.registry?.peers)||page.registry.peers.length>128||page.registry.peers.some(p=>!id(p.id)||!Array.isArray(p.operations)||p.operations.some(o=>!text(o,64)))||new Set(page.registry.peers.map(p=>p.id)).size!==page.registry.peers.length))throw Error();
   const peers=page.enabled?page.registry.peers:[],keep=new Set(peers.map(p=>p.id));
   for(const [key,h]of hosts)if(!keep.has(key)){h.el.remove();hosts.delete(key);}
   if(!peers.length){set(state,'No paired remote systems. Add a connection in Settings.');return;}
   let csrf='';if(page.inspection_enabled){const session=await json('/api/v1/session/csrf',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({version:1})});if(session.version===1&&text(session.csrf_token,4096))csrf=session.csrf_token;}
   let next=0,failed=0;
   async function worker(){while(next<peers.length){const peer=peers[next++],h=host(peer);
    if(!csrf||!peer.operations.includes('info')){set(h.status,'Model inspection is not permitted for this connection.');h.list.replaceChildren();h.rows.clear();failed++;continue;}
    try{const v=await json('/api/v1/remote-inspection',{method:'POST',headers:{'Content-Type':'application/json','X-Darwin-CSRF':csrf},body:JSON.stringify({version:1,instance:peer.id,view:'info'})});if(!validInfo(v,peer.id))throw Error();render(h,v);}
    catch{failed++;set(h.status,'Inventory unavailable. Previously displayed models are not currently verified.');h.list.replaceChildren();h.rows.clear();}
   }}
   await Promise.all(Array.from({length:Math.min(4,peers.length)},worker));set(state,failed?'Remote inventory incomplete: '+failed+' connection(s) unavailable or not permitted.':'Remote inventory is current. Updates automatically.');
  }catch{set(state,'Remote inventory unavailable. Last displayed observations may be stale.');}finally{busy=false;}
 }
 document.querySelector('#refresh-models').addEventListener('click',load);
 window.NexusLive.watch('remote-models',load,{interval:10000});load();
})();
