"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.DarwinRoutes || !window.DarwinRoutes.settings(relative)) return;
	const view = document.querySelector("#settings-view"), chat = document.querySelector("#chat-view"), workboards = document.querySelector("#workboard-view"), models = document.querySelector("#models-view");
	const form = document.querySelector("#tool-settings-form"), tools = document.querySelector("#tools-enabled"), delegated = document.querySelector("#delegate-read-tools");
	const root = document.querySelector("#tools-read-root"), validation = document.querySelector("#settings-validation"), status = document.querySelector("#settings-status");
	const save = document.querySelector("#save-settings"), reset = document.querySelector("#reset-settings"), refresh = document.querySelector("#refresh-settings");
	const badge = document.querySelector("#settings-restart-badge"), activeSummary = document.querySelector("#active-settings");
	const connection = document.querySelector("#connection-state");
	const digestPattern = /^[0-9a-f]{64}$/;
	let csrf = "", projection = null, loading = false;
	chat.hidden = true; workboards.hidden = true; models.hidden = true; view.hidden = false;
	function validAccess(value) {
		return value && typeof value.tools_enabled === "boolean" && typeof value.delegate_read_tools === "boolean" && typeof value.read_root === "string" && value.read_root.length <= 4096 && !/[\u0000-\u001f\u007f]/.test(value.read_root) && (!value.delegate_read_tools || value.tools_enabled) && (!value.tools_enabled || /^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(value.read_root));
	}
	function validProjection(value) {
		return value && value.version === 1 && digestPattern.test(value.digest) && validAccess(value.active) && validAccess(value.saved) && typeof value.restart_required === "boolean" && value.restart_required === (JSON.stringify(value.active) !== JSON.stringify(value.saved));
	}
	function setStatus(message, failed) { status.textContent = message; status.classList.toggle("error", Boolean(failed)); }
	function setBusy(value) { loading = value; save.disabled = value || !csrf || !projection; reset.disabled = value || !projection; refresh.disabled = value; tools.disabled = value; root.disabled = value; syncDependency(); }
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
		tools.checked = value.saved.tools_enabled; delegated.checked = value.saved.delegate_read_tools; root.value = value.saved.read_root;
		badge.hidden = !value.restart_required; activeSummary.replaceChildren();
		addSummary("File tools", value.active.tools_enabled ? "Enabled" : "Disabled");
		addSummary("Delegated reads", value.active.delegate_read_tools ? "Enabled" : "Disabled");
		addSummary("Read root", value.active.read_root || "Not configured");
		syncDependency(); validation.hidden = true;
		setStatus(value.restart_required ? "Settings are saved. Restart DarwinRouter to activate them." : "Saved settings match the running daemon.", false);
	}
	function load() {
		setBusy(true); setStatus("Loading settings…", false);
		return fetch(base + "/api/v1/settings", {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}}).then(response => {
			if (!response.ok) throw new Error("settings unavailable");
			return response.json();
		}).then(value => { if (!validProjection(value)) throw new Error("invalid settings"); render(value); }).catch(() => {
			projection = null; setStatus("Settings could not be loaded.", true);
		}).finally(() => setBusy(false));
	}
	function formValue() { return {tools_enabled: tools.checked, delegate_read_tools: delegated.checked, read_root: root.value.trim()}; }
	function validate(value) {
		let message = "";
		if (value.tools_enabled && !value.read_root) message = "An absolute read root is required when file tools are enabled.";
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
		fetch(base + "/api/v1/settings", {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json", Accept: "application/json", "X-Darwin-CSRF": csrf}, body: JSON.stringify({version: 1, expected_digest: projection.digest, settings})}).then(response => {
			if (response.status === 409) throw new Error("conflict");
			if (!response.ok) throw new Error("save failed");
			return response.json();
		}).then(value => { if (!validProjection(value)) throw new Error("invalid settings"); render(value); }).catch(error => {
			if (error.message === "conflict") { setStatus("The configuration changed elsewhere. Reloading the latest values…", true); return load(); }
			else setStatus("Settings were not saved. Review the values and try again.", true);
		}).finally(() => setBusy(false));
	}
	tools.addEventListener("change", syncDependency);
	delegated.addEventListener("change", () => validate(formValue()));
	root.addEventListener("input", () => { if (!validation.hidden) validate(formValue()); });
	form.addEventListener("submit", saveSettings);
	reset.addEventListener("click", () => { if (projection) render(projection); });
	refresh.addEventListener("click", load);
	setBusy(true);
	fetch(base + "/api/v1/session/csrf", {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json"}, body: JSON.stringify({version: 1})}).then(response => {
		if (!response.ok) throw new Error("session unavailable");
		return response.json();
	}).then(value => { if (!value || value.version !== 1 || typeof value.csrf_token !== "string" || !value.csrf_token) throw new Error("invalid session"); csrf = value.csrf_token; connection.textContent = "Connected"; return load(); }).catch(() => { connection.textContent = "Session needs attention"; setBusy(false); setStatus("The browser session needs attention before settings can be changed.", true); });
})();
