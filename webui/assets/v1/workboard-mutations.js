"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	if (!window.DarwinRoutes || !window.DarwinRoutes.workboards(relative) || !window.DarwinWorkboards || !window.DarwinOperationContract) return;
	const client = window.DarwinWorkboardClient;
	if (!client) return;
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
	const definitive = new Set([400, 401, 403, 404, 409, 422]);
	const controls = document.querySelector("#workboard-mutation-controls"), status = document.querySelector("#workboard-mutation-status");
	const reconcile = document.querySelector("#reconcile-workboard-operations"), acknowledge = document.querySelector("#acknowledge-workboard-operation");
	const reviewDialog = document.querySelector("#candidate-review-dialog"), reviewContent = document.querySelector("#candidate-review-content"), reviewTitle = document.querySelector("#candidate-review-title");
	const reviewRationale = document.querySelector("#candidate-review-rationale"), reviewConfirm = document.querySelector("#candidate-review-confirm"), reviewAccept = document.querySelector("#candidate-review-accept"), reviewReject = document.querySelector("#candidate-review-reject"), reviewClose = document.querySelector("#candidate-review-close"), reviewStatus = document.querySelector("#candidate-review-status");
	const shell = document.querySelector(".shell"), skipLink = document.querySelector(".skip-link"), stableReviewFocus = document.querySelector("#refresh-workboards");
	const openButtons = {
		"board.create": document.querySelector("#open-board-create"), "board.revise": document.querySelector("#open-board-revise"),
		"board.archive": document.querySelector("#open-board-archive"), "card.create": document.querySelector("#open-card-create"), "card.revise": document.querySelector("#open-card-revise"),
		"dependency.change": document.querySelector("#open-dependency-change")
	};
	const dialogs = {
		"board.create": document.querySelector("#board-create-dialog"), "board.revise": document.querySelector("#board-revise-dialog"),
		"board.archive": document.querySelector("#board-archive-dialog"), "card.create": document.querySelector("#card-create-dialog"), "card.revise": document.querySelector("#card-revise-dialog"),
		"dependency.change": document.querySelector("#dependency-change-dialog")
	};
	const closeButtons = Array.from(document.querySelectorAll(".close-workboard-dialog"));
	const forms = Object.fromEntries(Object.entries(dialogs).map(([action, dialog]) => [action, dialog.querySelector("[role=form]")]));
	const submitButtons = Object.fromEntries(Object.entries(forms).map(([action, form]) => [action, form.querySelector("button[id^=submit-]")]));
	let csrfToken = "", operationsReady = false, operationReadFailed = false, inFlight = false, pendingIntent = null, unresolved = [], scanGeneration = 0;
	let activeAction = "", activeCapture = null, activeOpener = null, criterionSequence = 0, activeReview = null;
	function textBytes(value) { try { return new TextEncoder().encode(value).length; } catch (_) { return Number.MAX_SAFE_INTEGER; } }
	function wellFormed(value) { for (let index = 0; index < value.length; index++) { const code = value.charCodeAt(index); if (code >= 0xd800 && code <= 0xdbff) { if (++index >= value.length || value.charCodeAt(index) < 0xdc00 || value.charCodeAt(index) > 0xdfff) return false; } else if (code >= 0xdc00 && code <= 0xdfff) return false; } return true; }
	function boundedText(value, max, empty) { return typeof value === "string" && wellFormed(value) && textBytes(value) <= max && (empty || Boolean(value.trim())) && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(value); }
	function validID(value) { return typeof value === "string" && idPattern.test(value); }
	function validTime(value) { const parsed = typeof value === "string" ? Date.parse(value) : NaN; return Number.isFinite(parsed) && parsed >= Date.UTC(1970, 0, 1) && parsed < Date.UTC(2261, 0, 1); }
	function exactKeys(value, required, optional) {
		if (!value || typeof value !== "object" || Array.isArray(value)) return false;
		const keys = Object.keys(value), allowed = new Set([...required, ...optional]);
		return required.every(key => Object.hasOwn(value, key)) && keys.every(key => allowed.has(key));
	}
	function key() { const bytes = new Uint8Array(18); window.crypto.getRandomValues(bytes); return "web-" + Array.from(bytes, value => value.toString(16).padStart(2, "0")).join(""); }
	function field(form, name) { return form.querySelector(`[name="${name}"]`); }
	function formStatus(form, message, error) { const output = form.querySelector(".mutation-form-status"); output.textContent = message; output.classList.toggle("error", Boolean(error)); }
	function barrier() { return !csrfToken || !operationsReady || operationReadFailed || inFlight || Boolean(pendingIntent) || unresolved.length > 0; }
	function updateControls() {
		const context = window.DarwinWorkboards.context(), blocked = barrier(), activeBoard = context.board && context.board.state === "active";
		controls.hidden = false;
		openButtons["board.create"].disabled = blocked;
		for (const action of ["board.revise", "card.create"]) openButtons[action].disabled = blocked || !activeBoard;
		openButtons["board.archive"].disabled = blocked || !activeBoard || context.board.active_claims !== 0;
		openButtons["card.revise"].disabled = blocked || !activeBoard || !context.card;
		openButtons["dependency.change"].disabled = blocked || !activeBoard || !context.card || !context.complete || !context.unfiltered;
		for (const button of document.querySelectorAll(".card-position")) {
			const direction = button.dataset.position, card = context.cards && context.cards.find(item => item.id === button.dataset.cardId);
			button.hidden = (direction === "ready" && (!card || card.state !== "backlog")) || (direction === "backlog" && (!card || card.state !== "ready"));
			button.disabled = blocked || !client.positionPlan(context, button.dataset.cardId, direction);
		}
		for (const button of document.querySelectorAll(".card-control")) {
			const card = context.cards && context.cards.find(item => item.id === button.dataset.cardId);
			button.hidden = !card || !["in_progress", "blocked"].includes(card.state);
			button.disabled = blocked || !client.controlPlan(context, button.dataset.cardId, button.dataset.control);
		}
		const rationale = reviewRationale.value;
		const reviewReady = activeReview && reviewConfirm.checked && boundedText(rationale, 65536, false) && !blocked;
		reviewAccept.disabled = !reviewReady || !activeReview.accept || !client.captureCurrent(activeReview.accept, context);
		reviewReject.disabled = !reviewReady || !activeReview.reject || !client.captureCurrent(activeReview.reject, context);
		reviewClose.disabled = inFlight;
		reconcile.hidden = !pendingIntent && !unresolved.length && !operationReadFailed;
		acknowledge.hidden = !client.acknowledgeAllowed(pendingIntent, operationsReady, operationReadFailed, unresolved.length);
		for (const button of closeButtons) button.disabled = inFlight;
		for (const [action, button] of Object.entries(submitButtons)) button.disabled = blocked || stale(action) || action === "board.archive" && !document.querySelector("#board-archive-confirm").checked;
	}
	function show(message, error) { status.textContent = message; status.classList.toggle("error", Boolean(error)); updateControls(); }
	function reviewNotice(message, error) { reviewStatus.textContent = message; reviewStatus.classList.toggle("error", Boolean(error)); }
	function validOperation(item) {
		if (!exactKeys(item, ["version", "operation_id", "action", "state", "created_at", "updated_at"], ["subject_type", "subject_id"]) || item.version !== 1 || !validID(item.operation_id) ||
			!window.DarwinOperationContract.validAction(item.action) || !["pending", "committed", "rejected"].includes(item.state) || !validTime(item.created_at) || !validTime(item.updated_at) || Date.parse(item.updated_at) < Date.parse(item.created_at)) return null;
		const subject = Object.hasOwn(item, "subject_type") || Object.hasOwn(item, "subject_id");
		if (subject && (!window.DarwinOperationContract.validSubject(item.subject_type) || !validID(item.subject_id)) || item.state === "committed" && !subject) return null;
		return Object.freeze({id: item.operation_id, action: item.action, state: item.state, subjectType: subject ? item.subject_type : "", subjectID: subject ? item.subject_id : ""});
	}
	function validPage(body, limit) {
		return exactKeys(body, ["version", "items", "next_cursor", "has_more"], []) && body.version === 1 && Array.isArray(body.items) && body.items.length <= limit && typeof body.has_more === "boolean" &&
			typeof body.next_cursor === "string" && textBytes(body.next_cursor) <= 512 && !/[\u0000-\u001f\u007f-\u009f]/u.test(body.next_cursor) && body.has_more === Boolean(body.next_cursor) && (!body.has_more || body.items.length > 0);
	}
	async function scanOperations(message) {
		const generation = ++scanGeneration; operationsReady = false; operationReadFailed = false; updateControls();
		const items = [], ids = new Set(), cursors = new Set(); let after = "";
		try {
			while (true) {
				const limit = Math.min(25, 100 - items.length), query = new URLSearchParams({limit: String(limit)}); if (after) query.set("after", after);
				const response = await fetch(base + "/api/v1/operations?" + query.toString(), {credentials: "same-origin", cache: "no-store", headers: {"Accept": "application/json"}});
				if (!response.ok) throw new Error("operation scan unavailable"); const body = await response.json();
				if (!validPage(body, limit)) throw new Error("invalid operation page");
				for (const raw of body.items) { const item = validOperation(raw); if (!item || ids.has(item.id)) throw new Error("invalid operation"); ids.add(item.id); items.push(item); }
				if (!body.has_more) break; if (items.length >= 100 || cursors.has(body.next_cursor)) throw new Error("operation scan bound"); cursors.add(body.next_cursor); after = body.next_cursor;
			}
			if (generation !== scanGeneration) return;
			unresolved = items.filter(item => item.state === "pending"); operationsReady = true;
			const exact = pendingIntent && pendingIntent.operationID ? items.find(item => item.id === pendingIntent.operationID) : null;
			const cardScoped = pendingIntent && ["card.move", "card.reorder", "dependency.add", "dependency.remove", "card.pause_request", "card.resume_request", "card.cancel_request", "acceptance.accept", "acceptance.reject"].includes(pendingIntent.body.action);
			if (exact && exact.action === pendingIntent.body.action && (!cardScoped || exact.subjectType === "card" && exact.subjectID === pendingIntent.body.card_id) && (exact.state === "committed" || exact.state === "rejected")) {
				const resolvedAction = pendingIntent.body.action, acceptance = resolvedAction === "acceptance.accept" || resolvedAction === "acceptance.reject"; pendingIntent = null; inFlight = false; if (exact.state === "committed" && acceptance) closeReview(true); else if (exact.state === "rejected" && acceptance) reviewNotice("The exact candidate decision was rejected. Close, refresh, and review the current evidence before deciding again.", true); window.DarwinWorkboards.refresh(); const others = unresolved.length ? " Other pending operations still block writes." : ""; show((exact.state === "committed" ? "The exact operation committed. Authoritative state was refreshed." : "The exact operation was rejected. Authoritative state was refreshed.") + others, exact.state === "rejected" || unresolved.length > 0); return;
			}
			if (pendingIntent && !pendingIntent.operationID) { if (!unresolved.length) pendingIntent = Object.freeze({...pendingIntent, reconciledClean: true}); show(unresolved.length ? "The outcome is unknown and pending operations remain. Actions stay blocked." : "No matching operation was published in a clean bounded scan. You may acknowledge the local unknown outcome; the request will not be replayed.", true); }
			else if (pendingIntent || unresolved.length) show(message || "An operation remains unresolved. Actions are blocked.", true);
			else show(message || "Workboard actions are ready.", false);
		} catch (_) { if (generation !== scanGeneration) return; operationsReady = true; operationReadFailed = true; show("Operation history could not be validated within 100 records. Actions remain blocked.", true); }
	}
	async function bootstrap() {
		try {
			const response = await fetch(base + "/api/v1/session/csrf", {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json", "Accept": "application/json"}, body: JSON.stringify({version: 1})});
			const body = await response.json(); if (!response.ok || !exactKeys(body, ["version", "csrf_token", "expires_at"], []) || body.version !== 1 || typeof body.csrf_token !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(body.csrf_token) || !validTime(body.expires_at) || Date.parse(body.expires_at) <= Date.now()) throw new Error("csrf unavailable");
			csrfToken = body.csrf_token; await scanOperations();
		} catch (_) { operationsReady = true; operationReadFailed = true; show("The workboard mutation session could not be established. Actions remain blocked.", true); }
	}
	function capture(action) {
		const context = window.DarwinWorkboards.context(), board = context.board, card = context.card;
		if (action === "board.create") return Object.freeze({action});
		if (!board || board.state !== "active") return null;
		if ((action === "card.revise" || action === "dependency.change") && !card) return null;
		if (action === "dependency.change" && (!context.complete || !context.unfiltered)) return null;
		if (action === "board.archive" && board.active_claims !== 0) return null;
		return Object.freeze({action, boardID: board.id, boardRevision: board.revision, activeClaims: board.active_claims, graphRevision: context.graphRevision, cardID: card ? card.id : "", cardRevision: card ? card.revision : 0, cardState: card ? card.state : "", dependencies: Object.freeze(card ? card.dependencies.slice() : [])});
	}
	function stale(action) {
		if (!activeCapture || activeCapture.action !== action || action === "board.create") return false;
		return !client.captureCurrent(activeCapture, window.DarwinWorkboards.context());
	}
	function writeHidden(form, name, value) { field(form, name).value = String(value); }
	function populate(action) {
		const context = window.DarwinWorkboards.context(), board = context.board, card = context.card, form = forms[action]; formStatus(form, "", false);
		if (action === "board.create") { field(form, "title").value = ""; field(form, "description").value = ""; return; }
		writeHidden(form, "board_id", board.id);
		if (action === "board.revise") { writeHidden(form, "expected_board_revision", board.revision); document.querySelector("#board-revise-revision").textContent = String(board.revision); field(form, "title").value = board.title; field(form, "description").value = board.description; }
		if (action === "board.archive") { writeHidden(form, "expected_board_revision", board.revision); document.querySelector("#board-archive-revision").textContent = String(board.revision); document.querySelector("#board-archive-confirm").checked = false; }
		if (action === "card.create") { writeHidden(form, "expected_board_revision", board.revision); writeHidden(form, "expected_graph_revision", context.graphRevision); document.querySelector("#card-create-board-revision").textContent = String(board.revision); document.querySelector("#card-create-graph-revision").textContent = String(context.graphRevision); resetCardCreate(form); }
		if (action === "card.revise") { writeHidden(form, "card_id", card.id); writeHidden(form, "expected_card_revision", card.revision); writeHidden(form, "expected_graph_revision", context.graphRevision); document.querySelector("#card-revise-revision").textContent = String(card.revision); document.querySelector("#card-revise-graph-revision").textContent = String(context.graphRevision); populateCardRevise(form, card); }
		if (action === "dependency.change") { field(form, "mode").value = "add"; field(form, "dependency_id").value = ""; document.querySelector("#dependency-card-revision").textContent = String(card.revision); document.querySelector("#dependency-graph-revision").textContent = String(context.graphRevision); populateDependencyOptions(); }
	}
	function populateDependencyOptions() {
		const form = forms["dependency.change"], context = window.DarwinWorkboards.context(), mode = field(form, "mode").value, options = field(form, "dependency_id"); options.replaceChildren();
		if (!activeCapture || !context.cards) return;
		const ids = mode === "remove" ? activeCapture.dependencies : activeCapture.dependencies.length >= 64 ? [] : context.cards.filter(card => card.id !== activeCapture.cardID && !activeCapture.dependencies.includes(card.id) && (activeCapture.cardState !== "ready" || card.state === "done")).map(card => card.id);
		const prompt = document.createElement("option"); prompt.value = ""; prompt.disabled = true; prompt.selected = true; prompt.textContent = mode === "remove" ? "Choose a prerequisite to remove" : "Choose a prerequisite to add"; options.append(prompt);
		for (const id of ids) { const option = document.createElement("option"); option.value = id; options.append(option); }
		if (!ids.length) prompt.textContent = mode === "remove" ? "No prerequisites to remove" : "No eligible cards";
	}
	function focusable(dialog) { return Array.from(dialog.querySelectorAll("button:not([disabled]), input:not([type=hidden]):not([disabled]), textarea:not([disabled]), select:not([disabled])")); }
	function showModal(dialog) { document.body.append(dialog); shell.inert = true; skipLink.inert = true; dialog.hidden = false; }
	function hideModal(dialog) { dialog.hidden = true; shell.inert = false; skipLink.inert = false; }
	function availableModalFocus(node) { if (!node || !node.isConnected || node.disabled || !document.body.contains(node)) return false; let current = node; for (let depth = 0; current && depth < 64; depth++, current = current.parentElement) { if (current.hidden || current.inert) return false; if (current === document.body) return true; } return false; }
	function restoreModalFocus(opener, fallback) { const target = availableModalFocus(opener) ? opener : availableModalFocus(fallback) ? fallback : null; if (target) target.focus(); }
	function open(action) { if (barrier() || activeAction || activeReview) return; const next = capture(action); if (!next) return; activeAction = action; activeCapture = next; activeOpener = openButtons[action]; populate(action); const dialog = dialogs[action]; showModal(dialog); const nodes = focusable(dialog); if (nodes.length) nodes[0].focus(); updateControls(); }
	function close() { if (!activeAction || inFlight) return; const dialog = dialogs[activeAction], opener = activeOpener; hideModal(dialog); activeAction = ""; activeCapture = null; activeOpener = null; restoreModalFocus(opener, stableReviewFocus); updateControls(); }
	function closeReview(stable) { if (inFlight || reviewDialog.hidden) return; const opener = activeReview && activeReview.opener; hideModal(reviewDialog); reviewDialog.removeAttribute("aria-busy"); reviewContent.replaceChildren(); reviewRationale.value = ""; reviewConfirm.checked = false; reviewNotice("", false); activeReview = null; restoreModalFocus(stable ? null : opener, stableReviewFocus); updateControls(); }
	function reviewLine(label, value, className) { const row = document.createElement("p"); if (className) row.className = className; const strong = document.createElement("strong"); strong.textContent = label + ": "; row.append(strong, document.createTextNode(value)); return row; }
	function openReview(detail) {
		if (barrier() || activeAction || activeReview || !detail || !detail.card || !detail.attempt || !validID(detail.card.id)) return;
		const context = window.DarwinWorkboards.context(), accept = client.acceptancePlan(context, detail.card.id, detail.attempt, "acceptance.accept"), reject = client.acceptancePlan(context, detail.card.id, detail.attempt, "acceptance.reject"), plan = accept || reject;
		if (!plan) { show("The candidate is not eligible for an operator decision from the current evidence. Refresh and inspect the attempt again.", true); return; }
		const attempt = detail.attempt, candidate = attempt.candidate, opener = Array.from(document.querySelectorAll(".card-review")).find(button => button.dataset.cardId === detail.card.id) || null; activeReview = Object.freeze({accept, reject, opener});
		reviewContent.replaceChildren(reviewLine("Candidate summary", candidate.summary));
		const artifacts = document.createElement("section"), artifactTitle = document.createElement("h3"), artifactList = document.createElement("ul"); artifactTitle.textContent = "Artifact references"; for (const reference of candidate.artifact_refs) { const item = document.createElement("li"); item.textContent = reference; artifactList.append(item); } if (!candidate.artifact_refs.length) { const item = document.createElement("li"); item.textContent = "No artifact references."; artifactList.append(item); } artifacts.append(artifactTitle, artifactList); reviewContent.append(artifacts);
		const criteria = document.createElement("section"), criteriaTitle = document.createElement("h3"), criteriaList = document.createElement("ul"); criteriaTitle.textContent = "Criteria and evidence";
		for (const criterion of attempt.criteria) { const item = document.createElement("li"), heading = document.createElement("strong"); heading.textContent = criterion.description; item.append(heading, document.createTextNode(" — " + (criterion.required ? "required" : "optional") + " " + criterion.kind + ", source " + criterion.required_source)); const matches = attempt.evidence.filter(record => record.criterion_id === criterion.id); const evidenceList = document.createElement("ul"); for (const record of matches) { const evidence = document.createElement("li"); evidence.textContent = record.source + " · " + record.outcome + " · " + record.actor_type + " " + record.actor_id + " · reference " + record.reference; if (record.source === "model_audit") evidence.className = "advisory-evidence"; evidenceList.append(evidence); } if (!matches.length) { const evidence = document.createElement("li"); evidence.textContent = "No recorded evidence."; evidenceList.append(evidence); } item.append(evidenceList); criteriaList.append(item); }
		criteria.append(criteriaTitle, criteriaList); reviewContent.append(criteria, reviewLine("Advisory", "Model-audit evidence informs review but never independently authorizes acceptance.", "advisory-evidence"));
		document.querySelector("#candidate-review-card-revision").textContent = String(plan.cardRevision); document.querySelector("#candidate-review-attempt").textContent = plan.attemptID; document.querySelector("#candidate-review-evidence-head").textContent = String(plan.evidenceHeadRevision);
		reviewRationale.value = ""; reviewConfirm.checked = false; reviewNotice(!accept ? "Acceptance is unavailable because required acceptance evidence is missing or failed." : !reject ? "Rejection is unavailable because no required evidence failed and no required subjective criterion can receive operator feedback." : "Choose a decision after reviewing the evidence.", false); showModal(reviewDialog); reviewTitle.focus(); updateControls();
	}
	function lines(value, max, bytes, ids) { const values = value.split(/\r?\n/u).map(item => item.trim()).filter(Boolean); if (values.length > max) return null; const seen = new Set(); for (const value of values) { const token = ids ? value : value.toLowerCase(); if (seen.has(token) || (ids ? !validID(value) : !boundedText(value, bytes, false))) return null; seen.add(token); } return values; }
	function integer(input, min, max) { const value = Number(input.value); return Number.isSafeInteger(value) && value >= min && value <= max ? value : null; }
	function budget(form, prefix, required) { const values = ["attempt_limit", "time_limit_ms", "token_limit", "cost_micros"].map(name => field(form, `budget.${name}`)); if (!required && values.every(input => input.value === "")) return undefined; const result = {attempt_limit: integer(values[0], 1, 32), time_limit_ms: integer(values[1], 0, 2592000000), token_limit: integer(values[2], 0, 1000000000), cost_micros: integer(values[3], 0, 1000000000000)}; return Object.values(result).some(value => value === null) ? null : result; }
	function criterionRow(value) {
		const index = ++criterionSequence;
		const row = document.createElement("div"); row.className = "criterion-row";
		const add = (label, name, control) => { const id = "dynamic-criterion-" + String(index) + "-" + name.replace(".", "-"); const caption = document.createElement("label"); caption.htmlFor = id; caption.textContent = label; control.id = id; control.name = name; row.append(caption, control); };
		const id = document.createElement("input"); id.required = true; id.maxLength = 128; id.value = value && value.id || ""; add("Criterion ID", "criteria.id", id);
		const kind = document.createElement("select"); for (const optionValue of ["objective", "subjective"]) { const option = document.createElement("option"); option.value = optionValue; option.textContent = optionValue === "objective" ? "Objective" : "Subjective"; kind.append(option); } kind.value = value && value.kind || "objective"; add("Kind", "criteria.kind", kind);
		const source = document.createElement("select"); for (const optionValue of ["deterministic", "user_feedback"]) { const option = document.createElement("option"); option.value = optionValue; option.textContent = optionValue === "deterministic" ? "Deterministic" : "User feedback"; source.append(option); } source.value = value && value.required_source || "deterministic"; add("Required source", "criteria.required_source", source);
		const validator = document.createElement("input"); validator.required = true; validator.maxLength = 128; validator.value = value && value.validator_id || ""; add("Validator ID", "criteria.validator_id", validator);
		const description = document.createElement("textarea"); description.required = true; description.maxLength = 4096; description.rows = 3; description.value = value && value.description || ""; add("Description", "criteria.description", description);
		const requiredLabel = document.createElement("label"), required = document.createElement("input"); required.type = "checkbox"; required.name = "criteria.required"; required.checked = Boolean(value && value.required); requiredLabel.className = "confirmation"; requiredLabel.append(required, document.createTextNode(" Required")); row.append(requiredLabel);
		const remove = document.createElement("button"); remove.type = "button"; remove.className = "secondary"; remove.textContent = "Remove criterion"; remove.addEventListener("click", () => { if (document.querySelectorAll("#card-create-criteria .criterion-row").length > 1) row.remove(); }); row.append(remove); return row;
	}
	function resetCriteria(values) { const container = document.querySelector("#card-create-criteria"), button = document.querySelector("#add-card-create-criterion"); for (const row of container.querySelectorAll(".criterion-row")) row.remove(); (values && values.length ? values : [null]).forEach(value => container.insertBefore(criterionRow(value), button)); }
	function readCriteria() { const rows = Array.from(document.querySelectorAll("#card-create-criteria .criterion-row")); if (!rows.length || rows.length > 32) return null; const ids = new Set(), result = []; for (const row of rows) { const get = name => row.querySelector(`[name="${name}"]`), id = get("criteria.id").value, kind = get("criteria.kind").value, source = get("criteria.required_source").value, validator = get("criteria.validator_id").value, description = get("criteria.description").value; if (!validID(id) || ids.has(id) || !validID(validator) || !boundedText(description, 4096, false) || kind === "objective" && source !== "deterministic" || kind === "subjective" && source !== "user_feedback" || !["objective", "subjective"].includes(kind)) return null; ids.add(id); result.push({version: 1, id, kind, required_source: source, validator_id: validator, description, required: get("criteria.required").checked}); } return textBytes(JSON.stringify(result)) <= 65536 ? result : null; }
	function resetCardCreate(form) { for (const name of ["title", "description", "parent_id", "assignee_id", "labels", "dependencies"]) field(form, name).value = ""; field(form, "priority").value = "normal"; field(form, "budget.attempt_limit").value = "1"; for (const name of ["time_limit_ms", "token_limit", "cost_micros"]) field(form, `budget.${name}`).value = "0"; resetCriteria(); }
	function populateCardRevise(form, card) { field(form, "title").value = card.title; field(form, "description").value = card.description; field(form, "priority").value = card.priority; field(form, "parent_id").value = card.parent_id || ""; field(form, "assignee_id").value = card.assignee_id || ""; field(form, "clear_parent").checked = false; field(form, "clear_assignee").checked = false; field(form, "labels").value = card.labels.join("\n"); for (const name of ["attempt_limit", "time_limit_ms", "token_limit", "cost_micros"]) field(form, `budget.${name}`).value = String(card.budget[name]); }
	function build(action) {
		const form = forms[action], context = window.DarwinWorkboards.context(), board = context.board, card = context.card, operationKey = key(); let body;
		if (action === "board.create") { const title = field(form, "title").value, description = field(form, "description").value; if (!boundedText(title, 256, false) || !boundedText(description, 65536, true)) return null; body = {version: 1, action, idempotency_key: operationKey, title, description}; }
		if (action === "board.revise") { body = {version: 1, action, idempotency_key: operationKey, board_id: activeCapture.boardID, expected_board_revision: activeCapture.boardRevision}; const title = field(form, "title").value, description = field(form, "description").value; if (title !== board.title) { if (!boundedText(title, 256, false)) return null; body.title = title; } if (description !== board.description) { if (!boundedText(description, 65536, true)) return null; body.description = description; } if (body.title === undefined && body.description === undefined) return null; }
		if (action === "board.archive") { if (!document.querySelector("#board-archive-confirm").checked || activeCapture.activeClaims !== 0 || !board || board.active_claims !== 0) return null; body = {version: 1, action, idempotency_key: operationKey, board_id: activeCapture.boardID, expected_board_revision: activeCapture.boardRevision}; }
		if (action === "card.create") { const title = field(form, "title").value, description = field(form, "description").value, priority = field(form, "priority").value, labels = lines(field(form, "labels").value, 32, 64, false), dependencies = lines(field(form, "dependencies").value, 64, 128, true), parent = field(form, "parent_id").value.trim(), assignee = field(form, "assignee_id").value.trim(), workBudget = budget(form, "", true), criteria = readCriteria(); if (!boundedText(title, 256, false) || !boundedText(description, 65536, true) || !["low", "normal", "high", "urgent"].includes(priority) || !labels || !dependencies || parent && !validID(parent) || assignee && !validID(assignee) || !workBudget || !criteria) return null; body = {version: 1, action, idempotency_key: operationKey, board_id: activeCapture.boardID, title, description, priority, labels, dependencies, criteria, budget: workBudget, expected_board_revision: activeCapture.boardRevision, expected_graph_revision: activeCapture.graphRevision}; if (parent) body.parent_id = parent; if (assignee) body.assignee_id = assignee; }
		if (action === "card.revise") { body = {version: 1, action, idempotency_key: operationKey, board_id: activeCapture.boardID, card_id: activeCapture.cardID, expected_card_revision: activeCapture.cardRevision}; const title = field(form, "title").value, description = field(form, "description").value, priority = field(form, "priority").value, labels = lines(field(form, "labels").value, 32, 64, false), parent = field(form, "parent_id").value.trim(), assignee = field(form, "assignee_id").value.trim(), clearParent = field(form, "clear_parent").checked, clearAssignee = field(form, "clear_assignee").checked, workBudget = budget(form, "", false); if (!labels || parent && !validID(parent) || assignee && !validID(assignee) || clearParent && parent || clearAssignee && assignee || workBudget === null) return null; if (title !== card.title) { if (!boundedText(title, 256, false)) return null; body.title = title; } if (description !== card.description) { if (!boundedText(description, 65536, true)) return null; body.description = description; } if (priority !== card.priority) { if (!["low", "normal", "high", "urgent"].includes(priority)) return null; body.priority = priority; } if (JSON.stringify(labels) !== JSON.stringify(card.labels)) body.labels = labels; if (clearParent) body.clear_parent = true; else if (parent !== (card.parent_id || "")) { if (!parent) return null; body.parent_id = parent; } if (body.parent_id !== undefined || body.clear_parent) body.expected_graph_revision = activeCapture.graphRevision; if (clearAssignee) body.clear_assignee = true; else if (assignee !== (card.assignee_id || "")) { if (!assignee) return null; body.assignee_id = assignee; } const originalBudget = card.budget; if (workBudget && JSON.stringify(workBudget) !== JSON.stringify(originalBudget)) body.budget = workBudget; if (Object.keys(body).length === 6) return null; }
		if (action === "dependency.change") { const plan = client.dependencyPlan(context, activeCapture, field(form, "mode").value, field(form, "dependency_id").value); if (!plan) return null; body = {version: 1, action: plan.action, idempotency_key: operationKey, board_id: activeCapture.boardID, card_id: activeCapture.cardID, dependency_id: plan.dependencyID, expected_card_revision: activeCapture.cardRevision, expected_graph_revision: activeCapture.graphRevision}; }
		const path = action === "board.create" ? "/api/v1/workboards" : "/api/v1/workboards/" + encodeURIComponent(body.board_id) + "/operations";
		return client.freezeIntent(path, body);
	}
	function buildPosition(plan) {
		if (!plan || !client.captureCurrent(plan, window.DarwinWorkboards.context())) return null;
		const body = {version: 1, action: plan.action, idempotency_key: key(), board_id: plan.boardID, card_id: plan.cardID, expected_board_revision: plan.boardRevision, expected_layout_revision: plan.layoutRevision, expected_card_revision: plan.cardRevision};
		if (plan.action === "card.move") body.target_state = plan.targetState;
		else if (plan.beforeCardID) body.before_card_id = plan.beforeCardID;
		else if (plan.afterCardID) body.after_card_id = plan.afterCardID;
		else return null;
		return client.freezeIntent("/api/v1/workboards/" + encodeURIComponent(body.board_id) + "/operations", body);
	}
	function buildControl(plan) {
		if (!plan || !client.captureCurrent(plan, window.DarwinWorkboards.context())) return null;
		return client.freezeIntent("/api/v1/workboards/" + encodeURIComponent(plan.boardID) + "/operations", {version: 1, action: plan.action, idempotency_key: key(), board_id: plan.boardID, card_id: plan.cardID, expected_card_revision: plan.cardRevision});
	}
	function buildAcceptance(plan) {
		const evidence = reviewRationale.value; if (!plan || !reviewConfirm.checked || !boundedText(evidence, 65536, false) || !client.captureCurrent(plan, window.DarwinWorkboards.context())) return null;
		return client.freezeIntent("/api/v1/workboards/" + encodeURIComponent(plan.boardID) + "/operations", {version: 1, action: plan.action, idempotency_key: key(), board_id: plan.boardID, card_id: plan.cardID, attempt_id: plan.attemptID, candidate_id: plan.candidateID, criteria_revision: plan.criteriaRevision, expected_card_revision: plan.cardRevision, evidence_head_revision: plan.evidenceHeadRevision, evidence, candidate_digest: plan.candidateDigest, criteria_digest: plan.criteriaDigest, evidence_set_digest: plan.evidenceSetDigest, policy_digest: plan.policyDigest});
	}
	async function mutate(action, supplied, capture) {
		if (barrier() || capture && !client.captureCurrent(capture, window.DarwinWorkboards.context()) || !capture && stale(action)) return;
		const built = supplied || build(action), direct = Boolean(supplied); if (!built) { if (forms[action]) formStatus(forms[action], "Review the bounded fields and provide a meaningful change.", true); else if (action === "acceptance.accept" || action === "acceptance.reject") reviewNotice("The candidate decision is stale or incomplete. Refresh and review it again.", true); else show("The card action is no longer available. Refresh and choose it again.", true); return; }
		const intent = Object.freeze({path: built.path, domainKey: built.body.idempotency_key, capture: capture || activeCapture, body: built.body, encoded: built.encoded, operationID: "", reconciledClean: false}), positioned = action === "card.move" || action === "card.reorder"; pendingIntent = intent; inFlight = true;
		if (positioned && !window.DarwinWorkboards.previewPosition(intent.capture)) { pendingIntent = null; inFlight = false; show("The card position preview could not be validated. Refresh and try again.", true); return; }
		if (direct) show(positioned ? "Pending position preview — not saved. Submitting one exact card position request…" : action === "acceptance.accept" || action === "acceptance.reject" ? "Submitting one exact candidate decision…" : "Submitting one exact card control request…", false); else formStatus(forms[action], "Submitting one exact request…", false); updateControls();
		try {
			const response = await fetch(base + built.path, {method: "POST", credentials: "same-origin", cache: "no-store", headers: {"Content-Type": "application/json", "Accept": "application/json", "X-Darwin-CSRF": csrfToken}, body: intent.encoded});
			let body = null; try { body = await response.json(); } catch (_) {}
			const resolution = client.mutationResolution(response.status, response.ok && client.receiptMatches(body, intent));
			if (resolution === "committed") { pendingIntent = null; inFlight = false; if (!direct) close(); if (action === "acceptance.accept" || action === "acceptance.reject") closeReview(true); window.DarwinWorkboards.refresh(); const message = action === "card.pause_request" ? "Pause request committed. The worker has not necessarily paused yet; authoritative state is refreshing." : action === "card.resume_request" ? "Resume request committed. The worker remains paused until it acknowledges the exact request; authoritative state is refreshing." : action === "card.cancel_request" ? "Cancellation request committed. Cancellation is not final until verified stop finalization; authoritative state is refreshing." : action === "acceptance.accept" ? "Acceptance committed. The card is Done; dependent cards may have been unlocked. Authoritative state is refreshing." : action === "acceptance.reject" ? "Rejection committed. The candidate remains in history and the card returned to Ready for another attempt. Authoritative state is refreshing." : "Committed receipt validated. Authoritative workboard state is refreshing."; show(message, false); return; }
			const acceptance = action === "acceptance.accept" || action === "acceptance.reject", error = client.mutationError(body, response.status); if (definitive.has(response.status) && error && error.definitive) { pendingIntent = null; inFlight = false; if (!direct) formStatus(forms[action], "The request was definitively rejected. Refresh authoritative state before editing again.", true); if (acceptance) reviewNotice("The decision was definitively rejected. Close, refresh, and review the current evidence before deciding again.", true); window.DarwinWorkboards.refresh(); if (response.status === 401 || response.status === 403) { csrfToken = ""; operationReadFailed = true; show("The authenticated mutation session was rejected. Reload the page before attempting another action.", true); return; } if ([404, 409, 422].includes(response.status)) { await scanOperations("The request was rejected and authoritative state was refreshed."); return; } show("The request was not committed (HTTP " + String(response.status) + ").", true); return; }
			pendingIntent = Object.freeze({...intent, operationID: error && error.operationID || ""}); inFlight = false; if (positioned) window.DarwinWorkboards.refresh(); const unknown = pendingIntent.operationID ? "Outcome unknown. The provisional position was removed; reconcile only operation " + pendingIntent.operationID + ". The request will not be replayed." : "Outcome unknown. The provisional position was removed and authoritative state is refreshing. The request will not be replayed."; if (acceptance) reviewNotice(unknown, true); show(unknown, true);
		} catch (_) { if (!pendingIntent || pendingIntent.domainKey !== intent.domainKey) return; inFlight = false; if (positioned) window.DarwinWorkboards.refresh(); const unknown = positioned ? "Outcome unknown after a network or malformed response. The provisional position was removed and authoritative state is refreshing; the exact request will not be replayed." : "Outcome unknown after a network or malformed response. The exact request is retained and will not be replayed."; if (action === "acceptance.accept" || action === "acceptance.reject") reviewNotice(unknown, true); show(unknown, true); }
	}
	for (const [action, button] of Object.entries(openButtons)) button.addEventListener("click", () => open(action));
	for (const button of closeButtons) button.addEventListener("click", close);
	for (const [action, button] of Object.entries(submitButtons)) button.addEventListener("click", () => mutate(action));
	window.addEventListener("darwin:card-position", event => { const detail = event.detail || {}, context = window.DarwinWorkboards.context(), plan = client.positionPlan(context, detail.cardID, detail.direction); if (!plan || barrier()) return; const built = buildPosition(plan); if (built) mutate(plan.action, built, plan); });
	window.addEventListener("darwin:card-control", event => { const detail = event.detail || {}, context = window.DarwinWorkboards.context(), plan = client.controlPlan(context, detail.cardID, detail.action); if (!plan || barrier()) return; const warning = plan.action === "card.cancel_request" ? "Request cancellation for this card? Committed tool effects will not be undone, and cancellation is not final until verified stop finalization." : plan.action === "card.resume_request" ? "Resume this paused card? The worker will continue only after acknowledging this revision-fenced request." : "Request a pause for this card at the worker's next safe boundary?"; if (!window.confirm(warning)) return; const built = buildControl(plan); if (built) mutate(plan.action, built, plan); });
	window.addEventListener("darwin:acceptance-review", event => openReview(event.detail));
	reviewAccept.addEventListener("click", () => { const plan = activeReview && activeReview.accept; if (!plan || !window.confirm("Accept this exact candidate based on the evidence shown? This moves the card to Done and may unlock dependent cards. Objective failures cannot be overridden. Your decision records positive feedback for each required subjective criterion.")) return; const built = buildAcceptance(plan); if (built) { reviewNotice("Submitting one exact candidate decision…", false); mutate(plan.action, built, plan); } });
	reviewReject.addEventListener("click", () => { const plan = activeReview && activeReview.reject; if (!plan || !window.confirm("Reject this exact candidate? This preserves the candidate in history, records negative feedback for each required subjective criterion, and returns the card to Ready for another attempt. It does not undo committed tool effects.")) return; const built = buildAcceptance(plan); if (built) { reviewNotice("Submitting one exact candidate decision…", false); mutate(plan.action, built, plan); } });
	reviewClose.addEventListener("click", closeReview); reviewRationale.addEventListener("input", updateControls); reviewConfirm.addEventListener("change", updateControls);
	document.querySelector("#board-archive-confirm").addEventListener("change", updateControls);
	document.querySelector("#dependency-change-mode").addEventListener("change", populateDependencyOptions);
	document.querySelector("#add-card-create-criterion").addEventListener("click", () => { const rows = document.querySelectorAll("#card-create-criteria .criterion-row"); if (rows.length < 32) document.querySelector("#card-create-criteria").insertBefore(criterionRow(null), document.querySelector("#add-card-create-criterion")); });
	for (const dialog of Object.values(dialogs)) dialog.addEventListener("keydown", event => { if (event.key === "Escape" && !inFlight) { event.preventDefault(); close(); return; } if (event.key !== "Tab") return; const nodes = focusable(dialog); if (!nodes.length) return; const first = nodes[0], last = nodes[nodes.length - 1]; if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); } else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); } });
	reviewDialog.addEventListener("keydown", event => { if (event.key === "Escape" && !inFlight) { event.preventDefault(); closeReview(); return; } if (event.key !== "Tab") return; const nodes = focusable(reviewDialog); if (!nodes.length) return; const first = nodes[0], last = nodes[nodes.length - 1]; if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); } else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); } });
	reconcile.addEventListener("click", () => scanOperations("Checking the exact operation status…"));
	acknowledge.addEventListener("click", async () => { if (!client.acknowledgeAllowed(pendingIntent, operationsReady, operationReadFailed, unresolved.length) || !window.confirm("Acknowledge this unresolved workboard outcome without retrying the exact request?")) return; pendingIntent = null; window.DarwinWorkboards.refresh(); await scanOperations("Unknown outcome acknowledged after a clean post-submit journal scan. No request was replayed."); });
	window.DarwinWorkboards.subscribe(() => { if (activeAction && stale(activeAction)) formStatus(forms[activeAction], "This editor is stale because the authoritative snapshot changed. Close it and reopen before submitting.", true); if (activeReview && (!activeReview.accept || !client.captureCurrent(activeReview.accept, window.DarwinWorkboards.context())) && (!activeReview.reject || !client.captureCurrent(activeReview.reject, window.DarwinWorkboards.context()))) reviewNotice("This candidate review is stale. Close it, refresh, and review the current evidence again.", true); updateControls(); });
	updateControls(); bootstrap();
})();
