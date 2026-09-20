"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.DarwinRoutes || (!window.DarwinRoutes.routing(relative) && !window.DarwinRoutes.elimination(relative))) return;
	const allViews = ["#chat-view", "#workboard-view", "#models-view", "#settings-view", "#routing-view", "#elimination-view"];
	const jobs = [
		{key:"coding", label:"Coding", capabilities:["code","coding","reasoning"]},
		{key:"ocr", label:"OCR / document vision", capabilities:["ocr","vision","image"]},
		{key:"cli", label:"CLI and terminal", capabilities:["cli","terminal","tools","code"]},
		{key:"general", label:"General use", capabilities:["chat","reasoning"]},
		{key:"image_generation", label:"Image generation", capabilities:["image_generation","image","vision"]},
		{key:"video_generation", label:"Video generation", capabilities:["video_generation","video","vision"]},
		{key:"writing", label:"General writing", capabilities:["writing","chat","summarize"]},
		{key:"creative", label:"Creative work", capabilities:["creative","writing","chat"]}
	];
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
	let snapshot = null, routingTimer = 0;
	let creativePreference = "";
	for (const selector of allViews) document.querySelector(selector).hidden = selector !== (window.DarwinRoutes.routing(relative) ? "#routing-view" : "#elimination-view");

	function element(name, className, value) { const node = document.createElement(name); if (className) node.className = className; if (value !== undefined) node.textContent = value; return node; }
	function validModel(item) { return item && idPattern.test(item.id) && typeof item.model === "string" && item.model.length <= 512 && ["local","cloud"].includes(item.locality) && Array.isArray(item.capabilities) && item.capabilities.every(value => idPattern.test(value)) && typeof item.usable === "boolean"; }
	function validSnapshot(value) { return value && value.version === 1 && value.availability === "available" && Array.isArray(value.models) && value.models.length <= 256 && value.models.every(validModel) && (value.commander_id === undefined || idPattern.test(value.commander_id)); }
	async function inventory() { const response = await fetch(base + "/api/v1/models", {credentials:"same-origin", cache:"no-store", headers:{Accept:"application/json"}}); if (!response.ok) throw new Error("inventory unavailable"); const value = await response.json(); if (!validSnapshot(value)) throw new Error("invalid inventory"); return value; }
	function score(model, job) {
		let value = model.usable ? 100 : model.enabled ? 20 : 0;
		for (let index = 0; index < job.capabilities.length; index++) if (model.capabilities.includes(job.capabilities[index])) value += 30 - index * 6;
		if (model.locality === "local") value += 2;
		if (typeof model.estimated_cost === "number") value += Math.max(0, 5 - Math.min(5, model.estimated_cost));
		return value;
	}
	function ranked(job) { return snapshot.models.filter(model => model.usable && job.capabilities.some(capability => model.capabilities.includes(capability))).sort((a,b) => score(b,job)-score(a,job) || a.id.localeCompare(b.id)).slice(0,3); }
	function commander() {
		if (snapshot.commander_id) return snapshot.models.find(model => model.id === snapshot.commander_id) || null;
		return snapshot.models.find(model => model.capabilities.includes("orchestration")) || snapshot.models.find(model => /commander|coordinator|brain/i.test(model.id)) || null;
	}
	function modelChip(model, index) { const node = element("article", "route-model"); node.append(element("span","route-rank",String(index+1).padStart(2,"0")), element("strong","",model.id), element("small","",model.model + " · " + model.locality)); return node; }

	async function loadRouting() {
		const status = document.querySelector("#routing-live-status"); status.textContent = "Synchronizing live model inventory…";
		try {
			snapshot = await inventory(); const brain = commander();
			document.querySelector("#commander-model").textContent = brain ? brain.id : "Automatic router";
			document.querySelector("#commander-detail").textContent = brain ? brain.model + " · " + (snapshot.commander_source || "derived") + " command authority" : "No explicit commander is configured; DarwinRouter chooses from eligible routes.";
			const grid = document.querySelector("#specialist-grid"); grid.replaceChildren();
			for (const job of jobs) { const card = element("article","specialist-card"), heading = element("div","specialist-heading"); heading.append(element("span","job-glyph",job.label.slice(0,2).toUpperCase()), element("h3","",job.label)); card.append(heading); const routes = element("div","route-stack"), models = ranked(job); if (!models.length) routes.append(element("p","route-empty","No eligible capability match")); else models.forEach((model,index) => routes.append(modelChip(model,index))); card.append(routes); grid.append(card); }
			const creative = ranked(jobs[jobs.length-1]), choices = document.querySelector("#creative-choices"), preference = document.querySelector("#creative-preference"); choices.replaceChildren();
			creative.forEach(model => { const button = element("button","tron-choice",model.id); button.type="button"; button.setAttribute("aria-pressed",String(creativePreference === model.id)); button.addEventListener("click",() => { creativePreference = model.id; for (const item of choices.querySelectorAll("button")) item.setAttribute("aria-pressed",String(item === button)); preference.textContent = "User preference recorded for this consultation: " + model.id + "."; }); choices.append(button); });
			preference.textContent = creativePreference ? "Current consultation preference: " + creativePreference + ". The commander should ask again when the creative brief materially changes." : "No preference recorded. The commander must ask before choosing between subjective outputs.";
			status.textContent = "Grid synchronized · " + snapshot.models.length + " models · updates automatically from daemon inventory.";
		} catch (_) { status.textContent = "Routing grid unavailable. The last display was cleared."; document.querySelector("#specialist-grid").replaceChildren(); }
		window.clearTimeout(routingTimer); if (!document.hidden) routingTimer = window.setTimeout(loadRouting, snapshot && snapshot.refresh_interval_ms || 10000);
	}

	async function report(model, domain, threshold) {
		const query = new URLSearchParams({model:model.id, domain, profile:"default", window:"100", min_samples:"20", failure_threshold:String(threshold)});
		const response = await fetch(base + "/api/v1/models/deprecation?" + query.toString(), {credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}});
		if (!response.ok) throw new Error("report unavailable"); const value = await response.json();
		if (!value || value.version !== 1 || value.configured_model_id !== model.id || typeof value.candidate !== "boolean" || !Number.isSafeInteger(value.eligible_samples) || typeof value.failure_rate !== "number" || typeof value.reason !== "string") throw new Error("invalid report");
		return value;
	}
	function tribunalCard(model, value) { const card = element("article","tribunal-card"); const rate = Math.round(value.failure_rate * 1000) / 10; card.append(element("strong","",model.id), element("span",value.candidate ? "verdict eliminate" : "verdict retain",value.candidate ? "REVIEW" : "HOLD"), element("p","",value.reason.replaceAll("_"," ")), element("small","",value.eligible_samples + " eligible samples · " + rate + "% failures · approval required")); return card; }
	async function loadElimination() {
		const status = document.querySelector("#elimination-live-status"), candidateList = document.querySelector("#elimination-candidates"), retainedList = document.querySelector("#elimination-retained");
		candidateList.replaceChildren(); retainedList.replaceChildren(); status.textContent = "Scanning persisted evaluation evidence…";
		try {
			snapshot = await inventory(); const domain = document.querySelector("#elimination-job").value || "general", threshold = Number(document.querySelector("#elimination-threshold").value); let candidates = 0, unavailable = 0;
			for (const model of snapshot.models.filter(item => item.configured).slice(0,64)) { try { const value = await report(model,domain,threshold); const card = tribunalCard(model,value); (value.candidate ? candidateList : retainedList).append(card); if (value.candidate) candidates++; } catch (_) { unavailable++; const card = element("article","tribunal-card"); card.append(element("strong","",model.id),element("span","verdict unknown","UNKNOWN"),element("p","","No valid bounded deprecation report is available.")); retainedList.append(card); } }
			if (!candidateList.children.length) candidateList.append(element("p","route-empty","No model crosses the evidence threshold."));
			status.textContent = "Evidence scan complete · " + candidates + " review candidates · " + unavailable + " unavailable reports.";
		} catch (_) { status.textContent = "Elimination evidence is unavailable. No recommendation was manufactured."; }
	}

	if (window.DarwinRoutes.routing(relative)) { document.querySelector("#refresh-routing").addEventListener("click",loadRouting); document.addEventListener("visibilitychange",() => { window.clearTimeout(routingTimer); if (!document.hidden) loadRouting(); }); loadRouting(); }
	if (window.DarwinRoutes.elimination(relative)) {
		const select = document.querySelector("#elimination-job"); for (const job of jobs) { const option = element("option","",job.label); option.value = job.key; select.append(option); }
		document.querySelector("#refresh-elimination").addEventListener("click",loadElimination); document.querySelector("#elimination-controls").addEventListener("submit",event => { event.preventDefault(); loadElimination(); }); loadElimination();
	}
})();
