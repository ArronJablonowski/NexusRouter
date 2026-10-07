"use strict";
(()=>{
const base=document.body.dataset.basePath||"";if(location.pathname!==base+"/dependencies")return;
for(const n of document.querySelectorAll("main > section"))n.hidden=n.id!=="dependencies-view";
document.title="Dependencies · NexusRouter";
const root=document.querySelector("#dependencies-items"),status=document.querySelector("#dependencies-status"),button=document.querySelector("#dependencies-refresh"),hosts=new Map();let busy=false;
root.className="dependency-hosts";
const set=(n,v)=>{if(n.textContent!==v)n.textContent=v;};
const text=(v,max=4096)=>typeof v==="string"&&v.length>0&&v.length<=max&&!/[\x00-\x1f\x7f-\x9f]/.test(v);
const valid=p=>p&&p.version===1&&Number.isFinite(Date.parse(p.observed_at))&&(p.hostname===undefined||text(p.hostname,253))&&Array.isArray(p.items)&&p.items.length<=1024&&new Set(p.items.map(x=>x.id)).size===p.items.length&&p.items.every(x=>/^[A-Za-z0-9_-]{1,128}$/.test(x.id)&&[x.name,x.status,x.scope,x.location,x.note].every(v=>text(v)));
function host(id,label){let h=hosts.get(id);if(h)return h;const section=document.createElement('section'),title=document.createElement('h2'),summary=document.createElement('p'),cards=document.createElement('div');section.className='dependency-host';cards.className='logging-grid';summary.className='logging-meta';title.textContent=label;section.append(title,summary,cards);root.append(section);h={section,title,summary,cards};hosts.set(id,h);return h;}
function render(h,p,label){if(!valid(p))throw Error();set(h.title,(p.hostname||label)+(label==='This host'?' · This host':''));const old=new Map([...h.cards.children].map(n=>[n.dataset.id,n]));let missing=0;
for(const x of p.items){let n=old.get(x.id);if(!n){n=document.createElement("article");n.className="logging-card";n.dataset.id=x.id;for(const tag of ["h3","p","code","p"]){const e=document.createElement(tag);e.className=tag==="code"?"logging-path":"";n.append(e);}h.cards.append(n);}old.delete(x.id);const absent=x.status==='Not found'||x.status==='Missing';if(absent)missing++;n.classList.toggle('dependency-missing',absent);n.classList.toggle('dependency-unverified',['Unavailable','Environment check required'].includes(x.status));[x.name,x.status+" · "+x.scope,x.location,x.note].forEach((v,i)=>set(n.children[i],v));}for(const n of old.values())n.remove();set(h.summary,p.items.length+' dependencies · '+missing+' missing · Checked '+new Date(p.observed_at).toLocaleString());}
async function json(path,options={}){const c=new AbortController(),timer=setTimeout(()=>c.abort(),10000);try{const r=await window.NexusLive.fetch(base+path,{credentials:'same-origin',cache:'no-store',...options,signal:c.signal});if(!r.ok)throw Error();return await r.json();}finally{clearTimeout(timer);}}
async function load(){if(busy)return;busy=true;button.disabled=true;let failed=0;const local=host('local','This host');
try{try{render(local,await json('/api/v1/dependencies'),'This host');}catch{failed++;set(local.summary,'Dependency check unavailable. Previous results may be stale.');}
try{const membership=await json('/api/v1/remote-membership');if(membership.version!==1||typeof membership.enabled!=='boolean')throw Error();const peers=membership.enabled?membership.registry?.peers:[];if(!Array.isArray(peers)||peers.length>128||peers.some(p=>!text(p.id,64)||!Array.isArray(p.operations)))throw Error();const keep=new Set(['local',...peers.map(p=>p.id)]);for(const [id,h]of hosts)if(!keep.has(id)){h.section.remove();hosts.delete(id);}
const auth=peers.length?await json('/api/v1/session/csrf',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({version:1})}):null;
let next=0;async function worker(){while(next<peers.length){const p=peers[next++],h=host(p.id,p.hostname||p.id);if(!membership.inspection_enabled||!p.operations.includes('inspect')){failed++;set(h.summary,'Dependency inspection is not permitted for this host.');h.cards.replaceChildren();continue;}
try{const page=await json('/api/v1/remote-inspection',{method:'POST',headers:{'Content-Type':'application/json','X-Darwin-CSRF':auth.csrf_token},body:JSON.stringify({version:1,instance:p.id,view:'dependencies'})});render(h,page.dependencies,p.id);}catch{failed++;set(h.summary,'Dependency inventory unavailable. The host may be offline or require an update. Previous results may be stale.');}}}
await Promise.all(Array.from({length:Math.min(4,peers.length)},worker));
}catch{failed++;for(const [id,h]of hosts)if(id!=='local')set(h.summary,'Connection inventory unavailable. Previous results may be stale.');}
set(status,failed?'Checks incomplete: '+failed+' host or connection check(s) unavailable.':'Dependencies grouped by host. Orange indicates missing; amber indicates unverified.');
}finally{busy=false;button.disabled=false;}}
button.addEventListener("click",load);load();
})();
