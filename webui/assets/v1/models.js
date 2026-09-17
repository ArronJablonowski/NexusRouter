"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.DarwinRoutes || !window.DarwinRoutes.models(relative)) return;
	const view = document.querySelector("#models-view"), chat = document.querySelector("#chat-view"), workboards = document.querySelector("#workboard-view"), settings = document.querySelector("#settings-view");
	const refresh = document.querySelector("#refresh-models"), liveStatus = document.querySelector("#models-live-status");
	const total = document.querySelector("#local-model-total"), totalDetail = document.querySelector("#local-model-total-detail");
	const localList = document.querySelector("#local-model-list"), cloudList = document.querySelector("#cloud-model-list");
	const localState = document.querySelector("#local-model-state"), cloudState = document.querySelector("#cloud-model-state");
	const localCount = document.querySelector("#local-model-count"), cloudCount = document.querySelector("#cloud-model-count");
	const connection = document.querySelector("#connection-state");
	const digestPattern = /^[0-9a-f]{64}$/;
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
	const intervalMS = 10000;
	let timer = 0, loading = false, loaded = false, stopped = false;
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
	function validModel(item) {
		return item && idPattern.test(item.id) && text(item.provider, 128, false) && text(item.model, 512, false) && ["local", "cloud"].includes(item.locality) &&
			["configured", "enabled", "installed", "usable"].every(key => typeof item[key] === "boolean") && Array.isArray(item.capabilities) && item.capabilities.length <= 128 &&
			item.capabilities.every(value => text(value, 128, false)) && new Set(item.capabilities).size === item.capabilities.length &&
			["healthy", "degraded", "unavailable", "disabled", "unknown"].includes(item.health) && optionalInteger(item.context_tokens) &&
			(typeof item.estimated_cost === "undefined" || typeof item.estimated_cost === "number" && Number.isFinite(item.estimated_cost) && item.estimated_cost >= 0) &&
			optionalInteger(item.ram_bytes) && optionalInteger(item.vram_bytes) && optionalInteger(item.size_bytes) &&
			(item.digest === undefined || digestPattern.test(item.digest)) && ["family", "parameter_size", "quantization", "failure_domain", "status_code"].every(key => item[key] === undefined || text(item[key], 128)) && optionalTime(item.modified_at) &&
			(!item.usable || item.configured && item.enabled && item.health === "healthy") && (!item.enabled || item.configured) &&
			(item.locality !== "cloud" || !item.installed && item.size_bytes === undefined && item.digest === undefined && item.modified_at === undefined);
	}
	function validPage(value) {
		return value && value.version === 1 && value.availability === "available" && digestPattern.test(value.config_id) && Number.isFinite(Date.parse(value.refreshed_at)) &&
			Number.isSafeInteger(value.local_total_bytes) && value.local_total_bytes >= 0 && value.local_total_kind === "logical_deduplicated" &&
			Number.isSafeInteger(value.local_unknown_size_count) && value.local_unknown_size_count >= 0 && value.local_unknown_size_count <= 256 &&
			Array.isArray(value.models) && value.models.length <= 256 && value.models.every(validModel) && new Set(value.models.map(item => item.id)).size === value.models.length;
	}
	function element(name, className, value) { const node = document.createElement(name); if (className) node.className = className; if (value !== undefined) node.textContent = value; return node; }
	function detail(list, label, value) { list.append(element("dt", "", label), element("dd", "", value)); }
	function notice(node, message, failed) { node.textContent = message; node.classList.toggle("error", Boolean(failed)); node.hidden = false; }
	function badge(label, className) { return element("span", "model-badge" + (className ? " " + className : ""), label); }
	function card(item) {
		const node = element("li", "model-card"), heading = element("div", "model-card-heading"), identity = element("div"), badges = element("div", "model-badges");
		identity.append(element("h3", "", item.model), element("p", "model-card-provider", item.provider));
		badges.append(badge(item.usable ? "Usable" : item.health === "disabled" ? "Disabled" : "Unavailable", item.usable ? "usable" : "unavailable"));
		if (item.locality === "local") badges.append(badge(item.installed ? "Installed" : "Not installed"));
		if (item.configured) badges.append(badge("Configured"));
		heading.append(identity, badges); node.append(heading);
		const details = element("dl", "model-details");
		if (item.locality === "local") detail(details, "Size on disk", bytes(item.size_bytes));
		detail(details, "Health", item.health + (item.status_code ? " · " + item.status_code.replaceAll("_", " ") : ""));
		detail(details, "Capabilities", item.capabilities.length ? item.capabilities.join(", ") : "None declared");
		if (item.context_tokens !== undefined) detail(details, "Context", item.context_tokens.toLocaleString() + " tokens");
		if (item.parameter_size) detail(details, "Parameters", item.parameter_size);
		if (item.quantization) detail(details, "Quantization", item.quantization);
		if (item.family) detail(details, "Family", item.family);
		if (item.modified_at) detail(details, "Modified", time(item.modified_at));
		if (item.digest) detail(details, "Digest", item.digest.slice(0, 12) + "…");
		node.append(details); return node;
	}
	function render(page) {
		const locals = page.models.filter(item => item.locality === "local"), clouds = page.models.filter(item => item.locality === "cloud");
		localList.replaceChildren(...locals.map(card)); cloudList.replaceChildren(...clouds.map(card));
		localCount.textContent = String(locals.length); cloudCount.textContent = String(clouds.length);
		localState.hidden = locals.length > 0; cloudState.hidden = clouds.length > 0;
		if (!locals.length) notice(localState, "No local models were discovered or configured.", false);
		if (!clouds.length) notice(cloudState, "No cloud models are configured.", false);
		total.textContent = bytes(page.local_total_bytes);
		total.title = page.local_total_bytes.toLocaleString() + " bytes";
		const qualifier = page.local_unknown_size_count ? " · " + page.local_unknown_size_count + " model size" + (page.local_unknown_size_count === 1 ? " is" : "s are") + " unknown" : "";
		totalDetail.textContent = page.local_total_bytes.toLocaleString() + " bytes reported by local providers; duplicate model digests are counted once" + qualifier + ". Shared provider storage layers may use less physical space.";
		liveStatus.textContent = "Inventory refreshed " + time(page.refreshed_at) + ". Updates automatically every 10 seconds while this page is visible.";
		liveStatus.classList.remove("error"); connection.textContent = "Connected"; loaded = true;
	}
	async function load() {
		if (loading || stopped || document.hidden) return;
		loading = true; refresh.disabled = true;
		if (!loaded) liveStatus.textContent = "Loading model inventory…";
		try {
			const response = await fetch(base + "/api/v1/models", {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}});
			if (!response.ok) throw new Error("inventory unavailable");
			const page = await response.json();
			if (!validPage(page)) throw new Error("invalid inventory");
			render(page);
		} catch (_) {
			liveStatus.textContent = loaded ? "Inventory refresh failed. Showing the last verified snapshot; retrying automatically." : "Model inventory could not be loaded.";
			liveStatus.classList.add("error"); connection.textContent = "Inventory needs attention";
		} finally { loading = false; refresh.disabled = false; }
	}
	function schedule() { window.clearInterval(timer); timer = window.setInterval(load, intervalMS); }
	refresh.addEventListener("click", load);
	document.addEventListener("visibilitychange", () => { if (!document.hidden) load(); });
	window.addEventListener("beforeunload", () => { stopped = true; window.clearInterval(timer); });
	schedule(); load();
})();
