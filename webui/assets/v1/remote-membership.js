"use strict";
window.NexusRemoteMembership = (() => {
 function mount(base, csrf) {
  const form=document.querySelector("#remote-pair-form"), verified=document.querySelector("#remote-identity-verified"), pair=document.querySelector("#remote-pair"), refresh=document.querySelector("#remote-refresh"), status=document.querySelector("#remote-status"), peers=document.querySelector("#remote-peers");
  const editor=window.NexusRemotePairForm.mount(form,verified);
  let page=null, busy=false, confirmations=[];
  const lockedButtons=new Map();
  function message(text) { status.textContent=text; }
  function lock(value) { confirmations.forEach(node=>node.hidden=true); busy=value; pair.disabled=value||!page||!page.enabled; refresh.disabled=value; editor.lock(value); verified.disabled=value; if(value){peers.querySelectorAll("button").forEach(b=>{lockedButtons.set(b,b.disabled);b.disabled=true;});}else{lockedButtons.forEach((disabled,b)=>b.disabled=disabled);lockedButtons.clear();} }
  function render(value) {
   if (!value || value.version!==1 || typeof value.enabled!=="boolean" || (value.enabled && (!/^[a-f0-9]{64}$/.test(value.digest) || !value.registry || value.registry.version!==1 || !Array.isArray(value.registry.peers) || value.registry.peers.length>128))) throw Error("invalid response");
   page=value; confirmations=[]; peers.replaceChildren(); form.hidden=!value.enabled;
   if (!value.enabled) { message("Membership management is disabled. An administrator can enable it with a private remote trust registry in the daemon configuration."); return; }
   value.registry.peers.forEach(peer=>{
    const card=document.createElement("details"), title=document.createElement("summary"), content=document.createElement("pre"), revoke=document.createElement("button");
    title.textContent=peer.id+" · "+(peer.transport||"https")+" · "+peer.endpoint;
    content.textContent=JSON.stringify(peer,null,2); revoke.type="button"; revoke.textContent="Revoke "+peer.id;
    const confirmation=document.createElement("div"), warning=document.createElement("p"), confirm=document.createElement("button"), dismiss=document.createElement("button");
    confirmation.hidden=true;confirmation.setAttribute("role","group");confirmation.setAttribute("aria-label","Confirm peer revocation");
    warning.textContent="Revoke "+peer.id+"? New requests will be denied. Already admitted work is not canceled.";
    confirm.type=dismiss.type="button";confirm.textContent="Confirm revocation";dismiss.textContent="Keep peer";confirmation.append(warning,confirm,dismiss);confirmations.push(confirmation);
    revoke.addEventListener("click",()=>{if(!busy&&page===value){confirmations.forEach(node=>node.hidden=true);confirmation.hidden=false;confirm.focus();}});
    dismiss.addEventListener("click",()=>{confirmation.hidden=true;revoke.focus();});
    confirm.addEventListener("click",()=>{if(!confirmation.hidden&&!busy&&page===value)mutate({action:"revoke",instance:peer.id});});
    card.append(title,content,revoke,confirmation); if(value.inspection_enabled) window.NexusRemoteInspection.attach(card,peer,base,csrf,value.task_controls_enabled); if(value.dispatch_enabled && (peer.operations||[]).includes("dispatch")) window.NexusRemoteDispatch.attach(card,peer,base,csrf); peers.append(card);
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
    render(await response.json()); if(fields.action==="pair"){editor.clear();verified.checked=false;}
   } catch(error) {
    page=null; verified.checked=false; form.hidden=true; peers.replaceChildren();
    message(error.message==="conflict"?"Membership changed elsewhere. Refresh and review it before making another change.":"The membership change could not be confirmed. Refresh to inspect current membership before retrying.");
   } finally { lock(false); }
  }
  form.addEventListener("submit",event=>{event.preventDefault();if(!verified.checked||busy)return;try{const peer=editor.read();mutate({action:"pair",peer,identity_verified:true});}catch{message("Review the pairing fields and permission preview.");}});
  refresh.addEventListener("click",load);
  load();
 }
 return {mount};
})();
