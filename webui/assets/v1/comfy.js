(function () {
 "use strict";
 window.NexusComfyApproval = Object.freeze({preview(value,toolName,maximum) {
  if (!value || value.version!==1 || value.kind!==(toolName==="comfy_submit"?"comfy_workflow":"comfy_cancel") || Object.keys(value).length!==3 || !value.arguments || typeof value.arguments!=="object" || Array.isArray(value.arguments)) return "";
  const text=JSON.stringify(value.arguments,null,2);return text.length<=maximum?text:"";
 }});
 const base = document.body.dataset.basePath || "/app";
 if (window.location.pathname !== base + "/comfy") return;
 const view = document.querySelector("#comfy-view"); view.hidden = false;
 const notice = document.querySelector("#comfy-notice"), jobs = document.querySelector("#comfy-jobs"), key = document.querySelector("#comfy-key"), file = document.querySelector("#comfy-file"), run = document.querySelector("#comfy-run"), cancel = document.querySelector("#comfy-cancel"), state = document.querySelector("#comfy-state"), images = document.querySelector("#comfy-images");
 let csrf = "", busy = false, checking = false, refreshing = false, workflow = null, selected = "", uncertain = false, confirmed = "", preview = "";
 const unresolved = new Set();
 function select(id) {if(selected!==id){selected=id;confirmed="";preview="";images.replaceChildren();}controls();}
 key.addEventListener("input",()=>select(key.value.trim()));
 function message(text) { notice.textContent = text; }
 function controls() { run.disabled = busy || !csrf || !workflow || uncertain; cancel.disabled = busy || !csrf || !confirmed || confirmed !== selected || selected !== key.value.trim(); }
 async function request(path, options) {
  const response = await fetch(base + "/api/v1" + path, {credentials: "same-origin", cache: "no-store", ...options});
  if (!response.ok) throw new Error("Request unavailable");
  return response.json();
 }
 async function refresh() {
  if (refreshing || document.hidden) return;refreshing = true;
  try {
   const page = await request("/comfy"); if (page.version !== 1 || !Array.isArray(page.jobs)) throw new Error("Invalid jobs");
   jobs.replaceChildren();
   for (const job of page.jobs) {
    if (!job || typeof job.key !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(job.key) || typeof job.state !== "string") continue;
    if (["prepared","uncertain"].includes(job.state)) unresolved.add(job.key);else unresolved.delete(job.key);
    const li = document.createElement("li"), button = document.createElement("button");button.type = "button";button.textContent = job.key + " · " + job.state;
    button.addEventListener("click", () => {if (busy) return; key.value = job.key; select(job.key); check();});li.append(button);jobs.append(li);
   }
   uncertain=unresolved.size>0;controls();
   if (uncertain) message("Unconfirmed job references: "+[...unresolved].join(", ")+". Inspect these original jobs before submitting again.");
   if (!uncertain) message("Job records refreshed.");
  } catch (_) { message("ComfyUI controls are unavailable. Check the configured connection; saved jobs have not been resent."); }
  finally {refreshing=false;}
 }
 async function check() {
  if (checking || busy || document.hidden) return;
  const id = key.value.trim();if (!/^[A-Za-z0-9_-]{1,128}$/.test(id)) {message("Enter a valid job reference.");return;}
  checking = true;select(id);confirmed="";controls();
  try {
   const result = await request("/comfy/jobs/" + encodeURIComponent(id));if (result.version !== 1 || !result.job || result.job.key !== id) throw new Error("Invalid job");
   const job = result.job;if (selected !== id) return;
   if (["prepared", "uncertain"].includes(job.state)) unresolved.add(id);else unresolved.delete(id);
   uncertain = unresolved.size>0;confirmed=job.state==="accepted"?id:"";state.textContent = "Job " + id + ": " + job.state;
   if (uncertain) message("Delivery is unconfirmed. Keep this reference and inspect the original job; do not send it again.");
   const signature=id+JSON.stringify([job.state,job.history?.outputs]);
   if (preview!==signature) {preview=signature;images.replaceChildren();
   if (job.state === "success" && job.history && job.history.outputs) {
    for (const [node, output] of Object.entries(job.history.outputs)) {
     if (!output || !Array.isArray(output.images)) continue;
     output.images.forEach((_, index) => {
      const url = base + "/api/v1/comfy/jobs/" + encodeURIComponent(id) + "/image?node=" + encodeURIComponent(node) + "&image=" + index;
      const image = document.createElement("img"), link = document.createElement("a");image.src = url;image.alt = "ComfyUI output " + (index + 1);image.loading = "lazy";link.href = url;link.download = "comfy-output";link.textContent = "Download image " + (index + 1);images.append(image,link);
     });
    }
   }
   }
   cancel.disabled = busy || !csrf || job.state !== "accepted";
  } catch (_) {if(selected!==id)return;state.textContent = "Job status is unavailable. This does not mean it failed or was never submitted.";confirmed="";cancel.disabled=true;}
  finally {checking = false;run.disabled = busy || !csrf || !workflow || uncertain;}
 }
 file.addEventListener("change", async () => {
  workflow = null;controls();const selectedFile = file.files[0];if (!selectedFile) return;
  if (selectedFile.size > 120 * 1024) {message("Workflow file is too large (maximum 120 KiB).");return;}
  try {
   const value = JSON.parse(await selectedFile.text());
   if (!value || Array.isArray(value) || typeof value !== "object" || !Object.keys(value).length || Object.keys(value).length > 256 || Object.values(value).some(node => !node || typeof node.class_type !== "string" || !node.inputs || typeof node.inputs !== "object" || Array.isArray(node.inputs))) throw new Error("Invalid graph");
   if (file.files[0] !== selectedFile) return;workflow = value;message("Workflow ready. Run only workflows and custom nodes you trust.");
  } catch (_) {message("Choose a workflow exported in ComfyUI API format.");}
  controls();
 });
 document.querySelector("#comfy-form").addEventListener("submit", async event => {
  event.preventDefault();if (busy || !csrf || !workflow || uncertain) return;
  busy = true;const id = "web-" + crypto.randomUUID();key.value = id;select(id);controls();
  try {
   const result = await request("/comfy/jobs", {method: "POST", headers: {"Content-Type":"application/json","X-Darwin-CSRF":csrf},body:JSON.stringify({key:id,workflow})});
   if (result.version !== 1 || !result.job || result.job.key !== id) throw new Error("Invalid submission");
   if (["prepared","uncertain"].includes(result.job.state)) unresolved.add(id);
   uncertain=unresolved.size>0;state.textContent = "Job " + id + ": " + result.job.state;
   message(uncertain ? "Delivery is unconfirmed. Check this original job; do not resend." : "Job recorded. Check progress below.");
  } catch (_) {unresolved.add(id);uncertain = true;message("Submission response was lost or rejected. Keep job reference " + id + " and check it before taking further action.");}
  finally {busy = false;controls();await refresh();await check();}
 });
 cancel.addEventListener("click", async () => {
  if (busy || !csrf || !confirmed || confirmed!==selected || selected!==key.value.trim()) return;const id = selected;busy = true;controls();
  try {await request("/comfy/jobs/" + encodeURIComponent(id) + "/cancel",{method:"POST",headers:{"X-Darwin-CSRF":csrf}});message("Removal requested for this waiting job. Running work is not interrupted.");}
  catch (_) {message("Removal is unconfirmed. Check the original job; no retry was sent.");}
  finally {busy = false;await check();}
 });
 document.querySelector("#comfy-refresh").addEventListener("click",refresh);
 document.querySelector("#comfy-check").addEventListener("click",check);
 request("/session/csrf",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})}).then(session=>{if (session.version !== 1 || typeof session.csrf_token !== "string" || !session.csrf_token) throw new Error("Invalid session");csrf=session.csrf_token;controls();refresh();}).catch(()=>message("Sign in again to use ComfyUI controls."));
 setInterval(()=>{if (!document.hidden && selected && !busy && !checking) check();},5000);
})();
