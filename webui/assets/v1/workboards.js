"use strict";
(() => {
	const base = document.body.dataset.basePath || "";
	const relative = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	window.DarwinRoutes = window.DarwinRoutes || Object.freeze({workboards: path => /^\/workboards(?:\/[^/]+)?$/.test(path), chats: path => /^\/chats(?:\/[^/]+)?$/.test(path)});
	const route = window.DarwinRoutes.workboards(relative) ? relative.match(/^\/workboards(?:\/([^/]+))?$/) : null;
	if (!route) return;
	const client = window.DarwinWorkboardClient;
	if (!client) return;
	const idPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
	const digestPattern = /^[0-9a-f]{64}$/;
	const states = ["backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"];
	const boardPageLimit = 25, cardPageLimit = 100, dependencyLimit = 100, attemptLimit = 25, maxBoards = 100, maxCards = 10000;
	const view = document.querySelector("#workboard-view"), chat = document.querySelector("#chat-view");
	const list = document.querySelector("#board-list"), listState = document.querySelector("#board-list-state"), boardCount = document.querySelector("#board-count");
	const loadMoreBoards = document.querySelector("#load-more-boards"), refresh = document.querySelector("#refresh-workboards"), boardStateFilter = document.querySelector("#board-state-filter");
	const selectedTitle = document.querySelector("#selected-board-title"), selectedMeta = document.querySelector("#selected-board-meta"), liveStatus = document.querySelector("#workboard-live-status");
	const stateNode = document.querySelector("#workboard-state"), kanban = document.querySelector("#kanban"), cardList = document.querySelector("#workboard-card-list"), loadMoreCards = document.querySelector("#load-more-cards");
	const filterForm = document.querySelector("#card-filters"), cardStateFilter = document.querySelector("#card-state-filter"), assigneeFilter = document.querySelector("#assignee-filter"), ownerFilter = document.querySelector("#owner-filter"), claimStateFilter = document.querySelector("#claim-state-filter"), filterStatus = document.querySelector("#workboard-filter-status");
	const resetFilters = document.querySelector("#reset-card-filters"), showKanban = document.querySelector("#show-kanban"), showList = document.querySelector("#show-list");
	let selectedID = "", boardCursor = "", cardCursor = "", boardTotal = 0, cardTotal = 0, boardRequestVersion = 0, cardRequestVersion = 0, snapshotFence = null;
	let boardSource = null, streamBoard = "", streamRevision = 0, streamFailures = 0, invalidationTimer = 0, snapshotGraphRevision = 0, snapshotGraphDigest = "";
	let loadedCards = [], visibleColumns = [], presentation = "kanban", appliedBoardState = "active", appliedFilters = Object.freeze({state: "", assignee: "", owner: "", claim: ""});
	let currentBoard = null, selectedCard = null, selectedCardAnchor = null, pendingPosition = null;
	let pendingFocusAnchor = null, pendingFocusVersion = 0;
	const contextObservers = new Set();
	const boardIDs = new Set(), boardCursors = new Set(), cardIDs = new Set(), cardCursors = new Set(), laneLists = new Map(), laneCounts = new Map(), laneRanks = new Map(), cardNodes = new Map(), lifecycleByCard = new Map(), supervisionByCard = new Map();
	chat.hidden = true;
	view.hidden = false;
	function element(name, className, text) {
		const node = document.createElement(name);
		if (className) node.className = className;
		if (text !== undefined) node.textContent = text;
		return node;
	}
	function notice(node, message, failed) {
		node.textContent = message;
		node.classList.toggle("error", Boolean(failed));
		node.hidden = false;
	}
	function requestJSON(path) {
		return fetch(base + path, {credentials: "same-origin", cache: "no-store", headers: {"Accept": "application/json"}}).then(response => {
			if (!response.ok) throw new Error("request unavailable");
			return response.json();
		});
	}
	function actorFilter(value) { return value === "" || value === "unassigned" || idPattern.test(value); }
	function readFilters() {
		const filters = {state: cardStateFilter.value, assignee: assigneeFilter.value.trim(), owner: ownerFilter.value.trim(), claim: claimStateFilter.value};
		const valid = (filters.state === "" || states.includes(filters.state)) && actorFilter(filters.assignee) && actorFilter(filters.owner) && ["", "unclaimed", "active", "attention"].includes(filters.claim);
		assigneeFilter.setAttribute("aria-invalid", actorFilter(filters.assignee) ? "false" : "true"); ownerFilter.setAttribute("aria-invalid", actorFilter(filters.owner) ? "false" : "true");
		return valid ? Object.freeze(filters) : null;
	}
	function filterQuery(query) {
		if (appliedFilters.state) query.set("state", appliedFilters.state);
		if (appliedFilters.assignee) query.set("assignee_id", appliedFilters.assignee);
		if (appliedFilters.owner) query.set("owner_id", appliedFilters.owner);
		if (appliedFilters.claim) query.set("claim_state", appliedFilters.claim);
	}
	function textBytes(value) { try { return new TextEncoder().encode(value).length; } catch (_) { return Number.MAX_SAFE_INTEGER; } }
	function validUnicode(value) { for (const rune of value) if (rune.length === 1 && rune.charCodeAt(0) >= 0xd800 && rune.charCodeAt(0) <= 0xdfff) return false; return true; }
	function boundedText(value, max, empty) { return typeof value === "string" && validUnicode(value) && textBytes(value) <= max && (empty || value.trim()) && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(value); }
	function boundedPrintable(value, min, max) { return typeof value === "string" && validUnicode(value) && textBytes(value) >= min && textBytes(value) <= max && !/[\u0000-\u001f\u007f-\u009f]/u.test(value); }
	function validTime(value) { const time = typeof value === "string" ? Date.parse(value) : NaN; return Number.isFinite(time) && time >= Date.UTC(1970, 0, 1) && time < Date.UTC(2261, 0, 1); }
	function uniqueIDs(values, max, reject) { return Array.isArray(values) && values.length <= max && values.every(id => idPattern.test(id) && id !== reject) && new Set(values).size === values.length; }
	function validLabels(values) { const keys = new Set(); return Array.isArray(values) && values.length <= 32 && values.every(label => { const key = typeof label === "string" ? label.trim().toLowerCase() : ""; if (!boundedText(label, 64, false) || keys.has(key)) return false; keys.add(key); return true; }); }
	function validBudget(value) { return value && Number.isSafeInteger(value.attempt_limit) && value.attempt_limit >= 1 && value.attempt_limit <= 32 && Number.isSafeInteger(value.time_limit_ms) && value.time_limit_ms >= 0 && value.time_limit_ms <= 2592000000 && Number.isSafeInteger(value.token_limit) && value.token_limit >= 0 && value.token_limit <= 1000000000 && Number.isSafeInteger(value.cost_micros) && value.cost_micros >= 0 && value.cost_micros <= 1000000000000; }
	function validCriteria(values, revision) { const ids = new Set(); return Array.isArray(values) && values.length <= 32 && values.length > 0 && values.every(item => { if (!item || item.version !== 1 || !idPattern.test(item.id) || ids.has(item.id) || !idPattern.test(item.validator_id) || !boundedText(item.description, 4096, false) || typeof item.required !== "boolean") return false; ids.add(item.id); return item.kind === "objective" && item.required_source === "deterministic" || item.kind === "subjective" && item.required_source === "user_feedback"; }) && Number.isSafeInteger(revision) && revision >= 1; }
	function validCursorTail(value, limit) {
		return Array.isArray(value.items) && value.items.length <= limit && typeof value.has_more === "boolean" &&
			(value.next_cursor === undefined || value.next_cursor === "" || boundedPrintable(value.next_cursor, 1, 512)) && value.has_more === Boolean(value.next_cursor) && (!value.has_more || value.items.length > 0);
	}
	function validBoard(value) {
		return value && value.version === 1 && idPattern.test(value.id) && boundedText(value.title, 256, false) && boundedText(value.description, 65536, true) &&
			["active", "archived"].includes(value.state) && Number.isSafeInteger(value.revision) && value.revision > 0 &&
			Number.isSafeInteger(value.layout_revision) && value.layout_revision > 0 && Number.isSafeInteger(value.event_sequence) && value.event_sequence > 0 &&
			Number.isSafeInteger(value.card_count) && value.card_count >= 0 && value.card_count <= 10000 &&
			Number.isSafeInteger(value.active_claims) && value.active_claims >= 0 && value.active_claims <= value.card_count && (value.state !== "archived" || value.active_claims === 0) &&
			validTime(value.created_at) && validTime(value.updated_at) && Date.parse(value.updated_at) >= Date.parse(value.created_at);
	}
	function validBoardPage(value) {
		return value && value.version === 1 && validCursorTail(value, boardPageLimit) && value.items.every(validBoard);
	}
	function validCard(value, boardID) {
		return value && value.version === 1 && value.board_id === boardID && idPattern.test(value.id) && states.includes(value.state) &&
			boundedText(value.title, 256, false) && boundedPrintable(value.rank, 1, 128) && boundedText(value.description, 65536, true) &&
			["low", "normal", "high", "urgent"].includes(value.priority) && Number.isSafeInteger(value.revision) && value.revision > 0 &&
			validLabels(value.labels) && (!value.parent_id || idPattern.test(value.parent_id) && value.parent_id !== value.id) && uniqueIDs(value.dependencies, 64, value.id) &&
			Number.isSafeInteger(value.remaining_dependencies) && value.remaining_dependencies >= 0 && value.remaining_dependencies <= value.dependencies.length &&
			(!value.assignee_id || idPattern.test(value.assignee_id)) && Number.isSafeInteger(value.attempt_count) && value.attempt_count >= 0 && value.attempt_count <= 32 &&
			(!value.current_attempt_id || idPattern.test(value.current_attempt_id)) && (!value.current_claim_id || idPattern.test(value.current_claim_id)) && (!value.acceptance_id || idPattern.test(value.acceptance_id)) &&
			(!value.block_reason || idPattern.test(value.block_reason)) && typeof value.cancel_requested === "boolean" && typeof value.pause_requested === "boolean" && ["", "requested", "acknowledged", "resume_requested"].includes(value.pause_phase || "") && value.pause_requested === Boolean(value.pause_phase) && validBudget(value.budget) &&
			validCriteria(value.criteria, value.criteria_revision) && validTime(value.created_at) && validTime(value.updated_at) && Date.parse(value.updated_at) >= Date.parse(value.created_at) &&
			(value.remaining_dependencies === 0 || value.state !== "ready") && (value.state === "in_progress" ? value.current_attempt_id && value.current_claim_id && !value.acceptance_id : true) &&
			(value.state === "blocked" ? value.current_attempt_id && value.current_claim_id && !value.acceptance_id && value.block_reason : !value.block_reason) &&
			(value.state === "review" ? value.current_attempt_id && !value.current_claim_id && !value.acceptance_id : true) &&
			(value.state === "done" ? value.current_attempt_id && !value.current_claim_id && value.acceptance_id && value.remaining_dependencies === 0 : true) &&
			(!["in_progress", "blocked", "review", "done"].includes(value.state) ? !value.current_claim_id && !value.acceptance_id : true) &&
			(!(value.cancel_requested || value.pause_requested) || value.state === "in_progress" || value.state === "blocked");
	}
	function validSupervision(item, boardID, card) {
		if (!item || item.version !== 1 || item.board_id !== boardID || item.card_id !== card.id || item.card_revision !== card.revision ||
			!["ready", "running", "stalled", "orphaned"].includes(item.state) || !item.actions ||
			!["claim", "pause_request", "resume_request", "cancel_request", "recovery_check"].every(key => typeof item.actions[key] === "boolean") || Object.keys(item.actions).length !== 5) return false;
		if (item.state === "ready") return card.state === "ready" && item.reason === "dependencies_satisfied" && item.actions.claim && !item.actions.pause_request && !item.actions.cancel_request && !item.actions.recovery_check &&
			!item.actions.resume_request && !item.attempt_id && !item.claim_id && !item.claim_revision && !item.pause_phase && !item.worker_id && !item.task_id && !item.last_heartbeat && !item.expires_at;
		if (!idPattern.test(item.attempt_id) || !idPattern.test(item.claim_id) || !Number.isSafeInteger(item.claim_revision) || item.claim_revision < 1 || !idPattern.test(item.worker_id) ||
			item.task_id && !idPattern.test(item.task_id) || !validTime(item.last_heartbeat) || !validTime(item.expires_at) || Date.parse(item.last_heartbeat) >= Date.parse(item.expires_at) || item.actions.claim ||
			!["", "requested", "acknowledged", "resume_requested"].includes(item.pause_phase || "") || !["in_progress", "blocked"].includes(card.state) || item.attempt_id !== card.current_attempt_id || item.claim_id !== card.current_claim_id || (item.pause_phase || "") !== (card.pause_phase || "") || item.actions.pause_request !== (item.state === "running" && !card.pause_phase) || item.actions.resume_request !== (item.state === "running" && card.pause_phase === "acknowledged") || item.actions.cancel_request !== !card.cancel_requested) return false;
		if (item.state === "running") return item.reason === "lease_healthy" && !item.actions.recovery_check;
		if (item.state === "stalled") return ["heartbeat_stale", "lease_expired"].includes(item.reason) && !item.actions.pause_request && !item.actions.resume_request && item.actions.recovery_check;
		return ["task_completed_with_claim", "task_failed_with_claim", "task_canceled_with_claim"].includes(item.reason) && idPattern.test(item.task_id) && !item.actions.pause_request && !item.actions.resume_request && item.actions.recovery_check;
	}
	function validSnapshot(value, boardID) {
		if (!value || value.version !== 1 || !validBoard(value.board) || value.board.id !== boardID || !Array.isArray(value.columns) || value.columns.length !== states.length ||
			!Array.isArray(value.cards) || value.cards.length > cardPageLimit || typeof value.has_more !== "boolean" || (value.next_cursor !== undefined && typeof value.next_cursor !== "string") ||
			value.has_more !== Boolean(value.next_cursor) || value.next_cursor && !boundedPrintable(value.next_cursor, 1, 512) || value.has_more && value.cards.length === 0 || !Number.isSafeInteger(value.graph_revision) || value.graph_revision < 1 || !digestPattern.test(value.graph_digest) ||
			!value.cards.every(card => validCard(card, boardID)) || value.supervision !== undefined && (!Array.isArray(value.supervision) || value.supervision.length > value.cards.length)) return false;
		const cardMap = new Map(value.cards.map(card => [card.id, card])), supervisionIDs = new Set();
		for (const item of value.supervision || []) { const card = item && cardMap.get(item.card_id); if (!card || supervisionIDs.has(item.card_id) || !validSupervision(item, boardID, card)) return false; supervisionIDs.add(item.card_id); }
		let previousRank = "";
		return value.columns.every((column, index) => {
			const valid = column && column.version === 1 && column.board_id === boardID && column.id === column.state && column.state === states[index] && boundedText(column.title, 256, false) && boundedPrintable(column.rank, 1, 128) && (!index || client.compareText(column.rank, previousRank) > 0);
			previousRank = column && column.rank; return valid;
		});
	}
	function validDependencyPage(value, boardID, cardID, direction) {
		if (!value || value.version !== 1 || value.board_id !== boardID || value.card_id !== cardID || value.direction !== direction || !validCursorTail(value, dependencyLimit) ||
			!Number.isSafeInteger(value.graph_revision) || value.graph_revision < 1 || !/^[0-9a-f]{64}$/.test(value.graph_digest) ||
			value.graph_revision !== snapshotGraphRevision || value.graph_digest !== snapshotGraphDigest) return false;
		const seen = new Set();
		return value.items.every(item => {
			if (!item || item.version !== 1 || item.board_id !== boardID || !idPattern.test(item.card_id) || !idPattern.test(item.dependency_id) || item.card_id === item.dependency_id) return false;
			const key = item.card_id + "\u0000" + item.dependency_id;
			if (seen.has(key) || direction === "prerequisites" && item.card_id !== cardID || direction === "dependents" && item.dependency_id !== cardID) return false;
			seen.add(key); return true;
		});
	}
	const attemptStates = ["running", "review", "accepted", "rejected", "failed", "canceled"];
	function optionalID(value) { return value === undefined || value === "" || idPattern.test(value); }
	function validAttemptRecord(item, boardID, cardID) {
		if (!item || item.version !== 1 || item.board_id !== boardID || item.card_id !== cardID || !idPattern.test(item.id) || !idPattern.test(item.worker_id) || !attemptStates.includes(item.state) ||
			!Number.isSafeInteger(item.ordinal) || item.ordinal < 1 || item.ordinal > 32 || !Number.isSafeInteger(item.revision) || item.revision < 1 ||
			!Number.isSafeInteger(item.criteria_revision) || item.criteria_revision < 1 || !Number.isSafeInteger(item.checkpoint_count) || item.checkpoint_count < 0 || item.checkpoint_count > 10000 ||
			!optionalID(item.candidate_id) || !optionalID(item.acceptance_id) || !validTime(item.started_at) ||
			!(item.state === "running" ? item.ended_at === undefined : validTime(item.ended_at) && Date.parse(item.ended_at) >= Date.parse(item.started_at))) return false;
		return Boolean(item.candidate_id) === ["review", "accepted", "rejected"].includes(item.state) && Boolean(item.acceptance_id) === ["accepted", "rejected"].includes(item.state);
	}
	function validClaim(claim, item) {
		if (!claim || claim.version !== 1 || !idPattern.test(claim.id) || claim.board_id !== item.board_id || claim.card_id !== item.card_id || claim.attempt_id !== item.id ||
			!Number.isSafeInteger(claim.revision) || claim.revision < 1 || !["active", "attention", "released"].includes(claim.state) || claim.owner_id !== item.worker_id || claim.owner_type !== "worker" ||
			!optionalID(claim.task_id) || !validTime(claim.expires_at) || !validTime(claim.last_heartbeat) || Date.parse(claim.expires_at) < Date.parse(claim.last_heartbeat)) return false;
		return claim.state === "released" ? validTime(claim.released_at) && Date.parse(claim.released_at) >= Date.parse(claim.last_heartbeat) : claim.released_at === undefined && Date.parse(claim.expires_at) > Date.parse(claim.last_heartbeat) && Date.parse(claim.expires_at) - Date.parse(claim.last_heartbeat) <= 600000;
	}
	function validCandidate(candidate, item) {
		return candidate && candidate.version === 1 && idPattern.test(candidate.id) && candidate.board_id === item.board_id && candidate.card_id === item.card_id && candidate.attempt_id === item.id &&
			Number.isSafeInteger(candidate.revision) && candidate.revision >= 1 && digestPattern.test(candidate.digest) && candidate.criteria_digest === item.criteria_digest && candidate.policy_digest === item.policy_digest && digestPattern.test(candidate.evidence_digest) &&
			Number.isSafeInteger(candidate.evidence_count) && candidate.evidence_count >= 1 && candidate.evidence_count <= item.evidence.length && boundedText(candidate.summary, 65536, false) &&
			uniqueIDs(candidate.artifact_refs, 32, "") && candidate.submitted_by === item.worker_id && validTime(candidate.created_at);
	}
	function validEvidence(evidence, item, candidate, priorRevision, priorTime) {
		if (!evidence || evidence.version !== 1 || !idPattern.test(evidence.id) || !Number.isSafeInteger(evidence.revision) || evidence.revision <= priorRevision || evidence.board_id !== item.board_id || evidence.card_id !== item.card_id || evidence.attempt_id !== item.id || evidence.candidate_id !== candidate.id ||
			!idPattern.test(evidence.criterion_id) || !["deterministic", "user_feedback", "model_audit"].includes(evidence.source) || !["passed", "failed", "abstained"].includes(evidence.outcome) ||
			!idPattern.test(evidence.actor_id) || !idPattern.test(evidence.reference) || evidence.candidate_digest !== candidate.digest || evidence.criteria_digest !== item.criteria_digest || evidence.policy_digest !== item.policy_digest || !validTime(evidence.created_at) || Date.parse(evidence.created_at) < priorTime) return false;
		return evidence.source === "deterministic" ? evidence.actor_type === "validator" && evidence.outcome !== "abstained" :
			evidence.source === "user_feedback" ? evidence.actor_type === "operator" && evidence.outcome !== "abstained" : evidence.actor_type === "model";
	}
	function validAttempt(item, boardID, cardID) {
		if (!item || item.version !== 1 || item.board_id !== boardID || item.card_id !== cardID || !idPattern.test(item.id) || !idPattern.test(item.worker_id) || !attemptStates.includes(item.state) ||
			!Number.isSafeInteger(item.ordinal) || item.ordinal < 1 || item.ordinal > 32 || !Number.isSafeInteger(item.revision) || item.revision < 1 ||
			!Number.isSafeInteger(item.criteria_revision) || item.criteria_revision < 1 || !digestPattern.test(item.criteria_digest) || !digestPattern.test(item.policy_digest) ||
			!validBudget(item.budget) || !validCriteria(item.criteria, item.criteria_revision) || !uniqueIDs(item.task_ids, 128, "") || !uniqueIDs(item.session_ids, 128, "") ||
			!Array.isArray(item.evidence) || item.evidence.length > 100 || !optionalID(item.acceptance_id) || !optionalID(item.decision_by) || !optionalID(item.decision_authority_id) ||
			!(item.decision_by_type === undefined || item.decision_by_type === "" || item.decision_by_type === "operator" || item.decision_by_type === "validator") ||
			!(item.acceptance_evidence_digest === undefined || item.acceptance_evidence_digest === "" || digestPattern.test(item.acceptance_evidence_digest)) || !validTime(item.started_at) ||
			!(item.state === "running" ? item.ended_at === undefined : validTime(item.ended_at) && Date.parse(item.ended_at) >= Date.parse(item.started_at))) return false;
		if (item.claim === null || item.candidate === null) return false;
		const claim = item.claim === undefined ? null : item.claim, candidate = item.candidate === undefined ? null : item.candidate;
		if (claim && !validClaim(claim, item) || candidate && !validCandidate(candidate, item)) return false;
		const criterionIDs = new Set(item.criteria.map(criterion => criterion.id)), evidenceIDs = new Set(); let priorEvidenceRevision = 0, priorEvidenceTime = 0;
		if (candidate && (!item.evidence.every(evidence => { if (evidenceIDs.has(evidence.id) || !criterionIDs.has(evidence.criterion_id) || !validEvidence(evidence, item, candidate, priorEvidenceRevision, priorEvidenceTime)) return false; evidenceIDs.add(evidence.id); priorEvidenceRevision = evidence.revision; priorEvidenceTime = Date.parse(evidence.created_at); return true; }) || item.state === "review" && candidate.evidence_count !== item.evidence.length)) return false;
		const noDecision = !item.acceptance_id && !item.decision_by && !item.decision_by_type && !item.decision_authority_id && !item.acceptance_evidence_digest;
		if (item.state === "running") return claim && claim.state !== "released" && !candidate && noDecision;
		if (item.state === "review") return candidate && claim && claim.state === "released" && noDecision;
		if (item.state === "accepted" || item.state === "rejected") return candidate && claim && claim.state === "released" && idPattern.test(item.acceptance_id) && idPattern.test(item.decision_by) && item.decision_by !== item.worker_id &&
			(item.decision_by_type === "operator" || item.decision_by_type === "validator") && idPattern.test(item.decision_authority_id) && digestPattern.test(item.acceptance_evidence_digest);
		return (!claim || claim.state === "released") && noDecision;
	}
	function validAttemptHistory(value, boardID, cardID) {
		if (!value || value.version !== 1 || value.board_id !== boardID || value.card_id !== cardID || !validCursorTail(value, attemptLimit) ||
			!Number.isSafeInteger(value.high_water_ordinal) || value.high_water_ordinal < 0 || value.high_water_ordinal > 32 || (value.high_water_ordinal === 0) !== (value.items.length === 0)) return false;
		let previous = value.high_water_ordinal + 1;
		return value.items.every(item => { const valid = validAttemptRecord(item, boardID, cardID) && item.ordinal < previous && item.ordinal <= value.high_water_ordinal; previous = item.ordinal; return valid; }) &&
			(value.has_more || !value.items.length || previous === 1);
	}
	function validAttemptDetail(value, boardID, cardID, attemptID) {
		if (!value || value.version !== 1 || !validAttempt(value.attempt, boardID, cardID) || value.attempt.id !== attemptID || !Array.isArray(value.checkpoints) ||
			value.checkpoints.length > attemptLimit || typeof value.has_more !== "boolean" || (value.next_cursor !== undefined && typeof value.next_cursor !== "string") || value.has_more !== Boolean(value.next_cursor) ||
			(value.next_cursor && !boundedPrintable(value.next_cursor, 1, 512)) || value.has_more && value.checkpoints.length === 0 ||
			!Number.isSafeInteger(value.checkpoint_high_water_revision) || value.checkpoint_high_water_revision < 0 || value.checkpoint_high_water_revision > 10000 ||
			(value.checkpoint_high_water_revision === 0) !== (value.checkpoints.length === 0)) return false;
		let previous = value.checkpoint_high_water_revision + 1;
		return value.checkpoints.every(item => { const valid = item && item.version === 1 && item.board_id === boardID && item.card_id === cardID && item.attempt_id === attemptID &&
			idPattern.test(item.id) && idPattern.test(item.claim_id) && idPattern.test(item.actor_id) && item.actor_type === "worker" && Number.isSafeInteger(item.revision) && item.revision >= 1 &&
			item.revision < previous && boundedText(item.evidence, 65536, false) && digestPattern.test(item.criteria_digest) && digestPattern.test(item.policy_digest) && digestPattern.test(item.evidence_digest) &&
			Number.isSafeInteger(item.claim_revision) && item.claim_revision >= 1 && Number.isSafeInteger(item.criteria_revision) && item.criteria_revision >= 1 && validTime(item.created_at); previous = item.revision; return valid; }) &&
			(value.has_more || !value.checkpoints.length || previous === 1);
	}
	function validAcceptance(acceptance, attempt) {
		if (!acceptance || acceptance.version !== 1 || !idPattern.test(acceptance.id) || acceptance.id !== attempt.acceptance_id || acceptance.board_id !== attempt.board_id || acceptance.card_id !== attempt.card_id || acceptance.attempt_id !== attempt.id ||
			!attempt.candidate || acceptance.candidate_id !== attempt.candidate.id || acceptance.candidate_digest !== attempt.candidate.digest || acceptance.criteria_revision !== attempt.criteria_revision || acceptance.criteria_digest !== attempt.criteria_digest ||
			!Number.isSafeInteger(acceptance.prior_evidence_head_revision) || acceptance.prior_evidence_head_revision < 1 || !digestPattern.test(acceptance.prior_evidence_set_digest) ||
			!Number.isSafeInteger(acceptance.evidence_head_revision) || acceptance.evidence_head_revision < acceptance.prior_evidence_head_revision || !digestPattern.test(acceptance.evidence_set_digest) || acceptance.evidence_set_digest !== attempt.acceptance_evidence_digest ||
			acceptance.policy_digest !== attempt.policy_digest || !["accepted", "rejected"].includes(acceptance.decision) || acceptance.decision !== attempt.state || acceptance.decided_by !== attempt.decision_by || acceptance.decided_by === attempt.worker_id ||
			acceptance.decided_by_type !== attempt.decision_by_type || !["operator", "validator"].includes(acceptance.decided_by_type) || acceptance.decision_authority_id !== attempt.decision_authority_id ||
			!boundedText(acceptance.rationale, 65536, false) || !validTime(acceptance.decided_at) || acceptance.decided_at !== attempt.ended_at || !attempt.evidence.length) return false;
		const last = attempt.evidence[attempt.evidence.length - 1];
		return acceptance.evidence_head_revision === last.revision && acceptance.prior_evidence_head_revision <= attempt.evidence.length;
	}
	function validLifecycle(item, boardID, card) {
		if (!item || item.version !== 1 || item.card_id !== card.id || !validAttempt(item.attempt, boardID, card.id) || item.attempt.id !== card.current_attempt_id || item.attempt.ordinal !== card.attempt_count ||
			!Array.isArray(item.checkpoints) || item.checkpoints.length > 100 || !Number.isSafeInteger(item.checkpoint_count) || item.checkpoint_count < item.checkpoints.length || item.checkpoint_count > 10000 ||
			typeof item.checkpoints_has_more !== "boolean" || item.checkpoints_has_more !== (item.checkpoint_count > item.checkpoints.length) || item.acceptance === null) return false;
		const acceptanceExpected = item.attempt.state === "accepted" || item.attempt.state === "rejected";
		if (acceptanceExpected !== (item.acceptance !== undefined) || item.acceptance && !validAcceptance(item.acceptance, item.attempt)) return false;
		if (card.current_claim_id && (!item.attempt.claim || item.attempt.claim.id !== card.current_claim_id || item.attempt.claim.state === "released") ||
			!card.current_claim_id && item.attempt.claim && item.attempt.claim.state !== "released" || card.acceptance_id && card.acceptance_id !== item.attempt.acceptance_id ||
			["in_progress", "blocked"].includes(card.state) && item.attempt.state !== "running" || card.state === "review" && item.attempt.state !== "review" || card.state === "done" && item.attempt.state !== "accepted") return false;
		let previousRevision = 0;
		return item.checkpoints.every(checkpoint => { const valid = checkpoint && checkpoint.version === 1 && checkpoint.board_id === boardID && checkpoint.card_id === card.id && checkpoint.attempt_id === item.attempt.id && item.attempt.claim && checkpoint.claim_id === item.attempt.claim.id &&
			idPattern.test(checkpoint.id) && Number.isSafeInteger(checkpoint.revision) && checkpoint.revision > previousRevision && checkpoint.revision <= 10000 && Number.isSafeInteger(checkpoint.claim_revision) && checkpoint.claim_revision >= 1 &&
			checkpoint.criteria_revision === item.attempt.criteria_revision && checkpoint.criteria_digest === item.attempt.criteria_digest && checkpoint.policy_digest === item.attempt.policy_digest && boundedText(checkpoint.evidence, 65536, false) &&
			digestPattern.test(checkpoint.evidence_digest) && checkpoint.actor_id === item.attempt.worker_id && checkpoint.actor_type === "worker" && validTime(checkpoint.created_at); previousRevision = checkpoint && checkpoint.revision; return valid; });
	}
	function validLifecycleBatch(snapshot, reset) {
		const items = snapshot.lifecycle === undefined ? [] : snapshot.lifecycle;
		if (!Array.isArray(items) || items.length > snapshot.cards.length) return null;
		const cards = new Map(snapshot.cards.map(card => [card.id, card])), incoming = new Map();
		for (const item of items) {
			const card = item && cards.get(item.card_id);
			if (!card || incoming.has(item.card_id) || !reset && lifecycleByCard.has(item.card_id) || !validLifecycle(item, snapshot.board.id, card)) return null;
			incoming.set(item.card_id, item);
		}
		return incoming;
	}
	function validSupervisionBatch(snapshot, reset) {
		const items = snapshot.supervision === undefined ? [] : snapshot.supervision, cards = new Map(snapshot.cards.map(card => [card.id, card])), incoming = new Map();
		if (!Array.isArray(items) || items.length > snapshot.cards.length) return null;
		for (const item of items) { const card = item && cards.get(item.card_id); if (!card || incoming.has(item.card_id) || !reset && supervisionByCard.has(item.card_id) || !validSupervision(item, snapshot.board.id, card)) return null; incoming.set(item.card_id, item); }
		return incoming;
	}
	function cardLifecycleSummary(card, lifecycle) {
		const required = card.criteria.filter(criterion => criterion.required);
		const result = {criteria: "Criteria revision " + String(card.criteria_revision) + " · " + String(required.length) + " required of " + String(card.criteria.length), required: required.map(criterion => criterion.description), attempt: "No current attempt lifecycle in this page.", acceptance: "Acceptance not recorded.", claim: "No claim recorded.", attention: false};
		if (!lifecycle) return result;
		const attempt = lifecycle.attempt; result.attempt = "Attempt " + String(attempt.ordinal) + " · " + attempt.state + " · criteria revision " + String(attempt.criteria_revision);
		if (lifecycle.acceptance) result.acceptance = "Acceptance " + lifecycle.acceptance.decision + " · decided by " + lifecycle.acceptance.decided_by;
		if (attempt.claim) { result.attention = attempt.claim.state === "attention"; result.claim = "Claim owner " + attempt.claim.owner_id + " · " + attempt.claim.state + " · heartbeat " + attempt.claim.last_heartbeat + " · expires " + attempt.claim.expires_at + (result.attention ? " · stale/orphan attention required" : ""); }
		return result;
	}
	function boardLink(board) {
		const item = element("li");
		const link = element("a");
		link.href = base + "/workboards/" + encodeURIComponent(board.id);
		if (board.id === selectedID) link.setAttribute("aria-current", "page");
		link.append(element("span", "board-name", board.title));
		link.append(element("span", "board-summary", String(board.card_count) + " cards · " + board.state));
		item.append(link);
		return item;
	}
	function loadBoards(after, reset) {
		const current = ++boardRequestVersion;
		if (reset) {
			list.replaceChildren(); boardTotal = 0; boardCursor = ""; boardIDs.clear(); boardCursors.clear();
			notice(listState, "Loading workboards…", false);
		}
		loadMoreBoards.disabled = true;
		const boardState = appliedBoardState;
		if (boardState !== "active" && boardState !== "archived") { notice(listState, "Choose a valid board state.", true); return; }
		const query = new URLSearchParams({limit: String(boardPageLimit), state: boardState});
		if (after) query.set("after", after);
		requestJSON("/api/v1/workboards?" + query.toString()).then(page => {
			if (!client.current(current, boardRequestVersion)) return;
			if (!validBoardPage(page) || boardTotal + page.items.length > maxBoards || after && boardCursors.has(after) || page.has_more && (page.next_cursor === after || boardCursors.has(page.next_cursor))) throw new Error("invalid workboard page");
			const incoming = new Set();
			for (const board of page.items) if (boardIDs.has(board.id) || incoming.has(board.id)) throw new Error("duplicate board"); else incoming.add(board.id);
			const nodes = page.items.map(boardLink);
			if (after) boardCursors.add(after);
			for (let index = 0; index < page.items.length; index++) { boardIDs.add(page.items[index].id); list.append(nodes[index]); }
			boardTotal += page.items.length; boardCursor = page.next_cursor || "";
			boardCount.textContent = String(boardTotal);
			listState.hidden = boardTotal > 0;
			if (!boardTotal) notice(listState, "No " + boardState + " workboards.", false);
			loadMoreBoards.hidden = !page.has_more || boardTotal >= maxBoards;
			loadMoreBoards.disabled = false;
		}).catch(() => {
			if (current !== boardRequestVersion) return;
			notice(listState, "Workboards could not be loaded. Use Refresh to try again.", true);
			loadMoreBoards.hidden = true;
		});
	}
	function renderColumns(columns) {
		kanban.replaceChildren(); laneLists.clear(); laneCounts.clear();
		for (const column of columns) {
			const lane = element("section", "kanban-column");
			lane.setAttribute("aria-labelledby", "lane-" + column.state);
			const heading = element("h3", "kanban-column-heading");
			heading.id = "lane-" + column.state;
			heading.append(element("span", "", column.title));
			const count = element("span", "count", "0");
			heading.append(count);
			const cards = element("ol", "kanban-cards");
			cards.setAttribute("aria-label", column.title + " cards");
			lane.append(heading, cards);
			kanban.append(lane);
			laneLists.set(column.state, cards); laneCounts.set(column.state, count);
		}
	}
	function renderPresentation() {
		const listMode = presentation === "list";
		const focused = document.activeElement;
		const cards = pendingPosition ? client.provisionalPosition(loadedCards, pendingPosition) : loadedCards;
		showKanban.setAttribute("aria-pressed", listMode ? "false" : "true"); showList.setAttribute("aria-pressed", listMode ? "true" : "false");
		showKanban.classList.toggle("secondary", listMode); showList.classList.toggle("secondary", !listMode);
		if (!selectedID || visibleColumns.length !== states.length) { kanban.hidden = true; cardList.hidden = true; return; }
		if (listMode) {
			cardList.replaceChildren(); client.reparent(cards, cardNodes, () => cardList, focused); cardList.hidden = false; kanban.hidden = true;
		} else {
			renderColumns(visibleColumns);
			client.reparent(cards, cardNodes, card => laneLists.get(card.state), focused);
			for (const state of states) laneCounts.get(state).textContent = String(laneLists.get(state).children.length);
			cardList.hidden = true; kanban.hidden = false;
		}
		for (const card of cards) { const node = cardNodes.get(card.id), badge = node && node.querySelector(".card-provisional"); if (node) node.classList.toggle("provisional-position", Boolean(card.provisional)); if (badge) badge.hidden = !card.provisional; }
	}
	function detailList(title) {
		const section = element("section", "card-detail-section"); section.append(element("h5", "", title));
		const items = element("ul", "card-detail-list"); section.append(items); return {section, items};
	}
	function renderDependencyItems(target, page, direction) {
		if (!page.items.length) target.append(element("li", "detail-empty", direction === "prerequisites" ? "No prerequisites." : "No dependents."));
		for (const link of page.items) target.append(element("li", "", direction === "prerequisites" ? link.dependency_id : link.card_id));
		if (page.has_more) target.append(element("li", "detail-more", "Showing the first " + String(page.items.length) + " relationships; more are available."));
	}
	function immutable(value) {
		if (Array.isArray(value)) return Object.freeze(value.map(immutable));
		if (!value || typeof value !== "object") return value;
		const result = {}; for (const [key, item] of Object.entries(value)) result[key] = immutable(item); return Object.freeze(result);
	}
	function cardContext(card) { return Object.freeze({...card, labels: Object.freeze(card.labels.slice()), dependencies: Object.freeze(card.dependencies.slice()), criteria: Object.freeze(card.criteria.map(item => Object.freeze({...item}))), budget: Object.freeze({...card.budget})}); }
	function restoreCardView(card) {
		selectedCard = null;
		if (!card) return null;
		const item = cardNodes.get(card.id), toggle = item && item.querySelector(".card-toggle"), details = item && item.querySelector(".card-details");
		if (!item || !toggle || !details) return null;
		item.classList.add("selected-card"); toggle.setAttribute("aria-pressed", "true"); toggle.setAttribute("aria-expanded", selectedCardAnchor.expanded ? "true" : "false"); details.hidden = !selectedCardAnchor.expanded;
		if (selectedCardAnchor.expanded && !details.dataset.loaded) { details.dataset.loaded = "true"; loadCardDetails(card, details); }
		selectedCard = cardContext(card);
		return selectedCard;
	}
	function renderCandidateReview(target, card, attempt) {
		const candidate = attempt.candidate;
		const candidateSection = detailList("Candidate output");
		candidateSection.items.append(element("li", "", candidate.summary));
		if (candidate.artifact_refs.length) for (const reference of candidate.artifact_refs) candidateSection.items.append(element("li", "", "Artifact: " + reference));
		else candidateSection.items.append(element("li", "detail-empty", "No artifact references."));
		const criteriaSection = detailList("Acceptance criteria and evidence"), audits = detailList("Advisory model audits");
		for (const criterion of attempt.criteria) {
			const item = element("li"), heading = element("p", "", criterion.description);
			heading.append(document.createTextNode(" · " + (criterion.kind === "objective" ? "Objective" : "Subjective") + " · " + (criterion.required ? "Required" : "Optional") + " · source " + criterion.required_source)); item.append(heading);
			const records = attempt.evidence.filter(evidence => evidence.criterion_id === criterion.id && evidence.source !== "model_audit"), list = element("ul", "card-detail-list");
			for (const evidence of records) list.append(element("li", "", evidence.source + " · " + evidence.outcome + " · reference " + evidence.reference));
			if (!records.length) list.append(element("li", "detail-empty", "No objective or user-feedback evidence recorded."));
			item.append(list); criteriaSection.items.append(item);
		}
		const advisory = attempt.evidence.filter(evidence => evidence.source === "model_audit");
		for (const evidence of advisory) audits.items.append(element("li", "", evidence.outcome + " · criterion " + evidence.criterion_id + " · reference " + evidence.reference));
		if (!advisory.length) audits.items.append(element("li", "detail-empty", "No model-audit evidence."));
		audits.section.insertBefore(element("p", "help", "Model-audit evidence is advisory and does not independently authorize acceptance."), audits.items);
		target.replaceChildren(element("p", "", "Exact review candidate from attempt " + attempt.id + "."), candidateSection.section, criteriaSection.section, audits.section);
		target.setAttribute("aria-busy", "false");
		return immutable({card, attempt});
	}
	function loadCandidateReview(card, target, button, onReady) {
		if (card.state !== "review" || !idPattern.test(card.current_attempt_id) || card.current_claim_id || card.acceptance_id) return;
		button.disabled = true; button.setAttribute("aria-expanded", "true"); target.hidden = false; target.setAttribute("aria-busy", "true"); notice(target, "Loading the exact candidate and acceptance evidence…", false);
		const query = new URLSearchParams({limit: String(attemptLimit)}), path = "/api/v1/workboards/" + encodeURIComponent(card.board_id) + "/cards/" + encodeURIComponent(card.id) + "/attempts/" + encodeURIComponent(card.current_attempt_id) + "?" + query.toString();
		requestJSON(path).then(page => {
			const current = loadedCards.find(item => item.id === card.id);
			if (!current || current.revision !== card.revision || current.state !== "review" || current.current_attempt_id !== card.current_attempt_id || !validAttemptDetail(page, card.board_id, card.id, card.current_attempt_id) || page.attempt.state !== "review") throw new Error("stale candidate review");
			const detail = renderCandidateReview(target, card, page.attempt); button.disabled = false; onReady(detail);
		}).catch(() => { delete target.dataset.loaded; target.setAttribute("aria-busy", "false"); notice(target, "Candidate review could not be validated. Refresh the board before deciding.", true); button.disabled = false; button.setAttribute("aria-expanded", "false"); });
	}
	function loadAttemptDetail(boardID, cardID, attempt, target, button) {
		button.disabled = true; notice(target, "Loading attempt detail…", false);
		const query = new URLSearchParams({limit: String(attemptLimit)});
		requestJSON("/api/v1/workboards/" + encodeURIComponent(boardID) + "/cards/" + encodeURIComponent(cardID) + "/attempts/" + encodeURIComponent(attempt.id) + "?" + query.toString()).then(page => {
			if (!validAttemptDetail(page, boardID, cardID, attempt.id)) throw new Error("invalid attempt detail");
			target.replaceChildren(element("p", "", "Bounded preview: worker " + page.attempt.worker_id + " · " + page.attempt.state + " · revision " + String(page.attempt.revision) + "."));
			const claim = page.attempt.claim;
			if (claim) {
				const attention = claim.state === "attention" ? " · stale/orphan attention required" : "";
				target.append(element("p", claim.state === "attention" ? "card-alert" : "", "Lease owner " + claim.owner_id + " · " + claim.state + " · expires " + claim.expires_at + " · heartbeat " + claim.last_heartbeat + attention + "."));
			} else target.append(element("p", "detail-empty", "No lease is recorded for this attempt."));
			const checkpoints = element("ol", "checkpoint-list");
			for (const checkpoint of page.checkpoints) checkpoints.append(element("li", "", "Checkpoint " + String(checkpoint.revision) + " · " + checkpoint.created_at));
			if (!page.checkpoints.length) checkpoints.append(element("li", "detail-empty", "No checkpoints."));
			target.append(checkpoints);
			if (page.has_more) target.append(element("p", "detail-more", "Showing the first " + String(page.checkpoints.length) + " checkpoints; more are available."));
			button.disabled = false;
		}).catch(() => { delete target.dataset.loaded; notice(target, "Attempt preview could not be loaded. Close and reopen to retry.", true); button.disabled = false; });
	}
	function renderAttempts(target, page, boardID, cardID) {
		if (!page.items.length) target.append(element("li", "detail-empty", "No attempts yet."));
		for (const attempt of page.items) {
			const item = element("li"); const button = element("button", "attempt-toggle", "Attempt " + String(attempt.ordinal) + " · " + attempt.state);
			button.type = "button"; button.setAttribute("aria-expanded", "false");
			const detail = element("div", "attempt-detail"); detail.hidden = true; detail.setAttribute("role", "status");
			button.addEventListener("click", () => { const opening = detail.hidden; detail.hidden = !opening; button.setAttribute("aria-expanded", opening ? "true" : "false"); if (opening && !detail.dataset.loaded) { detail.dataset.loaded = "true"; loadAttemptDetail(boardID, cardID, attempt, detail, button); } });
			item.append(button, detail); target.append(item);
		}
		if (page.has_more) target.append(element("li", "detail-more", "Showing the first " + String(page.items.length) + " attempts; more are available."));
	}
	function loadCardDetails(card, target) {
		target.replaceChildren(); const status = element("div", "notice compact", "Loading dependencies and attempts…"); status.setAttribute("role", "status"); target.append(status);
		const dependencyPath = "/api/v1/workboards/" + encodeURIComponent(card.board_id) + "/cards/" + encodeURIComponent(card.id) + "/dependencies?";
		const prerequisitesQuery = new URLSearchParams({direction: "prerequisites", limit: String(dependencyLimit)});
		const dependentsQuery = new URLSearchParams({direction: "dependents", limit: String(dependencyLimit)});
		const attemptsQuery = new URLSearchParams({limit: String(attemptLimit)});
		Promise.all([
			requestJSON(dependencyPath + prerequisitesQuery.toString()), requestJSON(dependencyPath + dependentsQuery.toString()),
			requestJSON("/api/v1/workboards/" + encodeURIComponent(card.board_id) + "/cards/" + encodeURIComponent(card.id) + "/attempts?" + attemptsQuery.toString())
		]).then(([prerequisites, dependents, attempts]) => {
			if (!validDependencyPage(prerequisites, card.board_id, card.id, "prerequisites") || !validDependencyPage(dependents, card.board_id, card.id, "dependents") || !validAttemptHistory(attempts, card.board_id, card.id)) throw new Error("invalid card detail");
			target.replaceChildren(); const before = detailList("Prerequisites preview"), after = detailList("Dependents preview"), history = detailList("Attempt history preview");
			renderDependencyItems(before.items, prerequisites, "prerequisites"); renderDependencyItems(after.items, dependents, "dependents"); renderAttempts(history.items, attempts, card.board_id, card.id);
			target.append(before.section, after.section, history.section);
		}).catch(() => { delete target.dataset.loaded; notice(status, "Card previews could not be loaded. Close and reopen the card to retry.", true); });
	}
	function cardNode(card, lifecycleRecord, supervision) {
		const item = element("li", "kanban-card");
		item.dataset.cardId = card.id;
		const article = element("article");
		const toggle = element("button", "card-toggle", card.title);
		toggle.type = "button"; toggle.setAttribute("aria-expanded", "false"); toggle.setAttribute("aria-pressed", "false"); toggle.setAttribute("aria-label", "Select and inspect card: " + card.title);
		const meta = element("div", "card-meta");
		meta.append(element("span", "", card.priority + " priority"));
		meta.append(element("span", "", card.state.replace("_", " ") + " state"));
		meta.append(element("span", "", "revision " + String(card.revision)));
		const lifecycleSummary = cardLifecycleSummary(card, lifecycleRecord);
		meta.append(element("span", "", lifecycleSummary.criteria));
		if (lifecycleRecord) meta.append(element("span", "", lifecycleSummary.attempt));
		if (card.remaining_dependencies > 0) meta.append(element("span", "card-alert", String(card.remaining_dependencies) + " dependencies remaining"));
		if (card.block_reason) meta.append(element("span", "card-alert", "blocked: " + card.block_reason));
		if (card.assignee_id && idPattern.test(card.assignee_id)) meta.append(element("span", "", "assigned " + card.assignee_id));
		if (supervision) meta.append(element("span", supervision.state === "stalled" || supervision.state === "orphaned" ? "card-alert" : "", "supervision " + supervision.state + " · " + supervision.reason.replaceAll("_", " ")));
		else if (lifecycleSummary.attention) meta.append(element("span", "card-alert", "stale/orphan claim attention"));
		if (card.pause_phase === "requested") meta.append(element("span", "card-alert", "pause requested · awaiting safe boundary"));
		if (card.pause_phase === "acknowledged") meta.append(element("span", "card-alert", "paused · worker acknowledged"));
		if (card.pause_phase === "resume_requested") meta.append(element("span", "card-alert", "resume requested · awaiting worker acknowledgement"));
		if (card.cancel_requested) meta.append(element("span", "card-alert", "cancel requested"));
		const provisional = element("span", "card-alert card-provisional", "Pending position — not saved"); provisional.hidden = true; meta.append(provisional);
		const position = element("div", "card-position-controls"); position.setAttribute("role", "group"); position.setAttribute("aria-label", "Position " + card.title);
		for (const [direction, label] of [["up", "Move up"], ["down", "Move down"], ["ready", "Move to Ready"], ["backlog", "Move to Backlog"]]) {
			const button = element("button", "secondary card-position", label); button.type = "button"; button.disabled = true; button.dataset.cardId = card.id; button.dataset.position = direction;
			button.setAttribute("aria-label", label + ": " + card.title + (direction === "ready" || direction === "backlog" ? ", at end" : ""));
			button.addEventListener("click", () => window.dispatchEvent(new CustomEvent("darwin:card-position", {detail: Object.freeze({cardID: card.id, direction})})));
			position.append(button);
		}
		const lifecycleControls = element("div", "card-lifecycle-controls"); lifecycleControls.setAttribute("role", "group"); lifecycleControls.setAttribute("aria-label", "Control " + card.title);
		for (const [action, label] of [["card.pause_request", "Request pause"], ["card.resume_request", "Resume"], ["card.cancel_request", "Request cancellation"]]) {
			const button = element("button", action === "card.cancel_request" ? "danger card-control" : "secondary card-control", label); button.type = "button"; button.disabled = true; button.dataset.cardId = card.id; button.dataset.control = action;
			button.setAttribute("aria-label", label + ": " + card.title); button.addEventListener("click", () => window.dispatchEvent(new CustomEvent("darwin:card-control", {detail: Object.freeze({cardID: card.id, action})}))); lifecycleControls.append(button);
		}
		const review = element("div", "candidate-review"); review.hidden = true; review.setAttribute("role", "region"); review.setAttribute("aria-label", "Candidate review for " + card.title);
		if (card.state === "review") {
			let reviewDetail = null;
			const button = element("button", "secondary card-review", "Review candidate"); button.type = "button"; button.dataset.cardId = card.id; button.setAttribute("aria-label", "Review candidate: " + card.title); button.setAttribute("aria-expanded", "false");
			button.addEventListener("click", () => { if (reviewDetail) window.dispatchEvent(new CustomEvent("darwin:acceptance-review", {detail: reviewDetail})); else if (!review.dataset.loaded) { review.dataset.loaded = "true"; loadCandidateReview(card, review, button, detail => { reviewDetail = detail; window.dispatchEvent(new CustomEvent("darwin:acceptance-review", {detail})); }); } }); lifecycleControls.append(button);
		}
		const details = element("div", "card-details"); details.hidden = true;
		if (card.description) details.append(element("p", "", card.description));
		details.append(element("p", "", card.assignee_id && idPattern.test(card.assignee_id) ? "Assignee: " + card.assignee_id : "Unassigned"));
		details.append(element("p", "", "Attempts: " + String(card.attempt_count) + (card.current_claim_id && idPattern.test(card.current_claim_id) ? " · active claim " + card.current_claim_id : " · no active claim")));
		if (supervision) details.append(element("p", supervision.state === "stalled" || supervision.state === "orphaned" ? "card-alert" : "", "Supervisor: " + supervision.state + " · " + supervision.reason.replaceAll("_", " ") + (supervision.worker_id ? " · worker " + supervision.worker_id : "") + (supervision.expires_at ? " · lease expires " + supervision.expires_at : "")));
		if (card.block_reason) details.append(element("p", "card-alert", "Block reason: " + card.block_reason));
		details.append(element("p", "", lifecycleSummary.criteria));
		const requiredCriteria = element("ul", "card-detail-list");
		for (const criterion of lifecycleSummary.required) requiredCriteria.append(element("li", "", "Required: " + criterion));
		if (!lifecycleSummary.required.length) requiredCriteria.append(element("li", "detail-empty", "No required criteria."));
		details.append(requiredCriteria, element("p", "", lifecycleSummary.attempt), element("p", "", lifecycleSummary.acceptance), element("p", lifecycleSummary.attention ? "card-alert" : "", lifecycleSummary.claim));
		details.append(element("p", "", card.labels.length ? "Labels: " + card.labels.join(", ") : "No labels"));
		toggle.addEventListener("click", () => { for (const node of cardNodes.values()) { node.classList.remove("selected-card"); const prior = node.querySelector(".card-toggle"); if (prior) prior.setAttribute("aria-pressed", "false"); } const expanded = toggle.getAttribute("aria-expanded") === "true"; selectedCardAnchor = client.cardViewAnchor(card.board_id, card.id, !expanded); item.classList.add("selected-card"); toggle.setAttribute("aria-pressed", "true"); toggle.setAttribute("aria-expanded", expanded ? "false" : "true"); details.hidden = expanded; selectedCard = cardContext(card); for (const observer of contextObservers) observer(); if (!expanded && !details.dataset.loaded) { details.dataset.loaded = "true"; loadCardDetails(card, details); } });
		article.append(toggle, meta, position, lifecycleControls, review, details); item.append(article);
		return item;
	}
	function validateCardBatch(cards, reset) {
		const previous = reset || !loadedCards.length ? null : loadedCards[loadedCards.length - 1];
		const result = client.canonical(cards, previous, reset ? [] : cardIDs, states);
		return result && result.ranks;
	}
	function clearCardState() {
		cardCursor = ""; cardTotal = 0; snapshotGraphRevision = 0; snapshotGraphDigest = ""; currentBoard = null; selectedCard = null; pendingPosition = null; cardIDs.clear(); cardCursors.clear(); laneRanks.clear(); lifecycleByCard.clear(); supervisionByCard.clear(); snapshotFence = null; loadedCards = []; visibleColumns = [];
		kanban.replaceChildren(); cardList.replaceChildren(); laneLists.clear(); laneCounts.clear(); cardNodes.clear(); kanban.hidden = true; cardList.hidden = true; loadMoreCards.hidden = true;
		for (const observer of contextObservers) observer();
	}
	function appendCards(cards, ranks, lifecycle, supervision) {
		const nodes = cards.map(card => cardNode(card, lifecycle.get(card.id), supervision.get(card.id)));
		for (const [cardID, item] of lifecycle) lifecycleByCard.set(cardID, item);
		for (const [cardID, item] of supervision) supervisionByCard.set(cardID, item);
		for (let index = 0; index < cards.length; index++) { cardIDs.add(cards[index].id); cardNodes.set(cards[index].id, nodes[index]); }
		for (const state of states) if (ranks.has(state)) laneRanks.set(state, ranks.get(state));
		loadedCards.push(...cards); renderPresentation();
	}
	function loadBoard(boardID, after, reset) {
		if (!idPattern.test(boardID)) { notice(stateNode, "The workboard address is invalid.", true); return; }
		const activeFocus = document.activeElement, focusBoardID = currentBoard && currentBoard.id || selectedID;
		const capturedFocus = reset ? client.captureFocusAnchor(activeFocus, focusBoardID, cardNodes) : null;
		const focusAnchor = reset ? client.refreshFocusAnchor(capturedFocus, pendingFocusAnchor, boardID, activeFocus, document.body) : null;
		const viewTransition = reset ? client.cardViewTransition(selectedCardAnchor, focusBoardID, boardID, [], false) : null;
		selectedID = boardID;
		const current = ++cardRequestVersion;
		if (reset) {
			pendingFocusAnchor = focusAnchor; pendingFocusVersion = current;
			selectedCardAnchor = viewTransition ? viewTransition.anchor : null; clearCardState();
			selectedTitle.textContent = "Loading workboard…"; selectedMeta.textContent = "";
			notice(stateNode, "Loading cards and lanes…", false);
		}
		kanban.setAttribute("aria-busy", "true"); cardList.setAttribute("aria-busy", "true"); loadMoreCards.disabled = true;
		const query = new URLSearchParams({limit: String(cardPageLimit)});
		filterQuery(query);
		if (after) query.set("after", after);
		requestJSON("/api/v1/workboards/" + encodeURIComponent(boardID) + "?" + query.toString()).then(snapshot => {
			if (!client.current(current, cardRequestVersion, boardID, selectedID)) return;
			if (!validSnapshot(snapshot, boardID) || cardTotal + snapshot.cards.length > maxCards || after && cardCursors.has(after) || snapshot.has_more && (snapshot.next_cursor === after || cardCursors.has(snapshot.next_cursor))) throw new Error("invalid workboard snapshot");
			const lifecycle = validLifecycleBatch(snapshot, reset), supervision = validSupervisionBatch(snapshot, reset); if (!lifecycle || !supervision) throw new Error("invalid workboard lifecycle");
			const ranks = validateCardBatch(snapshot.cards, reset); if (!ranks) throw new Error("invalid card order");
			const columnSignature = snapshot.columns.map(column => [column.id, column.state, column.title, column.rank].join("\u0000")).join("\u0001");
			const filterSignature = [appliedFilters.state, appliedFilters.assignee, appliedFilters.owner, appliedFilters.claim].join("\u0000");
			const fence = JSON.stringify([snapshot.board.revision, snapshot.board.layout_revision, snapshot.board.event_sequence, snapshot.graph_revision, snapshot.graph_digest, columnSignature, filterSignature]);
			if (snapshotFence && snapshotFence !== fence) throw new Error("workboard changed during pagination");
			if (!snapshotFence) snapshotFence = fence;
			snapshotGraphRevision = snapshot.graph_revision; snapshotGraphDigest = snapshot.graph_digest;
			streamRevision = Math.max(streamRevision, snapshot.board.event_sequence);
			if (after) cardCursors.add(after);
			if (reset) visibleColumns = snapshot.columns.slice();
			appendCards(snapshot.cards, ranks, lifecycle, supervision);
			cardTotal += snapshot.cards.length; cardCursor = snapshot.next_cursor || "";
			selectedTitle.textContent = snapshot.board.title;
			currentBoard = Object.freeze({...snapshot.board});
			const filtered = appliedFilters.state || appliedFilters.assignee || appliedFilters.owner || appliedFilters.claim;
			const cardView = client.cardViewTransition(selectedCardAnchor, boardID, boardID, loadedCards, !filtered && !snapshot.has_more && cardTotal === snapshot.board.card_count);
			selectedCardAnchor = cardView ? cardView.anchor : null; restoreCardView(cardView && cardView.card);
			for (const observer of contextObservers) observer();
			if (reset && pendingFocusVersion === current) { client.restoreFocusAnchor(focusAnchor, boardID, cardNodes, refresh, document.activeElement, document.body); pendingFocusAnchor = null; pendingFocusVersion = 0; }
			const positionState = !filtered && !snapshot.has_more && cardTotal === snapshot.board.card_count ? " · position controls ready" : " · position controls require all cards loaded and filters clear";
			selectedMeta.textContent = snapshot.board.state + " board · " + String(cardTotal) + " matching cards loaded · " + String(snapshot.board.card_count) + " total on board" + (snapshot.has_more ? " · more matching available" : "") + positionState;
			stateNode.hidden = true; kanban.setAttribute("aria-busy", "false"); cardList.setAttribute("aria-busy", "false");
			if (!cardTotal) notice(stateNode, "No cards match the current filters.", false);
			loadMoreCards.hidden = !snapshot.has_more || cardTotal >= maxCards;
			loadMoreCards.disabled = false;
		}).catch(() => {
			if (current !== cardRequestVersion) return;
			kanban.setAttribute("aria-busy", "false"); cardList.setAttribute("aria-busy", "false"); kanban.hidden = true; cardList.hidden = true;
			notice(stateNode, "This workboard could not be loaded. Use Refresh to try again.", true);
			loadMoreCards.hidden = true;
			if (reset && pendingFocusVersion === current) { client.restoreFocusAnchor(focusAnchor, boardID, cardNodes, refresh, document.activeElement, document.body); pendingFocusAnchor = null; pendingFocusVersion = 0; }
		});
	}
	window.DarwinWorkboards = Object.freeze({
		context: () => Object.freeze({
			board: currentBoard, card: selectedCard, graphRevision: snapshotGraphRevision,
			cards: Object.freeze((pendingPosition ? client.provisionalPosition(loadedCards, pendingPosition) : loadedCards).map(card => Object.freeze({id: card.id, state: card.state, rank: card.rank, revision: card.revision, criteria_revision: card.criteria_revision, remaining_dependencies: card.remaining_dependencies, current_attempt_id: card.current_attempt_id || "", current_claim_id: card.current_claim_id || "", acceptance_id: card.acceptance_id || "", pause_requested: card.pause_requested, pause_phase: card.pause_phase || "", cancel_requested: card.cancel_requested, supervision: supervisionByCard.get(card.id) || null, provisional: Boolean(card.provisional)}))),
			complete: Boolean(currentBoard && !cardCursor && cardTotal === currentBoard.card_count),
			unfiltered: !appliedFilters.state && !appliedFilters.assignee && !appliedFilters.owner && !appliedFilters.claim
		}),
		previewPosition: plan => { const projected = client.provisionalPosition(loadedCards, plan); if (!projected) return false; pendingPosition = plan; renderPresentation(); return true; },
		refresh: () => { loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true); },
		subscribe: observer => { if (typeof observer !== "function") return () => {}; contextObservers.add(observer); return () => contextObservers.delete(observer); }
	});
	function closeBoardStream() {
		if (boardSource) boardSource.close();
		boardSource = null; streamBoard = "";
	}
	function queueInvalidation(message) {
		if (message) liveStatus.textContent = message;
		if (invalidationTimer || !selectedID) return;
		invalidationTimer = window.setTimeout(() => { invalidationTimer = 0; loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true); }, 120);
	}
	function validBoardEvent(payload, boardID) {
		const changes = ["board_created", "board_revised", "card_changed", "dependency_changed", "claim_changed", "evidence_changed", "acceptance_changed"];
		if (!payload || payload.version !== 1 || payload.kind !== "board.changed" || payload.durability !== "committed" || payload.subject !== boardID ||
			!Number.isSafeInteger(payload.revision) || payload.revision < 1 || !boundedPrintable(payload.cursor, 1, 512) || !payload.data ||
			payload.data.board_id !== boardID || !changes.includes(payload.data.change)) return false;
		const boardOnly = payload.data.change === "board_created" || payload.data.change === "board_revised";
		return boardOnly ? payload.data.card_id === undefined && payload.data.state === undefined :
			idPattern.test(payload.data.card_id) && (payload.data.change !== "card_changed" ? payload.data.state === undefined : payload.data.state === undefined || states.includes(payload.data.state));
	}
	function connectBoard(boardID) {
		if (!idPattern.test(boardID)) { notice(stateNode, "The workboard address is invalid.", true); return; }
		if (streamBoard === boardID && boardSource) return;
		closeBoardStream(); streamBoard = boardID; streamRevision = 0; streamFailures = 0;
		if (typeof EventSource !== "function") { streamBoard = ""; liveStatus.textContent = "Live updates are unavailable in this browser. Use Refresh to retry."; return; }
		let source;
		try { source = new EventSource(base + "/api/v1/workboards/" + encodeURIComponent(boardID) + "/events"); }
		catch (_) { streamBoard = ""; liveStatus.textContent = "Live updates could not connect. Use Refresh to retry."; return; }
		boardSource = source;
		source.addEventListener("board.changed", event => {
			if (boardSource !== source) return;
			let payload;
			try { payload = JSON.parse(event.data); } catch (_) { closeBoardStream(); notice(stateNode, "Live updates returned invalid data. Refresh the board to reconnect.", true); return; }
			if (!validBoardEvent(payload, boardID) || event.lastEventId !== payload.cursor) { closeBoardStream(); notice(stateNode, "Live updates could not be validated. Refresh the board to reconnect.", true); return; }
			if (payload.revision <= streamRevision) return;
			streamRevision = payload.revision; queueInvalidation("Updating after a committed change…");
		});
		source.onopen = () => { if (boardSource === source) { streamFailures = 0; liveStatus.textContent = "Live updates connected."; } };
		source.onerror = () => { if (boardSource === source) { streamFailures++; if (streamFailures >= 8) { closeBoardStream(); liveStatus.textContent = "Live updates stopped after repeated reconnect failures. Use Refresh to reconnect."; return; } liveStatus.textContent = "Reconnecting live updates…"; queueInvalidation(""); } };
	}
	refresh.addEventListener("click", () => { loadBoards("", true); if (selectedID) { closeBoardStream(); connectBoard(selectedID); loadBoard(selectedID, "", true); } });
	function applyCardFilters() {
		const next = readFilters();
		if (!next) { filterStatus.textContent = "Assignee and owner must be an ID, unassigned, or empty for all."; return; }
		appliedFilters = next; filterStatus.textContent = "Filters applied.";
		loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true);
	}
	filterForm.addEventListener("submit", event => { event.preventDefault(); applyCardFilters(); });
	for (const control of [cardStateFilter, assigneeFilter, ownerFilter, claimStateFilter]) control.addEventListener("change", () => filterForm.requestSubmit());
	boardStateFilter.addEventListener("change", () => { if (boardStateFilter.value !== "active" && boardStateFilter.value !== "archived") { notice(listState, "Choose a valid board state.", true); return; } appliedBoardState = boardStateFilter.value; loadBoards("", true); if (selectedID) loadBoard(selectedID, "", true); });
	resetFilters.addEventListener("click", () => { cardStateFilter.value = ""; assigneeFilter.value = ""; ownerFilter.value = ""; claimStateFilter.value = ""; applyCardFilters(); assigneeFilter.focus(); });
	showKanban.addEventListener("click", () => { presentation = "kanban"; renderPresentation(); showKanban.focus(); });
	showList.addEventListener("click", () => { presentation = "list"; renderPresentation(); showList.focus(); });
	loadMoreBoards.addEventListener("click", () => { if (boardCursor) loadBoards(boardCursor, false); });
	loadMoreCards.addEventListener("click", () => { if (selectedID && cardCursor) loadBoard(selectedID, cardCursor, false); });
	loadBoards("", true);
	if (route[1]) {
		try { const boardID = decodeURIComponent(route[1]); if (!idPattern.test(boardID)) throw new Error("invalid board id"); connectBoard(boardID); loadBoard(boardID, "", true); } catch (_) { notice(stateNode, "The workboard address is invalid.", true); }
	}
	window.addEventListener("beforeunload", () => { if (invalidationTimer) window.clearTimeout(invalidationTimer); closeBoardStream(); });
})();
