"use strict";
window.NexusRemotePairForm = (()=>{
 function build(v){
  const name=/^[A-Za-z0-9._:/-]{1,128}$/, list=(text,max)=>{const a=text.split(/[\r\n,]+/).map(x=>x.trim()).filter(Boolean);if(a.length>max||new Set(a).size!==a.length||a.some(x=>!name.test(x)||x==="auto"))throw Error("Use unique explicit model and harness IDs.");return a;};
  const integer=(value,min,max,label)=>{const n=Number(value);if(String(value).trim()===""||!Number.isSafeInteger(n)||n<min||n>max)throw Error("Enter a valid "+label+".");return n;};
  if(!/^[A-Za-z0-9_-]{1,64}$/.test(v.id)||!name.test(v.server_name))throw Error("Enter the instance ID and certificate server name.");
  if(!/^https:\/\/(\[[0-9a-fA-F:.]+\]|[0-9.]+):[0-9]+$/.test(v.endpoint))throw Error("Use a concrete HTTPS IP address and task port.");
  integer(v.endpoint.slice(v.endpoint.lastIndexOf(":")+1),1,65535,"task port");
  const pins=v.pins.split(/[\r\n,]+/).map(x=>x.trim().toLowerCase().replace(/:/g,"")).filter(Boolean);
  if(pins.length<1||pins.length>2||new Set(pins).size!==pins.length||pins.some(x=>!/^[a-f0-9]{64}$/.test(x)))throw Error("Provide one or two unique verified SHA-256 fingerprints.");
  const operations=["info","inspect","dispatch","cancel","logs","runner"].filter(op=>v["op_"+op]);if(!operations.length)throw Error("Select at least one allowed operation.");
  const cost=Number(v.max_cost);if(v.max_cost.trim()===""||!Number.isFinite(cost)||cost<0)throw Error("Enter a nonnegative estimated cost ceiling.");
  const peer={id:v.id,endpoint:v.endpoint,server_name:v.server_name,pins,transport:v.transport,operations,models:list(v.models,128),harnesses:list(v.harnesses,256),allow_private:v.allow_private,allow_public_network:v.allow_public_network,allow_cloud_inference:v.allow_cloud_inference,max_cost:cost,max_context_tokens:integer(v.max_context_tokens,1,Number.MAX_SAFE_INTEGER,"context ceiling")};
  if(v.transport==="ssh"){
   if(!v.ssh_user||!v.ssh_key||!v.ssh_hosts)throw Error("Provide the SSH user, identity path and verified known-hosts path.");
   peer.ssh={user:v.ssh_user,port:integer(v.ssh_port,1,65535,"SSH port"),identity_file:v.ssh_key,known_hosts_file:v.ssh_hosts};
  }else if(v.transport!=="https")throw Error("Choose HTTPS or SSH.");
  if(v.custom_limits){peer.request_limits={};for(const op of ["info","dispatch","inspect","cancel"])peer.request_limits[op]=integer(v["limit_"+op],1,10000,op+" rate limit");}
  return peer;
 }
 function mount(form,verified){
  const field=id=>document.querySelector("#remote-"+id),preview=field("peer-preview"),ssh=field("ssh-fields"),limits=field("limit-fields");
  const text=id=>field(id).value.trim(),checked=id=>field(id).checked;
  function read(){return build({id:text("peer-id"),endpoint:text("peer-endpoint"),server_name:text("peer-server-name"),pins:text("peer-pins"),transport:text("peer-transport"),models:text("peer-models"),harnesses:text("peer-harnesses"),max_cost:text("peer-cost"),max_context_tokens:text("peer-context"),allow_private:checked("peer-private"),allow_public_network:checked("peer-public"),allow_cloud_inference:checked("peer-cloud"),op_info:checked("op-info"),op_inspect:checked("op-inspect"),op_dispatch:checked("op-dispatch"),op_cancel:checked("op-cancel"),op_logs:checked("op-logs"),op_runner:checked("op-runner"),ssh_user:text("peer-ssh-user"),ssh_port:text("peer-ssh-port"),ssh_key:text("peer-ssh-key"),ssh_hosts:text("peer-ssh-hosts"),custom_limits:checked("peer-custom-limits"),limit_info:text("limit-info"),limit_dispatch:text("limit-dispatch"),limit_inspect:text("limit-inspect"),limit_cancel:text("limit-cancel")});}
  function update(event){if(!event||event.target!==verified)verified.checked=false;ssh.hidden=text("peer-transport")!=="ssh";limits.hidden=!checked("peer-custom-limits");try{preview.textContent=JSON.stringify(read(),null,2);}catch(error){preview.textContent=error.message;}}
  form.addEventListener("input",update);form.addEventListener("change",update);update();
  return {read,prefill(c){form.reset();field("peer-id").value=c.instance;field("peer-endpoint").value=c.endpoint;field("peer-server-name").value=c.server_name;field("peer-pins").value=c.claimed_certificate_sha256;if(c.ssh_port)field("peer-ssh-port").value=String(c.ssh_port);verified.checked=false;update();},lock(value){form.querySelectorAll("input,select,textarea").forEach(n=>n.disabled=value);},clear(){form.reset();update();}};
 }
 return {mount,build};
})();
