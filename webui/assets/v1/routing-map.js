"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.NexusRoutes || (!window.NexusRoutes.routing(relative) && !window.NexusRoutes.elimination(relative))) return;
	const allViews = ["#chat-view", "#workboard-view", "#models-view", "#settings-view", "#routing-view", "#elimination-view"];
	const jobs = [
		{key:"coding", domain:"code", label:"Coding", capabilities:["code","coding","reasoning"]},
		{key:"ocr", label:"OCR / document vision", capabilities:["ocr","vision","image"]},
		{key:"cli", domain:"commandline", label:"CLI and terminal", capabilities:["cli","terminal","tools","code"]},
		{key:"general", label:"General use", capabilities:["chat","reasoning"]},
		{key:"research", label:"Research & document understanding", capabilities:["chat","reasoning","summarize"]},
		{key:"data_analysis", label:"Data analysis & databases", capabilities:["code","reasoning"]},
		{key:"reasoning", label:"Reasoning & planning", capabilities:["chat","reasoning"]},
		{key:"workflow", label:"Workflow & API automation", capabilities:["tools"]},
		{key:"translation", label:"Translation & multilingual", capabilities:["chat","translation","multilingual"]},
		{key:"audio", label:"Audio & speech", capabilities:["audio","speech","transcription","speech_to_text","text_to_speech","audio_generation"]},
		{key:"image_generation", label:"Image generation", capabilities:["image_generation"]},
		{key:"video_generation", label:"Video generation", capabilities:["video_generation"]},
		{key:"writing", label:"General writing", capabilities:["writing","chat","summarize"]},
		{key:"creative", label:"Creative work", capabilities:["creative","writing","chat"]}
	];
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
	let snapshot = null, routingTimer = 0, routingGeneration = 0, routingFailures = 0;
	let creativePreference = "", creativeSignature = "";
	const expandedModels = new Set(new URLSearchParams(window.location.search).getAll("expanded").filter(value => { const parts=value.split("|"); return parts.length===2 && jobs.some(job => job.key===parts[0]) && idPattern.test(parts[1]); }));
	for (const selector of allViews) document.querySelector(selector).hidden = selector !== (window.NexusRoutes.routing(relative) ? "#routing-view" : "#elimination-view");

	function element(name, className, value) { const node = document.createElement(name); if (className) node.className = className; if (value !== undefined) node.textContent = value; return node; }
	function validModel(item) { return item && idPattern.test(item.id) && typeof item.model === "string" && item.model.length <= 512 && (item.reasoning_effort === undefined || ["none","minimal","low","medium","high","xhigh","max","ultra"].includes(item.reasoning_effort)) && ["local","cloud"].includes(item.locality) && Array.isArray(item.capabilities) && item.capabilities.every(value => idPattern.test(value)) && (item.selected_context_tokens === undefined || Number.isSafeInteger(item.selected_context_tokens) && item.selected_context_tokens > 0 && Number.isSafeInteger(item.context_tokens) && item.selected_context_tokens <= item.context_tokens) && (item.context_selection_status === undefined || ["selected","blocked","unavailable"].includes(item.context_selection_status) && (item.context_selection_status === "selected" ? item.selected_context_tokens !== undefined : item.selected_context_tokens === undefined)) && typeof item.usable === "boolean"; }
	function validFitness(item) { return item && idPattern.test(item.model_id) && typeof item.domain === "string" && item.domain.length > 0 && item.domain.length <= 128 && typeof item.profile === "string" && item.profile.length > 0 && item.profile.length <= 128 && Number.isSafeInteger(item.samples) && item.samples > 0 && (item.fallback_eligible === undefined || typeof item.fallback_eligible === "boolean") && [item.quality,item.reliability,item.compliance,item.score,item.confidence].every(value => Number.isFinite(value) && value >= 0 && value <= 1); }
	function validFallbacks(value) { return value === undefined || Array.isArray(value) && value.length <= 128 && value.every(item => item && [item.domain,item.profile,item.source_domain,item.source_profile].every(part => typeof part === "string" && idPattern.test(part)) && (item.domain !== item.source_domain || item.profile !== item.source_profile)) && new Set(value.map(item => item.domain+"|"+item.profile)).size === value.length; }
    function validRankings(rows, models) { return rows === undefined || Array.isArray(rows) && rows.length <= jobs.length && new Set(rows.map(row => row.key)).size === rows.length && rows.every(row => row && (row.requires_evidence === undefined || typeof row.requires_evidence === "boolean") && [row.key,row.domain,row.profile].every(value => idPattern.test(value)) && Array.isArray(row.models) && row.models.length <= 3 && new Set(row.models.map(item => item.model_id)).size === row.models.length && row.models.every(item => item && models.some(model => model.id === item.model_id) && [item.domain,item.profile].every(value => idPattern.test(value)) && [item.score,item.confidence].every(value => Number.isFinite(value) && value >= 0 && value <= 1) && Number.isSafeInteger(item.samples) && item.samples >= 0 && (!row.requires_evidence || item.samples > 0))); }
	function validSnapshot(value) { const concurrency = value && value.local_concurrency === "auto" ? 1 : Number(value && value.local_concurrency); return value && value.version === 1 && value.availability === "available" && Array.isArray(value.models) && value.models.length <= 256 && value.models.every(validModel) && validRankings(value.rankings,value.models) && Array.isArray(value.fitness) && value.fitness.length <= 4096 && value.fitness.every(validFitness) && validFallbacks(value.evidence_fallbacks) && (value.commander_id === undefined || idPattern.test(value.commander_id)) && (value.commander_fallback_id === undefined || idPattern.test(value.commander_fallback_id)) && (value.local_concurrency === "auto" || Number.isInteger(concurrency) && concurrency >= 1 && concurrency <= 64 && String(concurrency) === value.local_concurrency) && ["reject","wait"].includes(value.local_pressure_policy) && Number.isFinite(value.local_ram_limit_pct) && value.local_ram_limit_pct > 0 && value.local_ram_limit_pct <= 100 && Number.isFinite(value.local_vram_limit_pct) && value.local_vram_limit_pct > 0 && value.local_vram_limit_pct <= 100 && typeof value.managed_residency === "boolean" && typeof value.specialists_allow_cloud === "boolean"; }
	async function inventory() { const response = await window.NexusLive.fetch(base + "/api/v1/models", {credentials:"same-origin", cache:"no-store", headers:{Accept:"application/json"}}); if (!response.ok) throw new Error("inventory unavailable"); const value = await response.json(); if (!validSnapshot(value)) throw new Error("invalid inventory"); return value; }
	function learned(model, job) {
        const scope = (snapshot.rankings || []).find(item => item.key === job.key && item.domain === (job.domain || job.key));
        return scope ? scope.models.find(item => item.model_id === model.id) || null : null;
    }
    function scopeLabel(job) {
        const scope = (snapshot.rankings || []).find(item => item.key === job.key && item.domain === (job.domain || job.key));
        return scope ? scope.domain + " / " + scope.profile : "Evidence scope unavailable";
    }
    function emptyRankingLabel(job) {
        const scope = (snapshot.rankings || []).find(item => item.key === job.key && item.domain === (job.domain || job.key));
        return scope && scope.requires_evidence ? "Insufficient measured evidence. No eligible models have evaluations for this profile." : "No eligible backend ranking available.";
    }
    function ranked(job) {
        const scope = (snapshot.rankings || []).find(item => item.key === job.key && item.domain === (job.domain || job.key));
        return scope ? scope.models.map(item => snapshot.models.find(model => model.id === item.model_id)).filter(model => model && model.usable && model.context_selection_status !== "blocked" && (snapshot.specialists_allow_cloud || model.locality === "local")) : [];
    }
	function commander() {
		if (snapshot.commander_id) return snapshot.models.find(model => model.id === snapshot.commander_id) || null;
		return snapshot.models.find(model => model.capabilities.includes("orchestration")) || snapshot.models.find(model => /commander|coordinator|brain/i.test(model.id)) || null;
	}
	function contextLabel(model) { if (!Number.isSafeInteger(model.context_tokens) || model.context_tokens < 1) return "context unknown"; if (model.context_tokens % 1024 === 0) return (model.context_tokens / 1024) + "K context ceiling"; return model.context_tokens.toLocaleString() + " context ceiling"; }
	function selectedContextLabel(model) { if (model.context_selection_status === "blocked") return "No safe context tier"; if (model.context_selection_status === "unavailable") return "Unavailable"; if (!Number.isSafeInteger(model.selected_context_tokens) || model.selected_context_tokens < 1) return "Not yet selected"; if (model.selected_context_tokens % 1024 === 0) return (model.selected_context_tokens / 1024) + "K"; return model.selected_context_tokens.toLocaleString(); }
	function byteLabel(value) { if (!Number.isSafeInteger(value) || value < 1) return "Unknown"; const units=["B","KiB","MiB","GiB","TiB"]; let amount=value,index=0; while (amount>=1024 && index<units.length-1) { amount/=1024; index++; } return (amount>=10 || index===0 ? Math.round(amount) : Math.round(amount*10)/10) + " " + units[index]; }
	function fact(label, value) { const row=element("div","route-model-fact"); row.append(element("span","",label),element("strong","",value)); return row; }
	function persistExpandedModels() { const url=new URL(window.location.href); url.searchParams.delete("expanded"); for (const key of [...expandedModels].sort()) url.searchParams.append("expanded",key); window.history.replaceState(null,"",url); }
	function modelChip(model, index, job) {
		const node=element("article","route-model"), evidence=learned(model,job), disclosure=element("details","route-model-details"), summary=element("summary","route-model-summary");
		const expansionKey=job.key+"|"+model.id; disclosure.open=expandedModels.has(expansionKey); summary.addEventListener("click",()=>{ if (disclosure.open) expandedModels.delete(expansionKey); else expandedModels.add(expansionKey); persistExpandedModels(); });
		const synopsis=evidence ? evidence.score.toFixed(3) + " routing score · " + (evidence.samples ? evidence.samples + " samples" : "unmeasured policy prior") + " · " + contextLabel(model) : model.locality + " · " + contextLabel(model) + " · ranking unavailable";
		summary.append(element("strong","",model.model),element("small","",synopsis)); disclosure.append(summary);
		const facts=element("div","route-model-facts"); facts.append(fact("Provider",model.provider),fact("NexusRouter ID",model.id),fact("Locality",model.locality),fact("Health",model.health),fact("Capabilities",model.capabilities.length ? model.capabilities.join(", ") : "None advertised"),fact("Selected context",selectedContextLabel(model)),fact("Advertised maximum",contextLabel(model)),fact("Estimated RAM",byteLabel(model.ram_bytes)),fact("Estimated VRAM",byteLabel(model.vram_bytes)));
        if (evidence) facts.append(fact("Evidence domain",evidence.domain),fact("Evidence profile",evidence.profile),fact("Target scope",scopeLabel(job)),fact("Routing score",evidence.score.toFixed(6)+" (not a pass probability)"),fact("Confidence",Math.round(evidence.confidence*1000)/10+"%"),fact("Samples",String(evidence.samples)));

		disclosure.append(facts); node.append(element("span","route-rank",String(index+1).padStart(2,"0")),disclosure); return node;
	}
	function circuitPath(svg, d, className) {
		const path = document.createElementNS(svg.namespaceURI, "path"); path.setAttribute("d", d); path.setAttribute("class", className); svg.append(path);
		for (const direction of ["out", "in"]) { const flow = document.createElementNS(svg.namespaceURI, "path"); flow.setAttribute("d", d); flow.setAttribute("class", "routing-flow routing-flow-" + direction); svg.append(flow); }
	}
	function drawBranches() {
		const network = document.querySelector("#routing-network"), core = document.querySelector(".commander-core"), svg = document.querySelector("#routing-branches"), cards = [...document.querySelectorAll("#specialist-grid .specialist-card, #remote-route-grid .remote-route-card")];
		if (!network || !core || !svg || !cards.length) return;
		const box = network.getBoundingClientRect(), root = core.getBoundingClientRect();
		if (!box.width || !box.height) return;
		svg.setAttribute("viewBox", "0 0 " + box.width + " " + box.height); svg.replaceChildren();
		const rootX = root.left - box.left + root.width / 2, rootY = root.bottom - box.top;
		const compact = window.matchMedia("(max-width: 42rem)").matches;
		const targets = cards.map(card => {
			const target = card.getBoundingClientRect(), left = target.left - box.left, top = target.top - box.top, center = left + target.width / 2;
			return compact ? {x:center, y:top} : {x:center < rootX ? left + target.width : left, y:top + Math.min(52, target.height * .3)};
		});
		const trunkY = Math.max(...targets.map(target => target.y));
		circuitPath(svg, `M ${rootX} ${rootY} L ${rootX} ${trunkY}`, "routing-trunk");
		for (const target of targets) {
			const shoulderY = Math.max(rootY + 26, target.y - (compact ? 42 : 74)), bendX = rootX + (target.x - rootX) * .58;
			circuitPath(svg, `M ${rootX} ${shoulderY} L ${bendX} ${target.y} L ${target.x} ${target.y}`, "routing-branch");
			const node = document.createElementNS(svg.namespaceURI, "rect");
			node.setAttribute("x", String(target.x - 5)); node.setAttribute("y", String(target.y - 5)); node.setAttribute("width", "10"); node.setAttribute("height", "10"); node.setAttribute("class", "routing-terminal"); svg.append(node);
		}
	}

 async function loadRouting() {
		const generation = ++routingGeneration;
		window.clearTimeout(routingTimer);
		const status = document.querySelector("#routing-live-status"); status.textContent = "Synchronizing live model inventory…";
		try {
			const next = await inventory();
			if (generation !== routingGeneration) return;
			snapshot = next; const brain = commander();
			document.querySelector("#commander-model").textContent = brain ? brain.model : "Automatic router";
			document.querySelector("#commander-detail").textContent = brain ? (brain.reasoning_effort ? brain.reasoning_effort + " reasoning · " : "") + (snapshot.commander_source || "derived") + " command authority · " + contextLabel(brain) : "No explicit commander is configured; NexusRouter chooses from eligible routes.";
			const fallback = snapshot.commander_fallback_id ? snapshot.models.find(model => model.id === snapshot.commander_fallback_id) : null, fallbackNode = document.querySelector("#commander-fallback"); fallbackNode.hidden = !fallback;
			if (fallback) { document.querySelector("#commander-fallback-model").textContent = fallback.model; document.querySelector("#commander-fallback-detail").textContent = contextLabel(fallback) + " · activates after retryable cloud failure"; }
			document.querySelector("#resource-guard-title").textContent = (snapshot.local_concurrency === "auto" ? "Adaptive local model concurrency" : snapshot.local_concurrency === "1" ? "One local model at a time" : "Up to " + snapshot.local_concurrency + " local models");
			document.querySelector("#resource-guard-detail").textContent = "RAM / unified memory " + snapshot.local_ram_limit_pct + "% · VRAM " + snapshot.local_vram_limit_pct + "% · pressure " + snapshot.local_pressure_policy + (snapshot.managed_residency ? " · managed unload enabled" : "");
			document.querySelector("#specialist-policy").textContent = snapshot.specialists_allow_cloud ? "Top 3 by backend routing policy · local + cloud" : "Top 3 by backend routing policy · local endpoints only";
			const grid = document.querySelector("#specialist-grid"); grid.replaceChildren();
			for (const job of jobs) { const card = element("article","specialist-card"), heading = element("div","specialist-heading"); card.dataset.route = job.key; heading.append(element("span","job-glyph",job.label.slice(0,2).toUpperCase()), element("h3","",job.label)); card.append(heading,element("small","route-scope",scopeLabel(job)+" · policy preview")); const routes = element("div","route-stack"), models = ranked(job); if (!models.length) routes.append(element("p","route-empty",emptyRankingLabel(job))); else models.forEach((model,index) => routes.append(modelChip(model,index,job))); card.append(routes); grid.append(card); }
			drawBranches();
			const creative = ranked(jobs[jobs.length-1]), choices = document.querySelector("#creative-choices"), preference = document.querySelector("#creative-preference");
			const nextCreative = JSON.stringify(creative.map(model => [model.id, model.model]));
			if (nextCreative !== creativeSignature && !choices.contains(document.activeElement)) { choices.replaceChildren(); creativeSignature = nextCreative;
			creative.forEach(model => { const button = element("button","tron-choice",model.model); button.type="button"; button.setAttribute("aria-pressed",String(creativePreference === model.id)); button.addEventListener("click",() => { creativePreference = model.id; for (const item of choices.querySelectorAll("button")) item.setAttribute("aria-pressed",String(item === button)); preference.textContent = "User preference recorded for this consultation: " + model.model + "."; }); choices.append(button); }); }
			preference.textContent = creativePreference ? "Current consultation preference: " + creativePreference + ". The commander should ask again when the creative brief materially changes." : "No preference recorded. The commander must ask before choosing between subjective outputs.";
			status.textContent = "Grid synchronized at " + new Date().toLocaleTimeString() + " · " + snapshot.models.length + " models · learned evidence updates automatically. Scores include configured weights and decay. Dispatch still checks request constraints, exploration and available capacity.";
		routingFailures = 0;
		} catch (_) { if (generation !== routingGeneration) return; routingFailures = Math.min(routingFailures + 1, 4); status.textContent = "Routing grid unavailable. The last display was cleared."; document.querySelector("#specialist-grid").replaceChildren(); }
		window.clearTimeout(routingTimer); if (!document.hidden) routingTimer = window.setTimeout(loadRouting, Math.min(60000, 5000 * 2 ** routingFailures));
	}

	async function report(model, domain, threshold) {
		const job = jobs.find(job => job.key === domain) || {key:domain};
		const scope = (snapshot.rankings || []).find(item => item.key === job.key && item.domain === (job.domain || job.key));
		if (!scope) throw new Error("evidence scope unavailable");
		const query = new URLSearchParams({model:model.id, domain:scope.domain, profile:scope.profile, window:"100", min_samples:"20", failure_threshold:String(threshold)});
		const response = await window.NexusLive.fetch(base + "/api/v1/models/deprecation?" + query.toString(), {credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}});
		if (!response.ok) throw new Error("report unavailable"); const value = await response.json();
		if (!value || value.version !== 1 || value.configured_model_id !== model.id || typeof value.candidate !== "boolean" || !Number.isSafeInteger(value.eligible_samples) || typeof value.failure_rate !== "number" || typeof value.reason !== "string") throw new Error("invalid report");
		return value;
	}
	function tribunalCard(model, value) { const card = element("article","tribunal-card"); const rate = Math.round(value.failure_rate * 1000) / 10; card.append(element("strong","",model.model), element("span",value.candidate ? "verdict eliminate" : "verdict retain",value.candidate ? "REVIEW" : "HOLD"), element("p","",value.reason.replaceAll("_"," ")), element("small","",value.eligible_samples + " eligible samples · " + rate + "% failures · " + contextLabel(model) + " · approval required")); return card; }
	let eliminationBusy=false;
 async function loadElimination() {
 if(eliminationBusy)return;eliminationBusy=true;
		const status = document.querySelector("#elimination-live-status"), candidateList = document.querySelector("#elimination-candidates"), retainedList = document.querySelector("#elimination-retained");
		candidateList.replaceChildren(); retainedList.replaceChildren(); status.textContent = "Scanning persisted evaluation evidence…";
		try {
			snapshot = await inventory(); const domain = document.querySelector("#elimination-job").value || "general", threshold = Number(document.querySelector("#elimination-threshold").value); let candidates = 0, unavailable = 0;
			for (const model of snapshot.models.filter(item => item.configured).slice(0,64)) { try { const value = await report(model,domain,threshold); const card = tribunalCard(model,value); (value.candidate ? candidateList : retainedList).append(card); if (value.candidate) candidates++; } catch (_) { unavailable++; const card = element("article","tribunal-card"); card.append(element("strong","",model.model),element("span","verdict unknown","UNKNOWN"),element("p","","No valid bounded deprecation report is available.")); retainedList.append(card); } }
			if (!candidateList.children.length) candidateList.append(element("p","route-empty","No model crosses the evidence threshold."));
			status.textContent = "Evidence scan complete · " + candidates + " review candidates · " + unavailable + " unavailable reports.";
		} catch (_) { status.textContent = "Elimination evidence is unavailable. No recommendation was manufactured."; }
 eliminationBusy=false;
	}

	if (window.NexusRoutes.routing(relative)) { document.querySelector("#refresh-routing").addEventListener("click",loadRouting); document.addEventListener("visibilitychange",() => { window.clearTimeout(routingTimer); if (!document.hidden) loadRouting(); }); window.addEventListener("resize",drawBranches); window.addEventListener("routing-remote-updated",drawBranches); window.addEventListener("online",loadRouting);window.addEventListener("focus",loadRouting);loadRouting(); }
	if (window.NexusRoutes.elimination(relative)) {
		const select = document.querySelector("#elimination-job"); for (const job of jobs) { const option = element("option","",job.label); option.value = job.key; select.append(option); }
		document.querySelector("#refresh-elimination").addEventListener("click",loadElimination); document.querySelector("#elimination-controls").addEventListener("submit",event => { event.preventDefault(); loadElimination(); }); loadElimination();
 window.NexusLive.watch("elimination",loadElimination,{interval:30000,ready:()=>!document.querySelector("#elimination-controls").contains(document.activeElement)});
	}
})();
