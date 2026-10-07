"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.NexusRoutes || !window.NexusRoutes.settings(relative)) return;
	const view = document.querySelector("#settings-view"), chat = document.querySelector("#chat-view"), workboards = document.querySelector("#workboard-view"), models = document.querySelector("#models-view");
	const form = document.querySelector("#tool-settings-form"), tools = document.querySelector("#tools-enabled"), delegated = document.querySelector("#delegate-read-tools"), specialistsAllowCloud = document.querySelector("#specialists-allow-cloud");
	const root = document.querySelector("#tools-read-root"), validation = document.querySelector("#settings-validation"), status = document.querySelector("#settings-status");
	const save = document.querySelector("#save-settings"), reset = document.querySelector("#reset-settings"), refresh = document.querySelector("#refresh-settings");
	const badge = document.querySelector("#settings-restart-badge"), activeSummary = document.querySelector("#active-settings");
	const connection = document.querySelector("#connection-state");
	const skillsEnabled=document.querySelector("#skills-enabled"), skillsDraft=document.querySelector("#skills-auto-draft"), skillsRoot=document.querySelector("#skills-root"), skillsScope=document.querySelector("#skills-scope");
 const advertiseEnabled=document.querySelector("#remote-advertise-enabled"), advertiseInterface=document.querySelector("#remote-advertise-interface"), advertiseName=document.querySelector("#remote-advertise-name"), advertiseSSH=document.querySelector("#remote-advertise-ssh-port");
 function validAdvertisement(a) {return a && typeof a.enabled==="boolean" && typeof a.interface==="string" && typeof a.name==="string" && Number.isInteger(a.ssh_port) && a.ssh_port>=0 && a.ssh_port<=65535 && (!a.interface || /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/.test(a.interface)) && a.name.length<=253 && (!a.name || a.name.split(".").every(label=>/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) && (!a.enabled || Boolean(a.interface&&a.name));}
 const macMemory=document.querySelector("#mac-memory"),macSwap=document.querySelector("#mac-swap");
 const dnsLogging=document.querySelector("#dns-logging"), dnsStatus=document.querySelector("#dns-logging-status");
 const digestPattern = /^[0-9a-f]{64}$/;
	let csrf = "", projection = null, loading = false;
	chat.hidden = true; workboards.hidden = true; models.hidden = true; view.hidden = false;
	function validAccess(value) {
		return value && (value.mac_memory_percent===undefined || Number.isFinite(value.mac_memory_percent)&&value.mac_memory_percent>=0&&value.mac_memory_percent<=100) && (value.mac_swap_growth_gb===undefined || Number.isFinite(value.mac_swap_growth_gb)&&value.mac_swap_growth_gb>=0&&value.mac_swap_growth_gb<=1024) && [undefined,"","managed","full"].includes(value.dns_logging) && validAdvertisement(value.remote_advertisement) && typeof value.skills_enabled === "boolean" && typeof value.skills_auto_draft === "boolean" && typeof value.skills_root === "string" && typeof value.skills_scope === "string" && (!value.skills_enabled || (value.skills_root.startsWith("/") && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value.skills_scope))) && typeof value.tools_enabled === "boolean" && typeof value.delegate_read_tools === "boolean" && typeof value.specialists_allow_cloud === "boolean" && typeof value.read_root === "string" && value.read_root.length <= 4096 && !/[\u0000-\u001f\u007f]/.test(value.read_root) && (!value.delegate_read_tools || value.tools_enabled) && (!value.tools_enabled || /^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(value.read_root));
	}
	function validProjection(value) {
		return value && value.version === 1 && digestPattern.test(value.digest) && validAccess(value.active) && validAccess(value.saved) && typeof value.restart_required === "boolean" && value.restart_required === (JSON.stringify(value.active) !== JSON.stringify(value.saved));
	}
	function setStatus(message, failed) { status.textContent = message; status.classList.toggle("error", Boolean(failed)); }
	function setBusy(value) { loading = value; [macMemory,macSwap,dnsLogging,skillsEnabled,skillsDraft,skillsRoot,skillsScope,advertiseEnabled,advertiseInterface,advertiseName,advertiseSSH].forEach(n=>n.disabled=value); save.disabled = value || !csrf || !projection; reset.disabled = value || !projection; refresh.disabled = value; tools.disabled = value; specialistsAllowCloud.disabled = value; root.disabled = value; syncDependency(); }
	function syncDependency() {
		if (!tools.checked) delegated.checked = false;
		delegated.disabled = loading || !tools.checked;
		root.setAttribute("aria-required", tools.checked ? "true" : "false");
	}
	function addSummary(label, value) {
		const term = document.createElement("dt"), detail = document.createElement("dd");
		term.textContent = label; detail.textContent = value; activeSummary.append(term, detail);
	}
	function render(value) {
		projection = value;
 macMemory.value=value.saved.mac_memory_percent||100;macSwap.value=value.saved.mac_swap_growth_gb||4;
 dnsLogging.value=value.saved.dns_logging||"";
 dnsStatus.textContent=value.active.dns_logging==="full"?"Managed logging enabled. Full capture: administrator setup required; not verified active.":value.active.dns_logging==="managed"?"Managed logging enabled. Daily DNS JSONL files are stored beside the runtime database; see Logging for coverage.":"DNS logging is off.";
 advertiseEnabled.checked=value.saved.remote_advertisement.enabled;advertiseInterface.value=value.saved.remote_advertisement.interface;advertiseName.value=value.saved.remote_advertisement.name;advertiseSSH.value=value.saved.remote_advertisement.ssh_port||"";
 skillsEnabled.checked=value.saved.skills_enabled;skillsDraft.checked=value.saved.skills_auto_draft;skillsRoot.value=value.saved.skills_root;skillsScope.value=value.saved.skills_scope;
		tools.checked = value.saved.tools_enabled; delegated.checked = value.saved.delegate_read_tools; specialistsAllowCloud.checked = value.saved.specialists_allow_cloud; root.value = value.saved.read_root;
		badge.hidden = !value.restart_required; activeSummary.replaceChildren();
		addSummary("Skill usage",value.active.skills_enabled?"Enabled":"Disabled");addSummary("Skill creation",value.active.skills_enabled&&value.active.skills_auto_draft?"Allowed":"Disabled");
 addSummary("Model tool use", value.active.tools_enabled ? "Enabled" : "Disabled");
		addSummary("Delegated reads", value.active.delegate_read_tools ? "Enabled" : "Disabled");
		addSummary("Specialist models", value.active.specialists_allow_cloud ? "Local and cloud" : "Local only");
		addSummary("Remote advertisement in loaded config",value.active.remote_advertisement.enabled?"Enabled (remote host status not checked)":"Disabled");
 addSummary("Advertised interface",value.active.remote_advertisement.interface||"Not configured");addSummary("Advertised TLS name",value.active.remote_advertisement.name||"Not configured");addSummary("SSH port hint",value.active.remote_advertisement.ssh_port?String(value.active.remote_advertisement.ssh_port):"None");
 addSummary("Read root", value.active.read_root || "Not configured");
		syncDependency(); validation.hidden = true;
		setStatus(value.restart_required ? "Settings are saved. Restart the owning service to activate them; remote advertisement requires a remote-host restart." : "Saved settings match the running daemon.", false);
	}
	function dirty(){return projection && Object.entries(formValue()).some(([key,value])=>value&&typeof value==="object"?Object.entries(value).some(([k,v])=>v!==projection.saved[key][k]):value!==(key==="dns_logging"?(projection.saved[key]||""):key==="mac_memory_percent"?(projection.saved[key]||100):key==="mac_swap_growth_gb"?(projection.saved[key]||4):projection.saved[key]));}
 function load() {
  if(dirty()){setStatus("Your unsaved edits are preserved. Use Reset changes before refreshing saved settings.",false);return Promise.resolve();}
		setBusy(true); setStatus("Loading settings…", false);
		return window.NexusLive.fetch(base + "/api/v1/settings", {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}}).then(response => {
			if (!response.ok) throw new Error("settings unavailable");
			return response.json();
		}).then(value => { if (!validProjection(value)) throw new Error("invalid settings"); render(value); }).catch(() => {
			setStatus("Settings could not be loaded. Previously loaded values and edits are preserved.", true);
		}).finally(() => setBusy(false));
	}
	function formValue() { return {...(projection?.saved.commander_model!==undefined?{commander_model:projection.saved.commander_model}:{}),mac_memory_percent:Number(macMemory.value),mac_swap_growth_gb:Number(macSwap.value),dns_logging:dnsLogging.value,remote_advertisement:{enabled:advertiseEnabled.checked,interface:advertiseInterface.value.trim(),name:advertiseName.value.trim(),ssh_port:advertiseSSH.value.trim()===""?0:Number(advertiseSSH.value)},skills_enabled:skillsEnabled.checked,skills_auto_draft:skillsDraft.checked,skills_root:skillsRoot.value.trim(),skills_scope:skillsScope.value.trim(),tools_enabled: tools.checked, delegate_read_tools: delegated.checked, read_root: root.value.trim(), specialists_allow_cloud: specialistsAllowCloud.checked}; }
	function validate(value) {
		let message = "";
		if (!validAdvertisement(value.remote_advertisement)) message = "Use a valid interface, lowercase TLS name, and optional SSH port from 1 to 65535. Interface and TLS name are required when discovery is enabled.";
 else if (value.tools_enabled && !value.read_root) message = "An absolute read root is required when file tools are enabled.";
		else if (!validAccess(value)) message = "Enter a valid absolute directory path and review the dependent permissions.";
		validation.textContent = message; validation.hidden = !message; root.setAttribute("aria-invalid", message ? "true" : "false");
		return !message;
	}
	function saveSettings(event) {
		event.preventDefault();
		if (!projection || !csrf || loading) return;
		const settings = formValue();
		if (!validate(settings)) return;
		setBusy(true); setStatus("Saving validated configuration…", false);
		window.NexusLive.fetch(base + "/api/v1/settings", {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json", Accept: "application/json", "X-Darwin-CSRF": csrf}, body: JSON.stringify({version: 1, expected_digest: projection.digest, settings})}).then(response => {
			if (response.status === 409) throw new Error("conflict");
			if (!response.ok) throw new Error("save failed");
			return response.json();
		}).then(value => { if (!validProjection(value)) throw new Error("invalid settings"); render(value); }).catch(error => {
			if (error.message === "conflict") { setStatus("The configuration changed elsewhere. Your edits are preserved. Reset changes, then Refresh to load the latest values before saving again.", true); }
			else setStatus("Settings were not saved. Review the values and try again.", true);
		}).finally(() => setBusy(false));
	}
 let liveSettingsBusy=false;
 async function reconcileSettings(){
  if(loading||liveSettingsBusy||!projection||form.contains(document.activeElement))return;
  liveSettingsBusy=true;
  try{
   const response=await window.NexusLive.fetch(base+"/api/v1/settings",{credentials:"same-origin",cache:"no-store"});if(!response.ok)throw Error();
   const value=await response.json();if(!validProjection(value))throw Error();
   if(value.digest===projection.digest&&JSON.stringify(value.active)===JSON.stringify(projection.active))return;
   // Keep the original digest while a draft is dirty, so save still detects conflicts.
   if(Object.entries(formValue()).some(([key,value])=>value&&typeof value==="object"?Object.entries(value).some(([k,v])=>v!==projection.saved[key][k]):value!==(key==="dns_logging"?(projection.saved[key]||""):key==="mac_memory_percent"?(projection.saved[key]||100):key==="mac_swap_growth_gb"?(projection.saved[key]||4):projection.saved[key]))){setStatus("Settings changed elsewhere. Your unsaved edits are preserved; use Reset or Refresh to load current settings.",false);return;}
   render(value);
  }catch{setStatus("Live settings check unavailable. Your edits are preserved; reconnecting automatically.",true);return false;}
  finally{liveSettingsBusy=false;}
 }
 window.NexusLive.watch("settings",reconcileSettings,{ready:()=>!loading});
	tools.addEventListener("change", syncDependency);
	delegated.addEventListener("change", () => validate(formValue()));
	root.addEventListener("input", () => { if (!validation.hidden) validate(formValue()); });
	form.addEventListener("submit", saveSettings);
	reset.addEventListener("click", () => { if (projection) render(projection); });
	refresh.addEventListener("click", load);
	setBusy(true);
	window.NexusLive.fetch(base + "/api/v1/session/csrf", {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json"}, body: JSON.stringify({version: 1})}).then(response => {
		if (!response.ok) throw new Error("session unavailable");
		return response.json();
	}).then(value => { if (!value || value.version !== 1 || typeof value.csrf_token !== "string" || !value.csrf_token) throw new Error("invalid session"); csrf = value.csrf_token; window.NexusRemoteMembership.mount(base, csrf); connection.textContent = "Connected"; return load(); }).catch(() => { connection.textContent = "Session needs attention"; setBusy(false); setStatus("The browser session needs attention before settings can be changed.", true); });
})();

