"use strict";
(()=>{
const base=document.body.dataset.basePath||"";if(location.pathname!==base+"/dependencies")return;
for(const n of document.querySelectorAll("main > section"))n.hidden=n.id!=="dependencies-view";
document.title="Dependencies · NexusRouter";
const root=document.querySelector("#dependencies-items"),status=document.querySelector("#dependencies-status"),button=document.querySelector("#dependencies-refresh");let busy=false;
const set=(n,v)=>{if(n.textContent!==v)n.textContent=v;};
async function load(){if(busy)return;busy=true;button.disabled=true;const c=new AbortController(),timer=setTimeout(()=>c.abort(),10000);
try{const r=await window.NexusLive.fetch(base+"/api/v1/dependencies",{credentials:"same-origin",cache:"no-store",signal:c.signal});if(!r.ok){if([401,403].includes(r.status))root.replaceChildren();throw Error();}
const p=await r.json();if(p.version!==1||!Number.isFinite(Date.parse(p.observed_at))||!Array.isArray(p.items)||p.items.length>1024||new Set(p.items.map(x=>x.id)).size!==p.items.length||!p.items.every(x=>/^[A-Za-z0-9_-]{1,128}$/.test(x.id)&&[x.name,x.status,x.scope,x.location,x.note].every(v=>typeof v==="string"&&v.length>0&&v.length<=4096)))throw Error();
const old=new Map([...root.children].map(n=>[n.dataset.id,n]));
for(const x of p.items){let n=old.get(x.id);if(!n){n=document.createElement("article");n.className="logging-card";n.dataset.id=x.id;for(const tag of ["h2","p","code","p"]){const e=document.createElement(tag);e.className=tag==="code"?"logging-path":"";n.append(e);}root.append(n);}old.delete(x.id);[x.name,x.status+" · "+x.scope,x.location,x.note].forEach((v,i)=>set(n.children[i],v));}for(const n of old.values())n.remove();set(status,p.items.length+" dependencies checked · "+new Date(p.observed_at).toLocaleString());
}catch{set(status,"Dependency check unavailable. Previous results may be stale.");}finally{clearTimeout(timer);busy=false;button.disabled=false;}}
button.addEventListener("click",load);load();
})();
