"use strict";

(() => {
	const base = document.body.dataset.basePath || "";
	const taskInspector = document.querySelector("#task-inspector");
	const healthState = document.querySelector("#health-state");
	const healthDetails = document.querySelector("#health-details");
	const resourcesState = document.querySelector("#resources-state");
	const resourcesDetails = document.querySelector("#resources-details");
	const modelsState = document.querySelector("#models-state");
	const modelList = document.querySelector("#model-list");
	const modelCount = document.querySelector("#model-count");
	const routeState = document.querySelector("#route-state");
	const routeDetails = document.querySelector("#route-details");
	const routeCandidates = document.querySelector("#route-candidates");
	const usageState = document.querySelector("#usage-state");
	const usageDetails = document.querySelector("#usage-details");
	const toolsState = document.querySelector("#tools-state");
	const toolList = document.querySelector("#tool-list");
	const toolCount = document.querySelector("#tool-count");
	const loadMoreTools = document.querySelector("#load-more-tools");
	const auditsState = document.querySelector("#audits-state");
	const auditList = document.querySelector("#audit-list");
	const auditCount = document.querySelector("#audit-count");
	const loadMoreAudits = document.querySelector("#load-more-audits");
	const refreshInspector = document.querySelector("#refresh-inspector");
	const inspectionPageLimit = 25;
	const maxInspectionItems = 100;
	const maxInspectedModels = 256;
	const maxRouteCandidates = 256;
	const maxHealthChecks = 512;
	const maxInspectionText = 4096;
	const maxInspectionPages = 8;
	const presentationID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
	let selectedTaskID = "";
	let toolCursor = "";
	let auditCursor = "";
	let toolTotal = 0;
	let auditTotal = 0;
	let toolPages = 0;
	let auditPages = 0;
	let inspectorRequest = 0;
	const toolIDs = new Set();
	const auditIDs = new Set();
	const toolCursors = new Set();
	const auditCursors = new Set();

	function element(name, className, value) {
		const node = document.createElement(name);
		if (className) node.className = className;
		if (value !== undefined) node.textContent = value;
		return node;
	}

	function textBytes(value) {
		try { return new TextEncoder().encode(value).length; } catch (_) { return maxInspectionText + 1; }
	}

	function showNotice(node, message, failed) {
		setText(node, message);
		node.classList.toggle("error", Boolean(failed));
		node.hidden = false;
	}

	function requestJSON(path) {
		return window.NexusLive.fetch(base + path, {credentials: "same-origin", cache: "no-store", headers: {"Accept": "application/json"}}).then(response => {
			if (!response.ok) throw new Error("request unavailable");
			return response.json();
		});
	}

	function printable(value, max, allowEmpty) {
		return typeof value === "string" && (allowEmpty || value.length > 0) && textBytes(value) <= max && !/[\u0000-\u001f\u007f-\u009f]/u.test(value);
	}

	// Retain row and text-node identities; only changed values touch the DOM.
 function setText(node, value) {
  if(node.textContent===value)return;
  if(node.childNodes.length===1&&node.firstChild.nodeType===3)node.firstChild.nodeValue=value;
  else node.textContent=value;
 }
 const detailKeys=new Map();
 function beginDetails(list){detailKeys.set(list,new Set());}
 function finishDetails(list){const keys=detailKeys.get(list);for(const term of Array.from(list.children)){if(term.tagName==="DT"&&!keys.has(term.textContent)){term.nextElementSibling?.remove();term.remove();}}detailKeys.delete(list);}
 function detail(list,label,value){
  detailKeys.get(list)?.add(label);
  const term=Array.from(list.children).find(node=>node.tagName==="DT"&&node.textContent===label);
  if(term)setText(term.nextElementSibling,value);
  else list.append(element("dt","",label),element("dd","",value));
 }
 function reconcileItems(list,rows){
  const existing=new Map(Array.from(list.children,node=>[node.dataset.key,node]));
  const keep=new Set();
  rows.forEach(([key,value],index)=>{let node=existing.get(key);if(!node){node=element("li","");node.dataset.key=key;}setText(node,value);keep.add(node);if(list.children[index]!==node)list.insertBefore(node,list.children[index]||null);});
  for(const node of Array.from(list.children))if(!keep.has(node))node.remove();
 }
 const initializedStates=new WeakSet();
 function loading(state,list,message){if(!initializedStates.has(state)){initializedStates.add(state);showNotice(state,message,false);}}

	function knownNumber(value) {
		return typeof value === "number" && Number.isFinite(value) && value >= 0;
	}

	function finiteNumber(value) {
		return typeof value === "number" && Number.isFinite(value);
	}

	function integer(value) {
		return Number.isSafeInteger(value) && value >= 0;
	}

	function optionalInteger(value) {
		return value === undefined || integer(value);
	}

	function optionalNumber(value) {
		return value === undefined || knownNumber(value);
	}

	function formatTime(value) {
		const date = new Date(value);
		return Number.isNaN(date.getTime()) ? "Unknown time" : date.toLocaleString();
	}

	function stateLabel(value) {
		return typeof value === "string" && value ? value.replaceAll("_", " ") : "unknown";
	}

	function formatBytes(value) {
		if (!integer(value)) return "Unknown";
		const units = ["B", "KiB", "MiB", "GiB", "TiB"];
		let amount = value;
		let unit = 0;
		while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit++; }
		return amount.toLocaleString(undefined, {maximumFractionDigits: unit ? 1 : 0}) + " " + units[unit];
	}

	function validUsageTotal(total) {
		if (!total || !integer(total.records) || !integer(total.known_usage_records) || !integer(total.unknown_usage_records) ||
			total.known_usage_records + total.unknown_usage_records !== total.records || !integer(total.known_cost_records) ||
			!integer(total.unknown_cost_records) || total.known_cost_records + total.unknown_cost_records !== total.records ||
			!optionalInteger(total.input_tokens) || !optionalInteger(total.output_tokens) || (total.input_tokens === undefined) !== (total.output_tokens === undefined) ||
			!optionalNumber(total.normalized_cost)) return false;
		return (total.records > 0 && total.unknown_usage_records === 0) === (total.input_tokens !== undefined) &&
			(total.records > 0 && total.unknown_cost_records === 0) === (total.normalized_cost !== undefined);
	}

	function validUsage(usage) {
		if (!usage || !["complete", "partial", "legacy_unavailable"].includes(usage.coverage) || !integer(usage.unaccounted_routed_operations) ||
			!Number.isFinite(Date.parse(usage.calculated_at)) || (usage.coverage === "complete") !== (usage.unaccounted_routed_operations === 0)) return false;
		return ["primary", "fallback", "classifier", "summarizer", "orchestrator_audit", "optional_judge", "routed", "auxiliary", "overall"].every(name => validUsageTotal(usage[name]));
	}

	function renderUsage(usage) {
		beginDetails(usageDetails);
		detail(usageDetails, "Coverage", stateLabel(usage.coverage));
		detail(usageDetails, "Unaccounted routed operations", String(usage.unaccounted_routed_operations));
		const groups = [["Routed total", "routed"], ["Primary routed", "primary"], ["Fallback routed", "fallback"],
			["Auxiliary total", "auxiliary"], ["Classifier (auxiliary)", "classifier"], ["Summarizer (auxiliary)", "summarizer"],
			["Orchestrator audit (auxiliary)", "orchestrator_audit"], ["Optional judge (auxiliary)", "optional_judge"], ["Overall", "overall"]];
		for (const [label, name] of groups) {
			const total = usage[name];
			const tokens = total.input_tokens === undefined ? "tokens unknown" : String(total.input_tokens) + " input / " + String(total.output_tokens) + " output tokens";
			const cost = total.normalized_cost === undefined ? "cost unknown" : "normalized cost " + String(total.normalized_cost);
			detail(usageDetails, label, String(total.records) + " records · " + tokens + " · " + cost);
		}
		detail(usageDetails, "Calculated", formatTime(usage.calculated_at));
		finishDetails(usageDetails);usageState.hidden = true;
	}

	function loadModels() {
		loading(modelsState,modelList,"Loading models…");
		return requestJSON("/api/v1/models").then(body => {
			if (!body || body.version !== 1 || !["available", "unavailable"].includes(body.availability) || !Array.isArray(body.models) || body.models.length > maxInspectedModels ||
				(body.availability === "available") !== printable(body.config_id, 128, false) || body.availability === "available" &&
				(!Number.isFinite(Date.parse(body.refreshed_at)) || !integer(body.local_total_bytes) || body.local_total_kind !== "logical_deduplicated" || !integer(body.local_unknown_size_count))) throw new Error("invalid models");
			const seen = new Set(), rows=[];
			for (const item of body.models) {
				if (!item || !printable(item.id, 128, false) || seen.has(item.id) || !printable(item.provider, 128, false) || !printable(item.model, 512, false) ||
					!["local", "cloud"].includes(item.locality) || ["configured", "enabled", "installed", "usable"].some(key => typeof item[key] !== "boolean") || !Array.isArray(item.capabilities) || item.capabilities.length > 128 ||
					item.capabilities.some(value => !printable(value, 128, false)) || !["healthy", "degraded", "unavailable", "disabled", "unknown"].includes(item.health) ||
					!optionalInteger(item.context_tokens) || !optionalNumber(item.estimated_cost) || !optionalInteger(item.ram_bytes) || !optionalInteger(item.vram_bytes) ||
					!optionalInteger(item.size_bytes) || !printable(item.failure_domain || "", 128, true) || !printable(item.status_code || "", 128, true)) throw new Error("invalid model");
				seen.add(item.id);
				const lines = [item.provider + " / " + item.model, stateLabel(item.locality) + " · health " + stateLabel(item.health),
					"Capabilities: " + (item.capabilities.length ? item.capabilities.join(", ") : "None declared"),
					"Context: " + (item.context_tokens === undefined ? "Unknown" : String(item.context_tokens)),
					"Estimated cost: " + (item.estimated_cost === undefined ? "Unknown" : String(item.estimated_cost)),
					"RAM / VRAM: " + formatBytes(item.ram_bytes) + " / " + formatBytes(item.vram_bytes),
					"Installed size: " + formatBytes(item.size_bytes),
					"Failure domain: " + (item.failure_domain || "Unknown")];
				rows.push([item.id,lines.join("\n")]);
			}
			reconcileItems(modelList,rows);
			setText(modelCount,String(body.models.length));
			if (body.availability === "unavailable") showNotice(modelsState, "Models unavailable.", false);
			else if (!body.models.length) showNotice(modelsState, "No configured models.", false);
			else modelsState.hidden = true;
		}).catch(() => showNotice(modelsState, "Models unavailable.", true));
	}

	function loadHealth() {
		loading(healthState,healthDetails,"Loading health…");
		return requestJSON("/api/v1/health").then(body => {
			if (!body || body.version !== 1 || !["available", "unavailable"].includes(body.availability) || !Array.isArray(body.checks) || body.checks.length > maxHealthChecks) throw new Error("invalid health");
			if (body.availability === "unavailable") {
				if (body.status !== "unavailable" || body.ready !== undefined || body.checked_at !== undefined || body.checks.length) throw new Error("invalid unavailable health");
				showNotice(healthState, "Health unavailable.", false);
				return;
			}
			if (typeof body.ready !== "boolean" || !Number.isFinite(Date.parse(body.checked_at)) || !printable(body.status || "", 128, true)) throw new Error("invalid health");
			for(const check of body.checks)if(!check || !printable(check.component,128,false) || !printable(check.id||"",128,true) || !printable(check.status,64,false) || !printable(check.code,128,false))throw Error("invalid health check");
   beginDetails(healthDetails);
   detail(healthDetails, "Status", body.status || "Unknown");
			detail(healthDetails, "Ready", body.ready ? "Yes" : "No");
			detail(healthDetails, "Checked", formatTime(body.checked_at));
			for (const check of body.checks) {
				if (!check || !printable(check.component, 128, false) || !printable(check.id || "", 128, true) || !printable(check.status, 64, false) || !printable(check.code, 128, false)) throw new Error("invalid health check");
				detail(healthDetails, check.component + (check.id ? " · " + check.id : ""), stateLabel(check.status) + " · " + stateLabel(check.code));
			}
			finishDetails(healthDetails);healthState.hidden = true;
		}).catch(() => showNotice(healthState, "Health unavailable.", true));
	}

	function loadResources() {
		loading(resourcesState,resourcesDetails,"Loading resources…");
		return requestJSON("/api/v1/resources").then(body => {
			if (!body || body.version !== 1 || !["available", "unavailable"].includes(body.availability)) throw new Error("invalid resources");
			const fields = ["cpus", "total_ram_bytes", "available_ram_bytes", "swap_used_bytes", "vram_total_bytes", "vram_available_bytes"];
			if (body.availability === "unavailable") {
				if (body.observed_at !== undefined || fields.some(name => body[name] !== undefined) || body.unified_memory !== undefined || body.thermal_pressure !== undefined) throw new Error("invalid unavailable resources");
				showNotice(resourcesState, "Resources unavailable.", false);
				return;
			}
			if (!Number.isFinite(Date.parse(body.observed_at)) || fields.some(name => !optionalInteger(body[name])) ||
				(body.total_ram_bytes === undefined) !== (body.available_ram_bytes === undefined) || (body.vram_total_bytes === undefined) !== (body.vram_available_bytes === undefined) ||
				(body.total_ram_bytes !== undefined && body.available_ram_bytes > body.total_ram_bytes) || (body.vram_total_bytes !== undefined && body.vram_available_bytes > body.vram_total_bytes) ||
				(body.cpus !== undefined && body.cpus < 1) || (body.unified_memory !== undefined && typeof body.unified_memory !== "boolean") ||
				(body.thermal_pressure !== undefined && typeof body.thermal_pressure !== "boolean")) throw new Error("invalid resources");
			beginDetails(resourcesDetails);
			detail(resourcesDetails, "Observed", formatTime(body.observed_at));
			detail(resourcesDetails, "CPUs", body.cpus === undefined ? "Unknown" : String(body.cpus));
			detail(resourcesDetails, "RAM total / available", formatBytes(body.total_ram_bytes) + " / " + formatBytes(body.available_ram_bytes));
			detail(resourcesDetails, "Swap used", formatBytes(body.swap_used_bytes));
			detail(resourcesDetails, "VRAM total / available", formatBytes(body.vram_total_bytes) + " / " + formatBytes(body.vram_available_bytes));
			detail(resourcesDetails, "Unified memory", body.unified_memory === undefined ? "Unknown" : body.unified_memory ? "Yes" : "No");
			detail(resourcesDetails, "Thermal pressure", body.thermal_pressure === undefined ? "Unknown" : body.thermal_pressure ? "Present" : "Not observed");
			finishDetails(resourcesDetails);resourcesState.hidden = true;
		}).catch(() => showNotice(resourcesState, "Resources unavailable.", true));
	}

	function validCandidate(item) {
		if (!item || !printable(item.model, 512, false) || !printable(item.provider, 128, false) || !printable(item.failure_domain || "", 128, true) ||
			!["selected", "fallback", "eligible", "excluded"].includes(item.disposition) || !Array.isArray(item.constraint_codes) || item.constraint_codes.length > 16 ||
			item.constraint_codes.some(code => !printable(code, 64, false))) return false;
		const scored = item.disposition !== "excluded";
		return scored ? finiteNumber(item.score) && knownNumber(item.confidence) && item.confidence <= 1 && integer(item.samples) && item.constraint_codes.length === 0 :
			item.score === undefined && item.confidence === undefined && item.samples === undefined && item.constraint_codes.length > 0;
	}

	function renderRoute(body) {
		if (!body || body.version !== 1 || body.task_id !== selectedTaskID || !["available", "unavailable"].includes(body.availability) ||
			!Array.isArray(body.candidates) || body.candidates.length > maxRouteCandidates) throw new Error("invalid route");
		if (body.availability === "unavailable") {
			if (body.route_id !== undefined || body.domain !== undefined || body.profile !== undefined || body.explored || body.candidates.length || body.usage !== undefined) throw new Error("invalid unavailable route");
			showNotice(routeState, "Route unavailable.", false);
			return;
		}
		if (!printable(body.route_id, 128, false) || !printable(body.domain, 128, false) || !printable(body.profile, 128, false) || typeof body.explored !== "boolean" ||
			!body.candidates.length || body.candidates.some(item => !validCandidate(item)) || !validUsage(body.usage) || body.candidates.filter(item => item.disposition === "selected").length !== 1) throw new Error("invalid route");
		beginDetails(routeDetails);
		const rows=[];
		detail(routeDetails, "Route", body.route_id);
		detail(routeDetails, "Domain / profile", body.domain + " / " + body.profile);
		detail(routeDetails, "Exploration", body.explored ? "Explored" : "Not explored");
		for (const item of body.candidates) {
			const evidence = item.disposition === "excluded" ? "Constraints: " + item.constraint_codes.join(", ") :
				"Score " + String(item.score) + " · confidence " + String(item.confidence) + " · samples " + String(item.samples);
			rows.push([JSON.stringify([item.provider,item.model]), item.provider + " / " + item.model + "\n" + stateLabel(item.disposition) + " · " + evidence + "\nFailure domain: " + (item.failure_domain || "Unknown")]);
		}
		finishDetails(routeDetails);reconcileItems(routeCandidates,rows);routeState.hidden = true;
	}

	function loadRoute(taskID, requestID) {
		loading(routeState,routeDetails,"Loading route…");
		return requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/route").then(body => {
			if (requestID !== inspectorRequest || selectedTaskID !== taskID) return;
			renderRoute(body);
		}).catch(() => {
			if (requestID === inspectorRequest && selectedTaskID === taskID) showNotice(routeState, "Route unavailable.", true);
		});
	}

	function loadUsage(taskID, requestID) {
		loading(usageState,usageDetails,"Loading usage…");
		return requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/usage").then(body => {
			if (requestID !== inspectorRequest || selectedTaskID !== taskID) return;
			if (!body || body.version !== 1 || body.task_id !== taskID || !["available", "unavailable"].includes(body.availability) ||
				(body.availability === "available") !== Boolean(body.usage)) throw new Error("invalid usage");
			if (body.availability === "unavailable") showNotice(usageState, "Usage unavailable.", false);
			else if (!validUsage(body.usage)) throw new Error("invalid usage");
			else renderUsage(body.usage);
		}).catch(() => {
			if (requestID === inspectorRequest && selectedTaskID === taskID) showNotice(usageState, "Usage unavailable.", true);
		});
	}

	function validTool(item) {
		if (!item || !presentationID.test(item.call_id) || !printable(item.name, 128, false) || !["read_only", "idempotent_write", "non_idempotent_write", "unknown"].includes(item.behavior) ||
			!["not_required", "pending", "approved", "denied", "revoked", "unknown"].includes(item.permission) || !["pending", "completed", "failed"].includes(item.state) ||
			!["none", "confirmed", "uncertain"].includes(item.effect) || !printable(item.code || "", 128, true) || !Number.isFinite(Date.parse(item.started_at)) ||
			!Number.isSafeInteger(item.start_sequence) || item.start_sequence < 1) return false;
		const finished = item.completed_at !== undefined || item.completion_sequence !== undefined;
		if ((item.completed_at !== undefined) !== (item.completion_sequence !== undefined)) return false;
		if (item.state === "pending") return !finished && item.effect === "uncertain" && !item.code;
		return finished && Number.isFinite(Date.parse(item.completed_at)) && Date.parse(item.completed_at) >= Date.parse(item.started_at) &&
			Number.isSafeInteger(item.completion_sequence) && item.completion_sequence > item.start_sequence && (item.state === "failed") === Boolean(item.code);
	}

	function loadTools(taskID, after, reset, requestID) {
		if(reset)loading(toolsState,toolList,"Loading tool lifecycle…");
		loadMoreTools.disabled = true;
		const limit = Math.min(inspectionPageLimit, maxInspectionItems - (reset ? 0 : toolTotal));
		const query = new URLSearchParams({limit: String(limit)});
		if (after) query.set("after", after);
		return requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/tools?" + query.toString()).then(body => {
			if (requestID !== inspectorRequest || selectedTaskID !== taskID) return;
			if (!body || body.version !== 1 || body.task_id !== taskID || !Array.isArray(body.tools) || body.tools.length > limit ||
				!printable(body.next_cursor || "", 512, true) || !reset && body.next_cursor && toolCursors.has(body.next_cursor)) throw new Error("invalid tools");
			const incoming=new Set();
   for(const item of body.tools){if(!validTool(item)||incoming.has(item.call_id)||!reset&&toolIDs.has(item.call_id))throw Error("invalid tool");incoming.add(item.call_id);}
   if(reset){toolIDs.clear();toolCursors.clear();toolTotal=0;toolPages=0;toolCursor="";}
   const rows=reset?[]:Array.from(toolList.children,node=>[node.dataset.key,node.textContent]);
   for (const item of body.tools) {
				toolIDs.add(item.call_id); toolTotal++;
				const completion = item.completed_at === undefined ? "Completion: pending / unknown" : "Completed: " + formatTime(item.completed_at);
				rows.push([item.call_id, item.name + "\n" + stateLabel(item.state) + " · " + stateLabel(item.behavior) + " · permission " + stateLabel(item.permission) + " · effect " + stateLabel(item.effect) + (item.code ? " · code " + item.code : "") + "\nStarted: " + formatTime(item.started_at) + " · " + completion]);
			}
			reconcileItems(toolList,rows);
			toolPages++;
			if (body.next_cursor) toolCursors.add(body.next_cursor);
			toolCursor = toolTotal < maxInspectionItems && toolPages < maxInspectionPages ? body.next_cursor || "" : "";
			setText(toolCount,String(toolTotal));
			loadMoreTools.hidden = !toolCursor;
			if (!toolTotal) showNotice(toolsState, "No tool activity recorded.", false);
			else if (body.next_cursor && !toolCursor) showNotice(toolsState, "Tool pagination limit reached.", false);
			else toolsState.hidden = true;
		}).catch(() => {
			if (requestID === inspectorRequest && selectedTaskID === taskID) showNotice(toolsState, "Tool lifecycle unavailable.", true);
		}).finally(() => { loadMoreTools.disabled = false; });
	}

	function validAuditFinding(item) {
		return item && printable(item.summary, maxInspectionText, false) && Array.isArray(item.evidence_refs) && item.evidence_refs.length <= 64 && item.evidence_refs.every(ref => printable(ref, 256, false));
	}

	function validAudit(item) {
		if (!item || !presentationID.test(item.id) || !["pending", "completed", "rejected", "abstained", "failed", "canceled"].includes(item.status) ||
			!printable(item.reviewer_id, 128, false) || !printable(item.evaluator_model, 512, false) || !printable(item.evaluator_provider, 128, false) ||
			!printable(item.rubric_version || "", 128, true) || !printable(item.domain || "", 128, true) || !Array.isArray(item.findings) || item.findings.length > 64 ||
			item.findings.some(finding => !validAuditFinding(finding)) || !Array.isArray(item.evidence_precedence) || item.evidence_precedence.length > 8 ||
			item.evidence_precedence.some(value => !printable(value, 64, false)) || !Number.isFinite(Date.parse(item.started_at)) || (item.usage !== undefined && !validUsageTotal(item.usage))) return false;
		const finished = item.finished_at !== undefined;
		if (item.status === "pending") return !finished && !item.rubric_version && !item.domain && !item.findings.length && item.usage === undefined;
		if (!finished || !Number.isFinite(Date.parse(item.finished_at)) || Date.parse(item.finished_at) < Date.parse(item.started_at)) return false;
		if (["failed", "canceled"].includes(item.status)) return !item.rubric_version && !item.domain && !item.findings.length && item.usage === undefined;
		return Boolean(item.rubric_version && item.domain);
	}

	function loadAudits(taskID, after, reset, requestID) {
		if(reset)loading(auditsState,auditList,"Loading audits…");
		loadMoreAudits.disabled = true;
		const limit = Math.min(inspectionPageLimit, maxInspectionItems - (reset ? 0 : auditTotal));
		const query = new URLSearchParams({limit: String(limit)});
		if (after) query.set("after", after);
		return requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/audits?" + query.toString()).then(body => {
			if (requestID !== inspectorRequest || selectedTaskID !== taskID) return;
			if (!body || body.version !== 1 || body.task_id !== taskID || !Array.isArray(body.audits) || body.audits.length > limit ||
				!printable(body.next_cursor || "", 512, true) || !reset && body.next_cursor && auditCursors.has(body.next_cursor)) throw new Error("invalid audits");
			const incoming=new Set();
   for(const item of body.audits){if(!validAudit(item)||incoming.has(item.id)||!reset&&auditIDs.has(item.id))throw Error("invalid audit");incoming.add(item.id);}
   if(reset){auditIDs.clear();auditCursors.clear();auditTotal=0;auditPages=0;auditCursor="";}
   const rows=reset?[]:Array.from(auditList.children,node=>[node.dataset.key,node.textContent]);
   for (const item of body.audits) {
				auditIDs.add(item.id); auditTotal++;
				const findings = item.findings.length ? item.findings.map(finding => finding.summary + " [evidence: " + (finding.evidence_refs.length ? finding.evidence_refs.join(", ") : "none") + "]").join("\n") : "No sanitized findings.";
				const usage = item.usage === undefined ? "Auxiliary usage: Unknown" : "Auxiliary usage: " + (item.usage.normalized_cost === undefined ? "cost unknown" : "normalized cost " + String(item.usage.normalized_cost));
				const precedence = "Evidence precedence: " + (item.evidence_precedence.length ? item.evidence_precedence.join(" → ") : "None");
				rows.push([item.id, item.evaluator_provider + " / " + item.evaluator_model + "\n" + stateLabel(item.status) + " · reviewer " + item.reviewer_id + " · domain " + (item.domain || "Unknown") + "\nRubric version: " + (item.rubric_version || "Unknown") + "\n" + precedence + "\n" + usage + "\n" + findings]);
			}
			reconcileItems(auditList,rows);
			auditPages++;
			if (body.next_cursor) auditCursors.add(body.next_cursor);
			auditCursor = auditTotal < maxInspectionItems && auditPages < maxInspectionPages ? body.next_cursor || "" : "";
			setText(auditCount,String(auditTotal));
			loadMoreAudits.hidden = !auditCursor;
			if (!auditTotal) showNotice(auditsState, "No audits recorded.", false);
			else if (body.next_cursor && !auditCursor) showNotice(auditsState, "Audit pagination limit reached.", false);
			else auditsState.hidden = true;
		}).catch(() => {
			if (requestID === inspectorRequest && selectedTaskID === taskID) showNotice(auditsState, "Audits unavailable.", true);
		}).finally(() => { loadMoreAudits.disabled = false; });
	}

	function loadTask(taskID) {
		if(selectedTaskID!==taskID){for(const list of [routeDetails,routeCandidates,usageDetails,toolList,auditList])list.replaceChildren();}
		selectedTaskID = taskID;
		taskInspector.hidden = false;
		const requestID = ++inspectorRequest;
		loadRoute(taskID, requestID);
		loadUsage(taskID, requestID);
		loadTools(taskID, "", true, requestID);
		loadAudits(taskID, "", true, requestID);
	}

	function clearTask() {
		selectedTaskID = "";
		inspectorRequest++;
		taskInspector.hidden = true;
	}

	let globalsPending=null;
	function loadGlobals() { if(!globalsPending)globalsPending=Promise.all([loadModels(),loadHealth(),loadResources()]).finally(()=>{globalsPending=null;});return globalsPending; }

	loadMoreTools.addEventListener("click", () => { if (selectedTaskID && toolCursor) loadTools(selectedTaskID, toolCursor, false, inspectorRequest); });
	loadMoreAudits.addEventListener("click", () => { if (selectedTaskID && auditCursor) loadAudits(selectedTaskID, auditCursor, false, inspectorRequest); });
	refreshInspector.addEventListener("click", () => {
		loadGlobals();
		if (selectedTaskID) loadTask(selectedTaskID);
	});

	window.NexusInspector = Object.freeze({
		clearTask,
		loadGlobals,
		loadTask,
		modelChanged: loadModels,
		toolChanged(taskID) { if (taskID === selectedTaskID) loadTools(taskID, "", true, inspectorRequest); },
		routeChanged(taskID) {
			if (taskID !== selectedTaskID) return;
			loadRoute(taskID, inspectorRequest);
			loadUsage(taskID, inspectorRequest);
		}
	});
})();