(() => {
 const base=document.body.dataset.basePath||'';
 if(!window.NexusRoutes?.settings(window.location.pathname.slice(base.length)))return;
 const select=document.querySelector('#vllm-instance'),status=document.querySelector('#vllm-status');
 const buttons=['refresh','start','stop'].map(x=>document.querySelector('#vllm-'+x));let busy=false;
 async function action(name){if(busy||!select.value)return;busy=true;select.disabled=true;buttons.forEach(b=>b.disabled=true);
  try{const session=await window.NexusLive.fetch(base+'/api/v1/session/csrf',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({version:1})});if(!session.ok)throw Error();const csrf=await session.json();
   const response=await window.NexusLive.fetch(base+'/api/v1/remote-runner',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json','X-Darwin-CSRF':csrf.csrf_token},body:JSON.stringify({version:1,instance:select.value,action:name})});if(!response.ok)throw Error();const value=await response.json();
   if(value.version!==1||typeof value.enabled!=='boolean'||!['disabled','active','inactive','activating','deactivating','failed','unavailable'].includes(value.state))throw Error();
   status.textContent='vLLM: '+value.state+(value.model_id?' · '+value.model_id:'');
  }catch{status.textContent='Runner unavailable, access denied, or host busy. No automatic retry of start/stop.';}
  finally{busy=false;select.disabled=false;buttons.forEach(b=>b.disabled=!select.value);}
 }
 buttons[0].addEventListener('click',()=>action('status'));buttons[1].addEventListener('click',()=>action('start'));buttons[2].addEventListener('click',()=>action('stop'));select.addEventListener('change',()=>action('status'));
 async function load(){try{const response=await window.NexusLive.fetch(base+'/api/v1/remote-membership',{credentials:'same-origin',cache:'no-store'});if(!response.ok)throw Error();const page=await response.json();if(page.version!==1||!Array.isArray(page.registry?.peers))throw Error();
  select.replaceChildren();for(const peer of page.registry.peers){if(!peer.operations?.includes('runner'))continue;const option=document.createElement('option');option.value=peer.id;option.textContent=peer.id;select.append(option);}
  buttons.forEach(b=>b.disabled=!select.value);if(select.value)await action('status');else status.textContent='No paired host grants runner control. Enable runner permission in the host trust configuration.';
 }catch{status.textContent='Runner connections could not be loaded.';buttons.forEach(b=>b.disabled=true);}}
 load();
})();
