"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(location.pathname!==base+"/models")return;
 let policy=null,generation=0,saving=false;const controls=new Set();
 async function request(body){
  const session=await window.NexusLive.fetch(base+'/api/v1/session/csrf',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({version:1})});
  if(!session.ok)throw Error();const csrf=await session.json();
  const response=await window.NexusLive.fetch(base+'/api/v1/model-use',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Darwin-CSRF':csrf.csrf_token},body:JSON.stringify({version:1,...body})});
  if(!response.ok)throw Error();const result=await response.json();
  if(result.version!==1||!result.disabled||typeof result.disabled!=='object'||Array.isArray(result.disabled))throw Error();
  return result;
 }
 function update(){for(const c of controls){if(!c.root.isConnected&&c.connected){controls.delete(c);continue;}c.connected=c.root.isConnected;if(c.busy)continue;c.input.checked=c.configured&&!!policy&&!policy.disabled[c.key];c.input.disabled=saving||!policy||!c.configured;c.label.textContent=c.input.checked?'Enabled for use':'Disabled for use';if(!c.configured)c.message.textContent='Configure this model before enabling it.';}}
 window.NexusModelUse={attach(root,host,model,configured=true){
  const row=document.createElement('div');row.className='model-use-control';
  const label=document.createElement('label'),input=document.createElement('input'),text=document.createElement('span'),message=document.createElement('span');input.type='checkbox';input.setAttribute('role','switch');input.setAttribute('aria-label','Enable '+model+' on '+host);message.setAttribute('role','status');
  label.append(input,text);row.append(label);message.className='model-use-feedback';root.classList.add('model-card-with-switch');root.prepend(row);root.append(message);
  const c={root:row,input,label:text,message,key:host+'/'+model,configured,busy:false};controls.add(c);update();
  input.addEventListener('change',async()=>{if(saving){update();return;}const enabled=input.checked;generation++;saving=true;c.busy=true;input.disabled=true;update();message.textContent='Saving…';try{policy=await request({host,model,enabled});message.textContent=(host==='local'?'Applies to new jobs.':'Applies to requests from this host.')+' Running jobs continue.';}catch{message.textContent='Could not save. Refresh and try again.';}finally{saving=false;c.busy=false;update();}});
 }};
 async function refresh(){if(saving)return;const current=++generation;try{const next=await request({});if(current!==generation||saving)return;policy=next;}catch{if(current!==generation||saving)return;policy=null;}update();}
 window.NexusLive.watch('model-use',refresh,{interval:10000});refresh();
})();
