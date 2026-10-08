"use strict";
window.NexusChatDescriptions=(()=>{
 const base=document.body.dataset.basePath||"",chatDescriptions=new Map(),preferences=new Map(),descriptionQueue=[];
 let descriptionReaders=0,openMenu=null,editing=false,saving=false;
 const node=(tag,text)=>{const el=document.createElement(tag);if(text)el.textContent=text;return el;};
 function accept(id,p){
  if(!p||typeof p.title!=="string"||typeof p.pinned!=="boolean"||!Number.isSafeInteger(p.revision)||p.revision<0)return;
  if((preferences.get(id)?.revision||0)>p.revision)return;
  preferences.set(id,p);
  if(preferences.size>1000)preferences.delete(preferences.keys().next().value);
 }
 function chatDescription(chatID){
  if(preferences.get(chatID)?.title)return Promise.resolve(preferences.get(chatID).title);
  if(chatDescriptions.has(chatID))return chatDescriptions.get(chatID).then(text=>preferences.get(chatID)?.title||text);
  const promise=new Promise(resolve=>descriptionQueue.push({chatID,resolve}));
  if(chatDescriptions.size>=500)chatDescriptions.delete(chatDescriptions.keys().next().value);
  chatDescriptions.set(chatID,promise);readChatDescriptions();return promise.then(text=>preferences.get(chatID)?.title||text);
 }
 function readChatDescriptions(){
  while(descriptionReaders<4&&descriptionQueue.length){
   const {chatID,resolve}=descriptionQueue.shift();descriptionReaders++;
   const preference=window.NexusLive.fetch(base+"/api/v1/chat-preferences/"+encodeURIComponent(chatID),{credentials:"same-origin",cache:"no-store"}).then(r=>{if(!r.ok)throw Error();return r.json();}).then(body=>{if(body.version===1&&body.chat_id===chatID)accept(chatID,body.preference);}).catch(()=>{});
   const description=window.NexusLive.fetch(base+"/api/v1/chats/"+encodeURIComponent(chatID)+"/messages?limit=1",{credentials:"same-origin",cache:"no-store"}).then(r=>{if(!r.ok)throw Error();return r.json();}).then(page=>{
    if(!page||page.version!==1||page.chat_id!==chatID||!Array.isArray(page.messages)||page.messages.length>1)throw Error();
    const message=page.messages.find(m=>m.role==="user"&&typeof m.text==="string"),text=message?message.text.replace(/\s+/g," ").trim():"";
    return text?(text.length>100?text.slice(0,97)+"…":text):"Description unavailable";
   }).catch(()=>{chatDescriptions.delete(chatID);return "Description unavailable";});
   Promise.all([preference,description]).then(([,text])=>resolve(text)).finally(()=>{descriptionReaders--;readChatDescriptions();});
  }
 }
 function closeMenu(focus=true){if(!openMenu)return;openMenu.menu.hidden=true;openMenu.trigger.setAttribute("aria-expanded","false");if(focus)openMenu.trigger.focus();openMenu=null;}
 function changed(id,p){accept(id,p);for(const button of document.querySelectorAll("#chat-list [data-chat-id]")){if(button.dataset.chatId===id){const row=button.parentElement,item={...row.chatItem,title:p.title,pinned:p.pinned,preference_revision:p.revision};decorate(button,item);actions(row,item);}}window.dispatchEvent(new CustomEvent("nexus-chat-preference",{detail:{id,preference:p}}));window.NexusLive.wake();}
 async function save(id,change,expectedRevision=preferences.get(id)?.revision||0){
  const session=await window.NexusLive.fetch(base+"/api/v1/session/csrf",{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});
  if(!session.ok)throw Error("Could not authorize the change.");const grant=await session.json();
  if(grant.version!==1||typeof grant.csrf_token!=="string"||!grant.csrf_token||grant.csrf_token.length>4096)throw Error("Could not authorize the change.");
  const response=await window.NexusLive.fetch(base+"/api/v1/chat-preferences",{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json","X-Darwin-CSRF":grant.csrf_token},body:JSON.stringify({version:1,chat_id:id,expected_revision:expectedRevision,...change})});
  if(!response.ok)throw Error(response.status===409?"This chat changed elsewhere. Close this dialog and refresh before trying again.":"Could not confirm the change. Refresh to check before trying again.");
  const body=await response.json();if(body.version!==1||body.chat_id!==id||!body.preference||!Number.isSafeInteger(body.preference.revision))throw Error("Refresh to check whether the change was saved.");
  changed(id,body.preference);
 }
 async function rename(id,opener){
  if(editing||saving)return;closeMenu(false);editing=true;const expectedRevision=preferences.get(id)?.revision||0;
  const dialog=node("dialog"),form=node("form"),heading=node("h2","Rename chat"),label=node("label","Chat name"),input=node("input"),feedback=node("p"),buttons=node("div"),cancel=node("button","Cancel"),submit=node("button","Save");
  dialog.className="chat-rename-dialog";heading.id="chat-rename-heading";dialog.setAttribute("aria-labelledby",heading.id);input.id="chat-rename-input";input.maxLength=100;input.required=true;label.htmlFor=input.id;feedback.setAttribute("role","status");cancel.type="button";submit.type="submit";buttons.className="chat-rename-buttons";buttons.append(cancel,submit);form.append(heading,label,input,feedback,buttons);dialog.append(form);document.body.append(dialog);
  input.value=preferences.get(id)?.title||"";dialog.showModal();input.focus();input.select();
  if(!input.value)chatDescription(id).then(text=>{if(dialog.open&&!input.value&&!input.dataset.edited){input.value=text;input.select();}});
  input.addEventListener("input",()=>{input.dataset.edited="true";});
  cancel.addEventListener("click",()=>dialog.close());
  dialog.addEventListener("cancel",e=>{if(saving)e.preventDefault();});
  dialog.addEventListener("close",()=>{editing=false;dialog.remove();if(opener.isConnected)opener.focus();window.NexusLive.wake();});
  form.addEventListener("submit",async e=>{e.preventDefault();if(saving)return;const title=input.value.trim();if(!title){feedback.textContent="Enter a chat name.";return;}saving=true;submit.disabled=cancel.disabled=input.disabled=true;feedback.textContent="Saving…";try{await save(id,{title},expectedRevision);dialog.close();}catch(err){feedback.textContent=err.message;}finally{saving=false;submit.disabled=cancel.disabled=input.disabled=false;}});
 }
 function actions(row,item){
  row.chatItem=item;
  let group=row.querySelector('.chat-actions');
  if(!group){
   group=node("div");group.className="chat-actions";const trigger=node("button","⋯"),menu=node("div"),renameButton=node("button","Rename"),pin=node("button");
   trigger.type=renameButton.type=pin.type="button";trigger.className="chat-more";trigger.setAttribute("aria-haspopup","menu");trigger.setAttribute("aria-expanded","false");menu.className="chat-action-menu";menu.setAttribute("role","menu");menu.hidden=true;
   for(const button of [renameButton,pin])button.setAttribute("role","menuitem");menu.append(renameButton,pin);group.append(trigger,menu);row.append(group);
   trigger.addEventListener("click",()=>{if(editing||saving)return;const was=openMenu?.trigger===trigger;closeMenu(false);if(!was){menu.hidden=false;trigger.setAttribute("aria-expanded","true");openMenu={trigger,menu};menu.scrollIntoView({block:"nearest"});renameButton.focus();}});
   group.addEventListener("keydown",e=>{if(e.key==="Escape"){e.preventDefault();closeMenu();}if(!menu.hidden&&["ArrowDown","ArrowUp","Home","End"].includes(e.key)){e.preventDefault();(e.key==="Home"?renameButton:e.key==="End"?pin:document.activeElement===renameButton?pin:renameButton).focus();}});
   renameButton.addEventListener("click",()=>rename(row.firstElementChild.dataset.chatId,trigger));
   pin.addEventListener("click",async()=>{if(saving)return;const id=row.firstElementChild.dataset.chatId;closeMenu();saving=true;trigger.disabled=true;const feedback=row.querySelector('.chat-action-feedback');feedback.textContent="Saving…";try{await save(id,{pinned:!preferences.get(id)?.pinned});feedback.textContent="";}catch(err){feedback.textContent=err.message;}finally{saving=false;trigger.disabled=false;}});
   const feedback=node("span");feedback.className="chat-action-feedback";feedback.setAttribute("role","status");row.append(feedback);
  }
  const p=preferences.get(item.chat_id)||{pinned:false};row.dataset.pinned=String(p.pinned);row.classList.add("chat-row");
  group.querySelector('.chat-more').setAttribute("aria-label","Chat options for "+(p.title||item.chat_id));
  group.querySelector('[role=menu]').lastElementChild.textContent=p.pinned?"Unpin":"Pin";
 }
 function decorate(button,item){
  accept(item.chat_id,{title:item.title||"",pinned:!!item.pinned,revision:item.preference_revision||0});
  let name=button.querySelector('.chat-name'),meta=button.querySelector('.chat-meta');
  if(!name){name=node('span');name.className='chat-name';name.textContent='Loading description…';meta=node('span');meta.className='chat-meta';meta.append(node('span'),node('time'));button.append(name,meta);chatDescription(item.chat_id).then(description=>{if(button.isConnected){name.textContent=preferences.get(item.chat_id)?.title||description;button.title=name.textContent+' — Chat ID: '+item.chat_id;}});}
  const p=preferences.get(item.chat_id);if(p?.title){name.textContent=p.title;button.title=p.title+' — Chat ID: '+item.chat_id;}
  meta.firstElementChild.textContent=(p?.pinned?'Pinned · ':'')+(typeof item.state==='string'?item.state.replaceAll('_',' '):'unknown');
  const date=new Date(item.started_at);meta.lastElementChild.textContent=Number.isNaN(date.getTime())?'Unknown time':date.toLocaleString();
 }
 document.addEventListener("click",e=>{if(openMenu&&!openMenu.trigger.parentElement.contains(e.target))closeMenu(false);});
 document.addEventListener("focusin",e=>{if(openMenu&&!openMenu.trigger.parentElement.contains(e.target))closeMenu(false);});
 function order(list,ids=[]){if(openMenu||editing)return;const scroll=list.scrollTop,rows=[...list.children],rank=new Map(ids.map((id,i)=>[id,i]));rows.sort((a,b)=>Number(b.dataset.pinned==='true')-Number(a.dataset.pinned==='true')||(rank.get(a.firstElementChild.dataset.chatId)??ids.length)-(rank.get(b.firstElementChild.dataset.chatId)??ids.length));const focus=document.activeElement;for(const row of rows)list.append(row);if(focus&&list.contains(focus))focus.focus({preventScroll:true});list.scrollTop=scroll;}
 return {get:chatDescription,decorate,actions,order,busy:()=>!!openMenu||editing||saving};
})();
