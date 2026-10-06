"use strict";
window.NexusHostMentions = (() => {
 const input=document.querySelector('#composer-text'),base=document.body.dataset.basePath||'',panel=document.querySelector('#host-routing'),popup=document.querySelector('#host-options'),notice=document.querySelector('#host-notice'),models=document.querySelector('#host-model'),receipt=document.querySelector('#host-receipt');
 let hosts=[{id:'local',name:'local',local:true}],matches=[],active=0,loading=false,loadedAt=0,selected='',busy=false,sent=false,csrf='';
 const id=/^[A-Za-z0-9_-]{1,64}$/, hostname=/^[A-Za-z0-9._-]{1,253}$/;
 const token=text=>/^@([A-Za-z0-9._-]*)(\s|$)/.exec(text);
 function message(text){notice.textContent=text;panel.hidden=false;}
 async function json(path,body){const response=await fetch(base+'/api/v1/'+path,{method:body?'POST':'GET',credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(12000),headers:{Accept:'application/json',...(body?{'Content-Type':'application/json','X-Darwin-CSRF':csrf}:{})},...(body?{body:JSON.stringify(body)}:{})});if(!response.ok)throw Error();return response.json();}
 async function session(){if(!csrf){const p=await json('session/csrf',{version:1});if(p.version!==1||typeof p.csrf_token!=='string'||!p.csrf_token)throw Error();csrf=p.csrf_token;}return csrf;}
 function hide(){popup.hidden=true;input.setAttribute('aria-expanded','false');input.removeAttribute('aria-activedescendant');}
 function draw(){
  const t=token(input.value),caret=input.selectionStart;
  if(!t||caret>t[1].length+1||caret<1){hide();return;}
  const query=t[1].toLowerCase();matches=hosts.filter(h=>h.name.toLowerCase().includes(query)||h.id.toLowerCase().includes(query));active=Math.min(active,Math.max(0,matches.length-1));popup.replaceChildren();
  matches.forEach((h,i)=>{const option=document.createElement('button');option.type='button';option.id='host-option-'+i;option.setAttribute('role','option');option.setAttribute('aria-selected',String(i===active));option.textContent='@'+h.name+(h.local?' · This host':' · '+h.id+(h.info?.available?'':' · unavailable'));option.addEventListener('mousedown',e=>e.preventDefault());option.addEventListener('click',()=>choose(h));popup.append(option);});
  popup.hidden=!matches.length;input.setAttribute('aria-expanded',String(matches.length>0));if(matches.length)input.setAttribute('aria-activedescendant','host-option-'+active);
 }
 function resolve(name){const exact=hosts.filter(h=>h.name.toLowerCase()===name.toLowerCase()||h.id.toLowerCase()===name.toLowerCase());return exact.length===1?exact[0]:null;}
 function sync(){
  const t=token(input.value),host=t&&resolve(t[1]);panel.hidden=!t&&!sent;models.hidden=!host||host.local;
  if(!host){selected='';models.replaceChildren();if(t)message('Choose a host from the list. Unknown or ambiguous mentions will not be sent.');return;}
  if(selected===host.id)return;selected=host.id;models.replaceChildren();
  if(host.local){message('Destination: this host.');return;}
  const placeholder=document.createElement('option');placeholder.value='';placeholder.textContent='Choose a local model on '+host.name;models.append(placeholder);
  for(const m of host.info?.models||[]){if(!m.local||!host.peer.models.includes(m.id)||m.estimated_cost!==0)continue;const option=document.createElement('option');option.value=m.id;option.textContent=m.model||m.id;models.append(option);}
  message('Destination: '+host.name+' · this host only · private, zero-cost model · no fallback.');
 }
 function choose(host){const t=token(input.value);if(!t)return;input.value='@'+host.name+' '+input.value.slice(t[0].length);input.setSelectionRange(host.name.length+2,host.name.length+2);hide();sync();input.dispatchEvent(new Event('input',{bubbles:true}));input.focus();}
 async function load(){
  if(loading||Date.now()-loadedAt<15000)return;loading=true;
  try{await session();const page=await json('remote-membership');if(page.version!==1||!page.enabled||!page.dispatch_enabled||!Array.isArray(page.registry?.peers))throw Error();
   const next=[hosts[0]];for(const peer of page.registry.peers.slice(0,128)){
    if(!id.test(peer.id)||peer.id==='local'||!Array.isArray(peer.models)||!['info','dispatch','inspect'].every(op=>peer.operations?.includes(op)))continue;
    const host={id:peer.id,name:peer.id,peer,info:null};next.push(host);
    try{const p=await json('remote-inspection',{version:1,instance:peer.id,view:'info'});if(p.version===1&&p.info?.instance===peer.id&&Array.isArray(p.info.models)){host.info=p.info;if(hostname.test(p.info.hostname)&&p.info.hostname!=='local')host.name=p.info.hostname;}}catch{}
   }
   hosts=next;loadedAt=Date.now();selected='';sync();draw();
  }catch{message('Host lookup unavailable. Remote mentions cannot be sent until hosts are verified.');}finally{loading=false;}
 }
 input.addEventListener('input',()=>{sync();draw();if(input.value.startsWith('@'))load();});input.addEventListener('click',draw);
 input.addEventListener('keydown',event=>{
  if(event.isComposing||popup.hidden)return;
  if(['ArrowDown','ArrowUp'].includes(event.key)){event.preventDefault();event.stopImmediatePropagation();active=(active+(event.key==='ArrowDown'?1:-1)+matches.length)%matches.length;draw();}
  else if(['Tab','Enter'].includes(event.key)&&!event.shiftKey){event.preventDefault();event.stopImmediatePropagation();choose(matches[active]);}
  else if(event.key==='Escape'){event.preventDefault();event.stopImmediatePropagation();hide();}
 },true);
 input.addEventListener('blur',hide);
 function recover(peer,key){receipt.replaceChildren();receipt.hidden=false;receipt.open=true;const summary=document.createElement('summary');summary.textContent='Remote job on '+peer;receipt.append(summary);window.NexusRemoteTaskControls.attach(receipt,{id:peer},{request_id:key},base,csrf);}
 async function send(host,prompt,model){
  if(busy||sent)return;busy=true;models.disabled=true;
  try{
   await session();const key=crypto.randomUUID(),url=new URL(window.location.href);url.hash=new URLSearchParams({chat_host:host.id,chat_request:key}).toString();window.history.replaceState(null,'',url);sent=true;
   message('Sending to '+host.name+' · '+model.model+'…');
   try{const p=await json('remote-dispatch',{version:1,instance:host.id,request_id:key,task:{version:1,model_id:model.id,prompt,domain:'general',profile:'default',context_tokens:Math.min(4096,host.peer.max_context_tokens,model.context_tokens),max_cost:0,private:true}});
    if(p.version!==1||p.instance!==host.id||p.request_id!==key||!p.submission_id)throw Error();message('Sent to '+host.name+' · '+model.model+'. Status and result below.');
   }catch{message('Delivery could not be confirmed. Inspect the saved request below; do not resend it. No fallback or retry was sent.');}
   recover(host.id,key);
  }catch{message('Could not preserve request recovery information. No task was sent.');}finally{busy=false;models.disabled=sent;}
 }
 function prepare(text,chat,sessionToken){
  const t=token(text);if(busy||sent){message('Inspect the previous remote job first. Choose “New remote task” below to send independent work.');return null;}
  if(!t){if(text.startsWith('@')){message('Invalid host mention. Choose a host from the popup; nothing was sent.');return null;}return text;}
  csrf=sessionToken||csrf;const host=resolve(t[1]);if(!host){message('Unknown or ambiguous host. Select a host from the popup; nothing was sent.');return null;}
  if(host.local)return text.slice(t[0].length);
  if(chat){message('Start a new chat to route an independent remote task. Existing local chat history is not transferred.');return null;}
  const prompt=text.slice(t[0].length).trim(),model=host.info?.models.find(m=>m.id===models.value);
  if(loading||!host.info?.available||!host.peer.allow_private||!model?.local||model.estimated_cost!==0||!host.peer.models.includes(model.id)||!Number.isSafeInteger(model.context_tokens)||model.context_tokens<1||!Number.isSafeInteger(host.peer.max_context_tokens)||host.peer.max_context_tokens<1||!prompt){message('Choose an available host, a permitted zero-cost local model, and enter a task. Nothing was sent.');return null;}
  if(new TextEncoder().encode(prompt).length>1048576){message('Task is too large. Nothing was sent.');return null;}
  send(host,prompt,model);return null;
 }
 document.querySelector('#host-new-task').addEventListener('click',()=>{if(busy)return;sent=false;models.disabled=false;const u=new URL(window.location.href);u.hash='';window.history.replaceState(null,'',u);message('New independent task. The previous job is not retried or canceled.');});
 const saved=new URLSearchParams(window.location.hash.slice(1)),peer=saved.get('chat_host'),key=saved.get('chat_request');if(id.test(peer||'')&&/^[A-Za-z0-9_-]{16,64}$/.test(key||'')){sent=true;session().then(()=>{recover(peer,key);message('Recovered remote job. No work was resent.');}).catch(()=>message('Sign in to inspect the saved remote job. No work was resent.'));}
 return {prepare};
})();
