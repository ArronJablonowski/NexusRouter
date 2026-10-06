"use strict";
(() => {
 const base=document.body.dataset.basePath||"";if(location.pathname!==base+"/logging")return;
 for(const node of document.querySelectorAll("main > section"))node.hidden=node.id!=="logging-view";
 document.title="Logging · NexusRouter";
 const root=document.querySelector("#logging-locations"),status=document.querySelector("#logging-status"),refresh=document.querySelector("#logging-refresh");let busy=false;
 const text=v=>typeof v==="string"&&v.length>0&&v.length<=4096;
 function valid(p){return p?.version===1&&Number.isFinite(Date.parse(p.observed_at))&&Array.isArray(p.items)&&p.items.length<=32&&new Set(p.items.map(x=>x?.id)).size===p.items.length&&p.items.every(x=>x&&/^[A-Za-z0-9_-]{1,128}$/.test(x.id)&&[x.name,x.location,x.scope,x.format,x.status,x.note].every(text)&&Array.isArray(x.records)&&x.records.length>0&&x.records.length<=16&&x.records.every(text));}
 const el=(tag,cls)=>{const n=document.createElement(tag);if(cls)n.className=cls;return n;};
 const set=(n,v)=>{if(n.textContent!==v)n.textContent=v;};
 function render(items){const existing=new Map([...root.children].map(n=>[n.dataset.id,n]));for(const x of items){let card=existing.get(x.id);if(!card){card=el("article","logging-card");card.dataset.id=x.id;card.append(el("h2"),el("p","logging-meta"),el("code","logging-path"),el("ul"),el("p","help"));root.append(card);}existing.delete(x.id);set(card.children[0],x.name);set(card.children[1],x.scope+" · "+x.format+" · "+x.status);set(card.children[2],x.location);const list=card.children[3];x.records.forEach((v,i)=>{if(!list.children[i])list.append(el("li"));set(list.children[i],v);});while(list.children.length>x.records.length)list.lastChild.remove();set(card.children[4],x.note);}for(const n of existing.values())n.remove();}
 async function load(){if(busy)return;busy=true;refresh.disabled=true;const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),10000);try{const r=await window.NexusLive.fetch(base+"/api/v1/logging",{credentials:"same-origin",cache:"no-store",signal:controller.signal});if(!r.ok){if([401,403].includes(r.status))root.replaceChildren();throw Error();}const p=await r.json();if(!valid(p))throw Error();render(p.items);set(status,"Storage metadata checked "+new Date(p.observed_at).toLocaleString()+". File presence does not confirm active logging.");}catch{set(status,"Logging metadata unavailable. Previous observations may be stale.");}finally{clearTimeout(timer);busy=false;refresh.disabled=false;}}
 refresh.addEventListener("click",load);load();
})();
