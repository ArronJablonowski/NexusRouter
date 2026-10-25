"use strict";
window.NexusRemoteChats=(()=>{
 const base=document.body.dataset.basePath||'',list=document.querySelector('#chat-list'),count=document.querySelector('#chat-count'),empty=document.querySelector('#chat-list-state');
 if(!window.NexusRoutes.chats(location.pathname.slice(base.length)))return {refresh(){}};
 const id=/^[A-Za-z0-9_-]{1,64}$/,key=/^[A-Za-z0-9_-]{16,64}$/;
 const more=document.createElement('button'),notice=document.createElement('p');more.type='button';more.textContent='Load more Spark chats';more.hidden=true;notice.setAttribute('role','status');notice.hidden=true;list.parentElement.append(more,notice);
 let cursor='',busy=false,pages=1;
 function totals(){const value=String(list.children.length);if(count.textContent!==value)count.textContent=value;if(list.children.length&&['No chats yet.','Loading chats…'].includes(empty.textContent))empty.hidden=true;}
 function row(item){
  let li=[...list.children].find(n=>n.dataset.remotePeer===item.instance&&n.dataset.remoteRoot===item.root);
  if(!li){li=document.createElement('li');li.className='chat-row remote-chat-row';li.dataset.remotePeer=item.instance;li.dataset.remoteRoot=item.root;const button=document.createElement('button'),name=document.createElement('span'),meta=document.createElement('span');button.type='button';name.className='chat-name';meta.className='chat-meta';button.append(name,meta);li.append(button);list.append(li);button.addEventListener('click',()=>{if(window.NexusHostMentions?.canLeave?.()===false)return;const u=new URL(base+'/chats',location.origin);u.hash=new URLSearchParams({chat_host:li.dataset.remotePeer,chat_request:button.dataset.remoteChat}).toString();if(location.pathname===u.pathname){history.replaceState(null,'',u);location.reload();}else location.assign(u);});}
  const button=li.firstElementChild;button.dataset.remoteChat=item.request_id;button.firstElementChild.textContent=item.title||'Spark conversation';button.lastElementChild.textContent=item.instance+' · '+item.model;button.title=(item.title||'Spark conversation')+' · '+item.instance+' · '+item.model;
  const current=new URLSearchParams(location.hash.slice(1));button.setAttribute('aria-current',String(item.instance===current.get('chat_host')&&(item.request_id===current.get('chat_request')||item.root===current.get('chat_request'))));li.dataset.orderTime=String(Date.parse(item.created_at)||0);li.dataset.pinned='false';
 }
 async function load(after='',reset=false){if(busy)return;busy=true;more.disabled=true;try{
  const response=await window.NexusLive.fetch(base+'/api/v1/remote-chats'+(after?'?after='+encodeURIComponent(after):''),{credentials:'same-origin',cache:'no-store'});if(!response.ok)throw Error();const p=await response.json();
  if(p.version!==1||!Array.isArray(p.items)||p.items.length>25||typeof p.next_cursor!=='string'||(p.next_cursor&&!/^[1-9][0-9]{0,15}$/.test(p.next_cursor))||p.items.some(v=>!id.test(v.instance)||!key.test(v.request_id)||!key.test(v.root)||typeof v.title!=='string'||v.title.length>512||typeof v.model!=='string'||v.model.length>512))throw Error();
  // Refresh upserts roots; it never removes older loaded pages or saved chats.
  p.items.forEach(row);if(after)pages++;if(reset&&pages>1){/* Preserve pagination until the operator requests another page. */}else cursor=p.next_cursor;
  more.hidden=!cursor;notice.hidden=!p.indexing;if(p.indexing)notice.textContent='Loading saved Spark chats…';window.NexusChatDescriptions.order(list);totals();
 }catch{notice.textContent='Spark chats could not be refreshed. Saved entries remain available.';notice.hidden=false;}finally{busy=false;more.disabled=false;}}
 more.addEventListener('click',()=>{if(cursor)load(cursor)});
 const observer=new MutationObserver(totals);observer.observe(list,{childList:true});observer.observe(count,{childList:true});observer.observe(empty,{childList:true});
 window.NexusLive.watch('remote-chat-sidebar',()=>load('',true),{interval:5000,immediate:true,ready:()=>!busy&&!window.NexusChatDescriptions.busy()});
 return {refresh:()=>load('',true)};
})();
