"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.NexusRoutes || !window.NexusRoutes.models(relative)) return;
	const view = document.querySelector("#models-view"), chat = document.querySelector("#chat-view"), workboards = document.querySelector("#workboard-view"), settings = document.querySelector("#settings-view");
	const refresh = document.querySelector("#refresh-models"), liveStatus = document.querySelector("#models-live-status");
	const total = document.querySelector("#local-model-total"), totalDetail = document.querySelector("#local-model-total-detail");
	const localList = document.querySelector("#local-model-list"), cloudList = document.querySelector("#cloud-model-list");
	const localState = document.querySelector("#local-model-state"), cloudState = document.querySelector("#cloud-model-state");
	const localCount = document.querySelector("#local-model-count"), cloudCount = document.querySelector("#cloud-model-count");
	const connection = document.querySelector("#connection-state");
	const digestPattern = /^[0-9a-f]{64}$/;
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
	const expanded = new Set();
	let modelSignature = "", commanderID="";
	let timer = 0, refreshMS = 10000, loading = false, loaded = false, stopped = false;
	chat.hidden = true; workboards.hidden = true; settings.hidden = true; view.hidden = false;

	function bytes(value) {
		if (!Number.isSafeInteger(value) || value < 0) return "Unknown";
		const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
		let amount = value, unit = 0;
		while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit++; }
		return amount.toLocaleString(undefined, {maximumFractionDigits: unit ? 1 : 0}) + " " + units[unit];
	}
	function time(value) { const parsed = new Date(value); return Number.isFinite(parsed.getTime()) ? parsed.toLocaleString() : "Unknown"; }
	function text(value, max, empty = true) { return typeof value === "string" && value.length <= max && (empty || value.length > 0) && !/[\u0000-\u001f\u007f-\u009f]/u.test(value); }
	function optionalInteger(value) { return value === undefined || Number.isSafeInteger(value) && value >= 0; }
	function optionalTime(value) { return value === undefined || typeof value === "string" && Number.isFinite(Date.parse(value)); }
	function validLocalProvider(item) {
		return item && idPattern.test(item.provider) && ((item.status === "available" && item.status_code === "available") ||
			(item.status === "unavailable" && item.status_code === "discovery_failed")) && typeof item.checked_at === "string" && Number.isFinite(Date.parse(item.checked_at));
	}
	function validModel(item) {
		return item && idPattern.test(item.id) && text(item.provider, 128, false) && text(item.model, 512, false) && ["local", "cloud"].includes(item.locality) &&
			["configured", "enabled", "installed", "usable"].every(key => typeof item[key] === "boolean") && Array.isArray(item.capabilities) && item.capabilities.length <= 128 &&
			item.capabilities.every(value => text(value, 128, false)) && new Set(item.capabilities).size === item.capabilities.length &&
			["healthy", "degraded", "unavailable", "disabled", "unknown"].includes(item.health) && optionalInteger(item.context_tokens) &&
			(typeof item.estimated_cost === "undefined" || typeof item.estimated_cost === "number" && Number.isFinite(item.estimated_cost) && item.estimated_cost >= 0) &&
			optionalInteger(item.ram_bytes) && optionalInteger(item.vram_bytes) && optionalInteger(item.size_bytes) &&
			(item.digest === undefined || digestPattern.test(item.digest)) && ["family", "parameter_size", "quantization", "failure_domain", "status_code"].every(key => item[key] === undefined || text(item[key], 128)) && optionalTime(item.modified_at) && optionalTime(item.health_checked_at) &&
			(!item.usable || item.configured && item.enabled && item.health === "healthy") && (!item.enabled || item.configured) &&
			(item.locality !== "cloud" || !item.installed && item.size_bytes === undefined && item.digest === undefined && item.modified_at === undefined);
	}
	function validPage(value) {
		return value && value.version === 1 && value.availability === "available" && digestPattern.test(value.config_id) && Number.isFinite(Date.parse(value.refreshed_at)) &&
			Number.isSafeInteger(value.local_total_bytes) && value.local_total_bytes >= 0 && value.local_total_kind === "logical_deduplicated" &&
			["complete", "partial"].includes(value.local_total_coverage) && Number.isSafeInteger(value.refresh_interval_ms) && value.refresh_interval_ms >= 5000 && value.refresh_interval_ms <= 300000 &&
			Number.isSafeInteger(value.local_unknown_size_count) && value.local_unknown_size_count >= 0 && value.local_unknown_size_count <= 256 && Array.isArray(value.local_providers) && value.local_providers.length <= 64 && value.local_providers.every(validLocalProvider) && new Set(value.local_providers.map(item => item.provider)).size === value.local_providers.length &&
			Array.isArray(value.models) && value.models.length <= 256 && value.models.every(validModel) && new Set(value.models.map(item => item.id)).size === value.models.length;
	}
	function element(name, className, value) { const node = document.createElement(name); if (className) node.className = className; if (value !== undefined) node.textContent = value; return node; }
	function detail(list, label, value) { list.append(element("dt", "", label), element("dd", "", value)); }
	function notice(node, message, failed) { node.textContent = message; node.classList.toggle("error", Boolean(failed)); node.hidden = false; }
	function badge(label, className) { return element("span", "model-badge" + (className ? " " + className : ""), label); }
	function card(item) {
		const node = element("li", "model-card"), heading = element("button", "model-card-toggle"), identity = element("span", "model-card-identity"), badges = element("span", "model-badges");
		const open = expanded.has(item.id), detailsID = "model-details-" + item.id;
		heading.type = "button"; heading.setAttribute("aria-expanded", String(open)); heading.setAttribute("aria-controls", detailsID);
		identity.append(element("strong", "model-card-name", item.model), element("span", "model-card-provider", item.provider));
		badges.append(badge(item.status_code === "catalog_only" ? "Discovered · access unverified" : item.usable ? "Usable" : item.health === "disabled" ? "Disabled" : "Unavailable", "model-status-badge " + (item.usable ? "usable" : "unavailable")));
		if (item.locality === "local") badges.append(badge(item.installed ? "Installed" : "Not installed", "model-install-badge"));
		if (item.configured) badges.append(badge("Configured", "model-config-badge"));
		if (item.locality === "local") badges.append(badge(bytes(item.size_bytes), "model-size-badge"));
		const disclosure = element("span", "model-disclosure", open ? "−" : "+"); disclosure.setAttribute("aria-hidden", "true");
		heading.append(identity, badges, disclosure); node.append(heading);
		const details = element("dl", "model-details");
		details.id = detailsID; details.hidden = !open; node.classList.toggle("expanded", open);
		if (item.locality === "local") detail(details, "Size on disk", bytes(item.size_bytes));
		detail(details, "Health", item.health + (item.status_code ? " · " + item.status_code.replaceAll("_", " ") : ""));
		if (item.health_checked_at) detail(details, "Health checked", time(item.health_checked_at));
		detail(details, "Estimated cost", item.estimated_cost === undefined ? "Unknown" : String(item.estimated_cost));
  detail(details, "RAM / VRAM", bytes(item.ram_bytes) + " / " + bytes(item.vram_bytes));
  detail(details, "Failure domain", item.failure_domain || "Unknown");
  detail(details, "Capabilities", item.capabilities.length ? item.capabilities.join(", ") : "None declared");
		if (item.context_tokens !== undefined) detail(details, "Context", item.context_tokens.toLocaleString() + " tokens");
		if (item.parameter_size) detail(details, "Parameters", item.parameter_size);
		if (item.quantization) detail(details, "Quantization", item.quantization);
		if (item.family) detail(details, "Family", item.family);
		if (item.modified_at) detail(details, "Modified", time(item.modified_at));
		if (item.digest) detail(details, "Digest", item.digest.slice(0, 12) + "…");
		heading.addEventListener("click", () => {
			const next = heading.getAttribute("aria-expanded") !== "true";
			heading.setAttribute("aria-expanded", String(next)); details.hidden = !next; node.classList.toggle("expanded", next);
			disclosure.textContent = next ? "−" : "+";
			if (next) expanded.add(item.id); else expanded.delete(item.id);
		});
		node.append(details);
        if(item.id===commanderID) badges.append(badge("Commander","usable"));
        if(item.configured&&item.enabled&&item.capabilities.includes("chat")&&item.id!==commanderID){
          const choose=element("button","commander-choice","Set as commander"),message=element("span","model-use-feedback");choose.type="button";message.setAttribute("role","status");node.append(choose,message);
          choose.addEventListener("click",async()=>{choose.disabled=true;message.textContent="Saving commander…";try{
           const get=await window.NexusLive.fetch(base+"/api/v1/settings",{credentials:"same-origin",cache:"no-store"});if(!get.ok)throw Error();const current=await get.json();
           const tokenResponse=await window.NexusLive.fetch(base+"/api/v1/session/csrf",{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});if(!tokenResponse.ok)throw Error();const token=await tokenResponse.json();
           const response=await window.NexusLive.fetch(base+"/api/v1/settings",{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json","X-Darwin-CSRF":token.csrf_token},body:JSON.stringify({version:1,expected_digest:current.digest,settings:{...current.saved,commander_model:item.id}})});if(!response.ok)throw Error();const saved=await response.json();message.textContent=saved.restart_required?"Commander saved. Restart NexusRouter when idle to activate.":"Commander selected.";
          }catch{message.textContent="Could not save commander. Refresh and try again.";}finally{choose.disabled=false;}});
        }
        if(window.NexusModelUse)window.NexusModelUse.attach(node,"local",item.id,item.configured); return node;
	}
	function render(page) {
		const locals = page.models.filter(item => item.locality === "local"), clouds = page.models.filter(item => item.locality === "cloud");
		const present = new Set(page.models.map(item => item.id)); expanded.forEach(id => { if (!present.has(id)) expanded.delete(id); });
		commanderID=page.commander_id||"";
		const signature = JSON.stringify([page.models,commanderID]);
		if (signature !== modelSignature && !localList.contains(document.activeElement) && !cloudList.contains(document.activeElement)) { localList.replaceChildren(...locals.map(card)); cloudList.replaceChildren(...clouds.map(card)); modelSignature = signature; }
		localCount.textContent = String(locals.length); cloudCount.textContent = String(clouds.length);
		localState.hidden = locals.length > 0; cloudState.hidden = clouds.length > 0 && page.cloud_discovery_status === undefined;
        if(page.cloud_discovery_status) notice(cloudState,({complete:"Provider catalogs discovered. New models require configuration before use; catalog presence does not verify access.",partial:"Cloud discovery incomplete: a provider failed or the inventory limit was reached.",disabled:"Cloud discovery is disabled in local-only mode.",not_configured:"No cloud providers configured."})[page.cloud_discovery_status]||"Cloud discovery unavailable.",page.cloud_discovery_status === "partial");
		if (!locals.length) notice(localState, "No local models were discovered or configured.", false);
		if (!clouds.length) notice(cloudState, "No cloud models are configured.", false);
		total.textContent = page.local_total_coverage === "partial" && page.local_total_bytes === 0 ? "Unavailable" : bytes(page.local_total_bytes);
		total.title = page.local_total_bytes.toLocaleString() + " bytes";
		const unknown = page.local_unknown_size_count ? " " + page.local_unknown_size_count + " model size" + (page.local_unknown_size_count === 1 ? " is" : "s are") + " unknown." : "";
		const unavailable = page.local_providers.filter(item => item.status === "unavailable").map(item => item.provider);
		const partial = page.local_total_coverage === "partial" ? "Partial logical total." : "Complete provider-reported logical total.";
		const failed = unavailable.length ? " Unavailable local provider" + (unavailable.length === 1 ? ": " : "s: ") + unavailable.join(", ") + "." : "";
		totalDetail.textContent = partial + " " + page.local_total_bytes.toLocaleString() + " bytes are reported; duplicate model digests are counted once." + unknown + failed + " Shared provider layers may use less physical space.";
		refreshMS = 5000;
		liveStatus.textContent = "Inventory refreshed " + time(page.refreshed_at) + ". Updates automatically every " + (refreshMS / 1000).toLocaleString() + " seconds while this page is visible.";
		liveStatus.classList.remove("error"); connection.textContent = "Connected"; loaded = true;
	}
	async function load() {
		if (loading || stopped || document.hidden) return;
		window.clearTimeout(timer);
		loading = true; refresh.disabled = true;
		if (!loaded) liveStatus.textContent = "Loading model inventory…";
		try {
			const response = await window.NexusLive.fetch(base + "/api/v1/models", {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}});
			if (!response.ok) throw new Error("inventory unavailable");
			const page = await response.json();
			if (!validPage(page)) throw new Error("invalid inventory");
			render(page);
		} catch (_) {
			liveStatus.textContent = loaded ? "Inventory refresh failed. Showing the last verified snapshot; retrying automatically." : "Model inventory could not be loaded.";
			liveStatus.classList.add("error"); connection.textContent = "Inventory needs attention";
		} finally { loading = false; refresh.disabled = false; schedule(); }
	}
	function schedule() {}
 window.NexusLive.watch("models",load,{interval:5000,ready:()=>!loading&&!stopped});
	refresh.addEventListener("click", load);
	document.addEventListener("visibilitychange", () => { if (document.hidden) window.clearTimeout(timer); else load(); });
	window.addEventListener("beforeunload", () => { stopped = true; window.clearTimeout(timer); });
	load();
})();
