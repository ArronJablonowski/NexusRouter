"use strict";
window.NexusRemoteDiscovery=(()=>{
 function mount(base,csrf,choose){
  const panel=document.querySelector('#remote-discovery'),scan=document.querySelector('#remote-discover'),status=document.querySelector('#remote-discovery-status'),results=document.querySelector('#remote-discovery-results');
  let enabled=false,busy=false,generation=0;
  function valid(c){
   return c&&c.version===1&&c.verified===false&&/^[A-Za-z0-9_-]{1,64}$/.test(c.instance)&&typeof c.endpoint==='string'&&c.endpoint.length<=256&&/^https:\/\/[0-9.]+:[0-9]+$/.test(c.endpoint)&&typeof c.server_name==='string'&&c.server_name.length<=253&&/^[a-z0-9.-]+$/.test(c.server_name)&&/^[a-f0-9]{64}$/.test(c.claimed_certificate_sha256)&&(!c.ssh_port||(Number.isSafeInteger(c.ssh_port)&&c.ssh_port>0&&c.ssh_port<=65535))&&Number.isFinite(Date.parse(c.expires_at))&&Date.parse(c.expires_at)>Date.now();
  }
  scan.addEventListener('click',async()=>{
   if(!enabled||busy)return;
   busy=true;scan.disabled=true;results.replaceChildren();status.textContent='Looking for nearby instances…';const current=generation;
   const abort=new AbortController(),timer=setTimeout(()=>abort.abort(),5000);
   try{
    const response=await fetch(base+'/api/v1/remote-discovery',{method:'POST',credentials:'same-origin',cache:'no-store',signal:abort.signal,headers:{'Content-Type':'application/json',Accept:'application/json','X-Darwin-CSRF':csrf},body:JSON.stringify({version:1})});
    if(!response.ok)throw Error();const page=await response.json();
    if(!page||page.version!==1||!Array.isArray(page.candidates)||page.candidates.length>64||page.candidates.some(c=>!valid(c)))throw Error();
    if(current!==generation||!enabled)return;
    page.candidates.forEach(c=>{
     const card=document.createElement('div'),label=document.createElement('p'),details=document.createElement('p'),button=document.createElement('button');
     label.textContent=c.instance+' · '+c.endpoint+' · Unverified';
     details.textContent='Claimed certificate: '+c.claimed_certificate_sha256+(c.ssh_port?' · SSH port hint: '+c.ssh_port:'');
     button.type='button';button.textContent='Review pairing details';
     button.addEventListener('click',()=>{if(!enabled||busy||current!==generation)return;if(Date.parse(c.expires_at)<=Date.now()){button.disabled=true;status.textContent='This discovery result expired. Scan again.';return;}if(choose(c)!==false)status.textContent='Details copied. Independently verify the identity and review permissions before pairing.';});
     card.append(label,details,button);results.append(card);
    });
    status.textContent=page.candidates.length?'Found '+page.candidates.length+' unverified instances. Discovery does not grant access.':'No instances responded on the configured network interface.';
   }catch{if(current===generation){results.replaceChildren();status.textContent='Discovery could not be completed. Check the session and configured network interface.';}}
   finally{clearTimeout(timer);busy=false;scan.disabled=!enabled;}
  });
  return {setEnabled(value){if(enabled!==value){generation++;results.replaceChildren();}enabled=value;panel.hidden=!value;scan.disabled=busy||!value;}};
 }
 return {mount};
})();
