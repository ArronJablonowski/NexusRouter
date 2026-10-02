"use strict";
(()=>{const base=document.body.dataset.basePath||"";if(location.pathname!==base+"/skills")return;
for(const n of document.querySelectorAll("main > section"))n.hidden=n.id!=="skills-view";
const status=document.querySelector("#skills-status"),list=document.querySelector("#skills-list");
function text(tag,value){const n=document.createElement(tag);n.textContent=value;return n;}
let busy=false,last="";
async function load(){if(busy)return;busy=true;try{const r=await window.NexusLive.fetch(base+"/api/v1/skills",{credentials:"same-origin",cache:"no-store"});if(!r.ok)throw Error();const p=await r.json();if(p.version!==1||!Array.isArray(p.items))throw Error();const signature=JSON.stringify(p);if(signature===last){status.textContent=p.items.length+" stored versions · Scope: "+p.scope+" · Live";return;}last=signature;list.replaceChildren();
status.textContent=p.items.length+" stored versions · Scope: "+p.scope+" · Skills "+(p.enabled?"enabled":"disabled")+(p.items.length?"":". No skills have been stored in this scope yet.");
for(const item of p.items){const m=item.metadata,card=text("article","");card.className="stats-card";card.append(text("p",item.status.toUpperCase()),text("h2",m.key.name),text("p",m.description),text("p","Tags: "+(m.tags||[]).join(", ")),text("p","Version: "+m.version));list.append(card);}
}catch(e){status.textContent="Skills could not be loaded. Reconnecting automatically.";return false;}finally{busy=false;}}
document.querySelector("#skills-refresh").addEventListener("click",load);window.NexusLive.watch("skills",load);load();})();
