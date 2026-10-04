"use strict";
window.NexusChatDescriptions=(()=>{
 const base=document.body.dataset.basePath||"";
	const chatDescriptions = new Map(), descriptionQueue = [];
 let descriptionReaders = 0;
 function chatDescription(chatID) {
  if (chatDescriptions.has(chatID)) return chatDescriptions.get(chatID);
  const promise = new Promise(resolve => {descriptionQueue.push({chatID,resolve});});
  if (chatDescriptions.size >= 500) chatDescriptions.delete(chatDescriptions.keys().next().value);
  chatDescriptions.set(chatID,promise);readChatDescriptions();return promise;
 }
 function readChatDescriptions() {
  while(descriptionReaders<4 && descriptionQueue.length){
   const {chatID,resolve}=descriptionQueue.shift();descriptionReaders++;
   window.NexusLive.fetch(base+"/api/v1/chats/"+encodeURIComponent(chatID)+"/messages?limit=1",{credentials:"same-origin",cache:"no-store"}).then(response=>{if(!response.ok)throw Error();return response.json();}).then(page=>{
    if(!page||page.version!==1||page.chat_id!==chatID||!Array.isArray(page.messages)||page.messages.length>1)throw Error();
    const message=page.messages.find(m=>m.role==="user"&&typeof m.text==="string");
    const text=message?message.text.replace(/\s+/g," ").trim():"";
    resolve(text?(text.length>100?text.slice(0,97)+"…":text):"Description unavailable");
   }).catch(()=>{chatDescriptions.delete(chatID);resolve("Description unavailable");}).finally(()=>{descriptionReaders--;readChatDescriptions();});
  }
 }
 function decorate(button,item) {
  let name=button.querySelector('.chat-name'),meta=button.querySelector('.chat-meta');
  if(!name){name=document.createElement('span');name.className='chat-name';name.textContent='Loading description…';meta=document.createElement('span');meta.className='chat-meta';meta.append(document.createElement('span'),document.createElement('time'));button.append(name,meta);chatDescription(item.chat_id).then(description=>{if(button.isConnected){name.textContent=description;button.title=description+' — Chat ID: '+item.chat_id;}});}
  const state=meta.firstElementChild,time=meta.lastElementChild;

  state.textContent=typeof item.state==="string"&&item.state?item.state.replaceAll("_"," "):"unknown";
  const date=new Date(item.started_at);time.textContent=Number.isNaN(date.getTime())?"Unknown time":date.toLocaleString();

 }
 return {get:chatDescription,decorate};
})();
