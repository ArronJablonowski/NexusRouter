"use strict";
window.NexusChatFiles=(()=>{
 let chat='',generation=0,stop=null,busy=false;
 const base=document.body.dataset.basePath||'/app',panel=document.querySelector('#chat-files'),list=document.querySelector('#chat-file-list'),status=document.querySelector('#chat-file-status');
 const id=v=>typeof v==='string'&&/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
 function valid(f){return f&&id(f.id)&&id(f.task_id)&&typeof f.name==='string'&&f.name.length>0&&f.name.length<=200&&f.name!=='.'&&f.name!=='..'&&!/[\x00-\x1f\x7f/\\]/.test(f.name)&&Number.isSafeInteger(f.size)&&f.size>=0&&f.size<=32*1024*1024&&['image/png','image/jpeg','image/webp','text/plain','application/octet-stream'].includes(f.media_type);}
 function render(files){
  const existing=new Map([...list.children].map(n=>[n.dataset.fileId,n])),keep=new Set();let cursor=list.firstElementChild;
  for(const f of files){let row=existing.get(f.id);const url=base+'/api/v1/chats/'+encodeURIComponent(chat)+'/files/'+encodeURIComponent(f.id);
   if(!row){row=document.createElement('li');row.className='chat-file-card';row.dataset.fileId=f.id;
    if(f.media_type.startsWith('image/')){const link=document.createElement('a');link.href=url+'/preview';link.target='_blank';link.rel='noopener';link.className='chat-file-preview';link.setAttribute('aria-label','Open '+f.name);const image=document.createElement('img');image.alt=f.name;image.loading='lazy';image.src=url+'/preview';image.addEventListener('error',()=>{image.hidden=true;row.querySelector('.chat-file-detail').textContent='Preview unavailable · Download may still be available';});link.append(image);row.append(link);}
    const name=document.createElement('p');name.className='chat-file-name';name.textContent=f.name;
    const detail=document.createElement('p');detail.className='chat-file-detail';detail.textContent=f.size<1024?f.size+' bytes':Math.ceil(f.size/1024)+' KB';
    const download=document.createElement('a');download.className='chat-file-download';download.href=url+'/download';download.download=f.name;download.textContent='Download';download.setAttribute('aria-label','Download '+f.name);row.append(name,detail,download);
   }keep.add(row);if(row!==cursor)list.insertBefore(row,cursor);cursor=row.nextElementSibling;
  }
  for(const row of [...list.children])if(!keep.has(row))row.remove();
 }
 async function load(){
  if(!chat||busy)return false;busy=true;const version=generation,selected=chat;
  try{const r=await window.NexusLive.fetch(base+'/api/v1/chats/'+encodeURIComponent(selected)+'/files',{credentials:'same-origin',cache:'no-store'},40000);if(!r.ok)throw Error();const p=await r.json();
   if(p.version!==1||p.chat_id!==selected||!Array.isArray(p.files)||p.files.length>100||typeof p.pending!=='boolean'||!p.files.every(valid)||new Set(p.files.map(f=>f.id)).size!==p.files.length)throw Error();
   if(version!==generation)return true;render(p.files);panel.hidden=p.files.length===0&&!p.pending;status.textContent=p.pending?'Waiting for generated files…':'';status.hidden=!p.pending;return true;
  }catch{if(version===generation&&list.children.length){panel.hidden=false;status.hidden=false;status.textContent='Files could not be refreshed. Saved downloads remain listed.';}return false;}
  finally{busy=false;}
 }
 function select(value){generation++;chat=id(value)?value:'';if(stop)stop();list.replaceChildren();panel.hidden=true;status.hidden=true;
  if(chat)stop=window.NexusLive.watch('chat-files',load,{interval:5000,immediate:true,ready:()=>!busy});
 }
 return{select,refresh:load};
})();
