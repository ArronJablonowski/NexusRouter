"use strict";
(() => {
 const base=document.body.dataset.basePath||'';if(window.location.pathname!==base+'/cron')return;
 document.querySelector('#chat-view').hidden=true;document.querySelector('#cron-view').hidden=false;document.title='Cron · NexusRouter';
 const localState=document.querySelector('#cron-local-state'),localList=document.querySelector('#cron-local-list'),remoteState=document.querySelector('#cron-remote-state'),remoteRoot=document.querySelector('#cron-remote-hosts'),osRoot=document.querySelector('#cron-os-hosts');
 const hosts=new Map();let busy=false;
 const node=(tag,value)=>{const e=document.createElement(tag);if(value!==undefined)e.textContent=value;return e;};
 const set=(e,v)=>{if(e.textContent!==v)e.textContent=v;};
 const text=(v,max=1024)=>typeof v==='string'&&v.length<=max&&!/[\x00-\x1f\x7f-\x9f]/.test(v);
 const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
 async function json(path,options={}){const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);try{const r=await window.NexusLive.fetch(base+path,{credentials:'same-origin',cache:'no-store',...options,signal:controller.signal});if(!r.ok)throw Error();return await r.json();}finally{clearTimeout(timer)}}
 function valid(p,os=false){return p?.version===1&&Number.isFinite(Date.parse(p.observed_at))&&Array.isArray(p.items)&&p.items.length<=(os?512:64)&&new Set(p.items.map(x=>x?.id)).size===p.items.length&&p.items.every(x=>x&&text(x.id)&&x.id.length>0&&(os?['name','source','schedule','state'].every(k=>text(x[k])):text(x.description,256)&&typeof x.enabled==='boolean'&&text(x.interval,64)))&&(!os||Array.isArray(p.limitations)&&p.limitations.length<=16&&p.limitations.every(x=>text(x)));}
 function cadence(value){return value.replace(/(\d+(?:\.\d+)?)h/g,'$1 hours ').replace(/(\d+(?:\.\d+)?)m(?!s)/g,'$1 minutes ').replace(/(\d+(?:\.\d+)?)s/g,'$1 seconds ').trim().replace(/^1 hours$/,'1 hour').replace(/^1 minutes$/,'1 minute').replace(/^1 seconds$/,'1 second');}
 function scheduleLabel(value){if(value.startsWith('Next ')){const d=new Date(value.slice(5));if(Number.isFinite(d.getTime()))return 'Next run · '+d.toLocaleString(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});}return value.replace(/^Every (\d+) seconds/,(_,n)=>Number(n)%3600===0?'Every '+cadence(Number(n)/3600+'h'):Number(n)%60===0?'Every '+cadence(Number(n)/60+'m'):'Every '+cadence(n+'s'));}
 function rows(list,p,os=false){
  const existing=new Map(Array.from(list.children,e=>[e.dataset.key,e])),keep=new Set();
  for(const x of p.items){let e=existing.get(x.id);if(!e){e=node('li');e.className='cron-card';e.dataset.key=x.id;const top=node('div');top.className='cron-card-heading';const title=node('strong'),badge=node('span');badge.className='cron-badge';top.append(title,badge);const timing=node('p');timing.className='cron-timing';const detail=node('p');detail.className='cron-detail';e.append(top,timing,detail);list.append(e)}keep.add(x.id);
   const [top,timing,detail]=e.children;set(top.firstChild,os?x.name:x.description);set(top.lastChild,os?'OS schedule':x.enabled?'Enabled':'Disabled');top.lastChild.className='cron-badge '+(os?'system':x.enabled?'enabled':'disabled');e.classList.toggle('cron-card-disabled',!os&&!x.enabled);
   set(timing,os?scheduleLabel(x.schedule):'Every '+cadence(x.interval));set(detail,os?x.source+' · '+x.state:'NexusRouter interval schedule');
  }
  for(const [key,e]of existing)if(!keep.has(key))e.remove();
 }
 function section(root,name){const el=node('section'),title=node('h3',name),status=node('p','Loading…'),list=node('ul');el.className='cron-host';status.className='cron-observation';list.className='cron-cards';el.append(title,status,list);root.append(el);return{el,title,status,list}}
 const localOS=section(osRoot,'Local operating-system schedules');
 async function local(){try{const p=await json('/api/v1/schedules');if(!valid(p))throw Error();rows(localList,p);set(localState,p.items.length?'Updated '+new Date(p.observed_at).toLocaleString():'No NexusRouter schedules configured.')}catch{set(localState,'Schedules unavailable. Previously displayed configuration may be stale.')}}
 async function osLocal(){try{const p=await json('/api/v1/os-schedules');if(!valid(p,true))throw Error();rows(localOS.list,p,true);set(localOS.status,(p.items.length?p.items.length+' schedules. ':'No schedules found. ')+p.limitations.join(' '))}catch{set(localOS.status,'Operating-system schedules unavailable. Previous observations may be stale.')}}
 async function remotes(){try{
  const p=await json('/api/v1/remote-membership');if(p?.version!==1||typeof p.enabled!=='boolean')throw Error();const peers=p.enabled?p.registry?.peers:[];
  if(!Array.isArray(peers)||peers.length>128||new Set(peers.map(x=>x.id)).size!==peers.length||peers.some(x=>!id(x.id)||!Array.isArray(x.operations)||x.operations.some(o=>!text(o,64))))throw Error();
  const keep=new Set(peers.map(x=>x.id));for(const [key,h]of hosts)if(!keep.has(key)){h.app.el.remove();h.os.el.remove();hosts.delete(key)}
  if(!peers.length){set(remoteState,'No paired remote systems.');return}
  let csrf='';if(p.inspection_enabled){const s=await json('/api/v1/session/csrf',{method:'POST',headers:{'Content-Type':'application/json'},body:'{"version":1}'});if(s.version===1&&text(s.csrf_token,4096))csrf=s.csrf_token}
  let next=0,unavailable=0;
  const inspect=(peer,view)=>json('/api/v1/remote-inspection',{method:'POST',headers:{'Content-Type':'application/json','X-Darwin-CSRF':csrf},body:JSON.stringify({version:1,instance:peer.id,view})});
  async function worker(){while(next<peers.length){const peer=peers[next++];let h=hosts.get(peer.id);if(!h){h={app:section(remoteRoot,peer.id+' · Remote'),os:section(osRoot,peer.id+' · Remote operating-system schedules')};hosts.set(peer.id,h)}
   if(csrf&&peer.operations.includes('info')){try{const v=await inspect(peer,'info');if(v.version!==1||v.info?.instance!==peer.id||!valid(v.info.schedules))throw Error();const name=text(v.info.hostname,253)&&v.info.hostname?v.info.hostname:peer.id;set(h.app.title,name+' · Remote');set(h.os.title,name+' · Remote operating-system schedules');rows(h.app.list,v.info.schedules);set(h.app.status,v.info.schedules.items.length?'Updated '+new Date(v.observed_at).toLocaleString():'No NexusRouter schedules on this host.')}catch{unavailable++;h.app.list.replaceChildren();set(h.app.status,'Schedule inspection unavailable or unsupported by this host.')}}else{unavailable++;h.app.list.replaceChildren();set(h.app.status,'Schedule inspection not permitted.')}
   if(csrf&&peer.operations.includes('inspect')){try{const v=await inspect(peer,'os_schedules');if(v.version!==1||!valid(v.os_schedules,true))throw Error();rows(h.os.list,v.os_schedules,true);set(h.os.status,(v.os_schedules.items.length?v.os_schedules.items.length+' schedules. ':'No schedules found. ')+v.os_schedules.limitations.join(' '))}catch{h.os.list.replaceChildren();set(h.os.status,'Operating-system inspection unavailable or unsupported by this host.')}}else{h.os.list.replaceChildren();set(h.os.status,'Operating-system inspection not permitted.')}
  }}
  await Promise.all(Array.from({length:Math.min(4,peers.length)},worker));set(remoteState,unavailable?'Some remote schedules are unavailable.':'Remote NexusRouter schedules updated.');
 }catch{set(remoteState,'Remote schedule inventory unavailable. Previous observations may be stale.')}}
 async function load(){if(busy||document.hidden)return;busy=true;try{await Promise.all([local(),osLocal(),remotes()])}finally{busy=false}}
 document.querySelector('#refresh-cron').addEventListener('click',load);window.NexusLive.watch('cron',load,{interval:30000});load();
})();
