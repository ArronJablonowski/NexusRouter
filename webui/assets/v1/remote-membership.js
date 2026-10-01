"use strict";
window.NexusRemoteMembership = (() => {
 function mount(base, csrf) {
  const form=document.querySelector("#remote-pair-form"), input=document.querySelector("#remote-peer-json"), verified=document.querySelector("#remote-identity-verified"), pair=document.querySelector("#remote-pair"), refresh=document.querySelector("#remote-refresh"), status=document.querySelector("#remote-status"), peers=document.querySelector("#remote-peers");
  let page=null, busy=false;
  function message(text) { status.textContent=text; }
  function lock(value) { busy=value; pair.disabled=value||!page||!page.enabled; refresh.disabled=value; input.disabled=value; verified.disabled=value; peers.querySelectorAll("button").forEach(b=>b.disabled=value); }
  function render(value) {
   if (!value || value.version!==1 || typeof value.enabled!=="boolean" || (value.enabled && (!/^[a-f0-9]{64}$/.test(value.digest) || !value.registry || value.registry.version!==1 || !Array.isArray(value.registry.peers) || value.registry.peers.length>128))) throw Error("invalid response");
   page=value; peers.replaceChildren(); form.hidden=!value.enabled;
   if (!value.enabled) { message("Membership management is disabled. An administrator can enable it with a private remote trust registry in the daemon configuration."); return; }
   value.registry.peers.forEach(peer=>{
    const card=document.createElement("details"), title=document.createElement("summary"), content=document.createElement("pre"), revoke=document.createElement("button");
    title.textContent=peer.id+" · "+(peer.transport||"https")+" · "+peer.endpoint;
    content.textContent=JSON.stringify(peer,null,2); revoke.type="button"; revoke.textContent="Revoke "+peer.id;
    revoke.addEventListener("click",()=>{ if (!busy && window.confirm("Revoke "+peer.id+"? New requests will be denied. Already admitted work is not canceled.")) mutate({action:"revoke",instance:peer.id}); });
    card.append(title,content,revoke); if(value.inspection_enabled) window.NexusRemoteInspection.attach(card,peer,base,csrf,value.task_controls_enabled); peers.append(card);
   });
   message(value.registry.peers.length+" configured peers. Availability has not been checked.");
  }
  async function load() {
   if (busy) return;
   lock(true);
   try { const response=await fetch(base+"/api/v1/remote-membership",{credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}}); if(!response.ok)throw Error(); render(await response.json()); }
   catch { page=null; form.hidden=true; peers.replaceChildren(); message("Membership could not be loaded. Check the browser session and configured private registry."); }
   finally { lock(false); }
  }
  async function mutate(fields) {
   if (busy || !page || !page.enabled) return;
   lock(true);
   try {
    const response=await fetch(base+"/api/v1/remote-membership",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json",Accept:"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({version:1,expected_digest:page.digest,...fields})});
    if (!response.ok) throw Error(response.status===409?"conflict":"unknown");
    render(await response.json()); if(fields.action==="pair"){input.value="";verified.checked=false;}
   } catch(error) {
    page=null; form.hidden=true; peers.replaceChildren();
    message(error.message==="conflict"?"Membership changed elsewhere. Refresh and review it before making another change.":"The membership change could not be confirmed. Refresh to inspect current membership before retrying.");
   } finally { lock(false); }
  }
  form.addEventListener("submit",event=>{event.preventDefault();if(!verified.checked||busy)return;try{const peer=JSON.parse(input.value);if(!peer||typeof peer!=="object"||Array.isArray(peer))throw Error();mutate({action:"pair",peer,identity_verified:true});}catch{message("Enter a valid peer configuration object.");}});
  refresh.addEventListener("click",load);
  load();
 }
 return {mount};
})();
