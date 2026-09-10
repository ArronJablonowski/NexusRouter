"use strict";
(function (root, factory) {
	const client = factory();
	if (typeof module === "object" && module.exports) module.exports = client;
	if (root) root.DarwinWorkboardClient = client;
})(typeof window === "object" ? window : null, () => {
	function compareText(left, right) {
		const a = new TextEncoder().encode(left), b = new TextEncoder().encode(right), count = Math.min(a.length, b.length);
		for (let index = 0; index < count; index++) if (a[index] !== b[index]) return a[index] - b[index];
		return a.length - b.length;
	}
	function current(requestGeneration, activeGeneration, requestedBoard, selectedBoard) {
		return requestGeneration === activeGeneration && (requestedBoard === undefined || requestedBoard === selectedBoard);
	}
	function canonical(cards, previous, existingIDs, states) {
		const ids = new Set(existingIDs), ranks = new Map(), stateIndex = new Map(states.map((state, index) => [state, index]));
		let prior = previous || null;
		for (const card of cards) {
			if (!card || ids.has(card.id) || !stateIndex.has(card.state)) return null;
			if (prior) {
				const stateOrder = stateIndex.get(card.state) - stateIndex.get(prior.state), rankOrder = compareText(card.rank, prior.rank);
				if (stateOrder < 0 || stateOrder === 0 && (rankOrder < 0 || rankOrder === 0 && compareText(card.id, prior.id) <= 0)) return null;
			}
			ids.add(card.id); ranks.set(card.state, card.rank); prior = card;
		}
		return Object.freeze({last: prior, ranks});
	}
	function reparent(cards, nodes, targetFor, focused) {
		for (const card of cards) { const node = nodes.get(card.id), target = targetFor(card); if (!node || !target) return false; target.append(node); }
		if (focused && cards.some(card => nodes.get(card.id).contains(focused))) focused.focus({preventScroll: true});
		return true;
	}
	function focusKind(node) {
		if (!node || !node.classList || typeof node.classList.contains !== "function") return null;
		if (node.classList.contains("card-toggle") || node.classList.contains("attempt-toggle")) return Object.freeze({kind: "toggle", action: ""});
		if (node.classList.contains("card-position") && ["up", "down", "ready", "backlog"].includes(node.dataset && node.dataset.position)) return Object.freeze({kind: "position", action: node.dataset.position});
		if (node.classList.contains("card-control") && ["card.pause_request", "card.resume_request", "card.cancel_request"].includes(node.dataset && node.dataset.control)) return Object.freeze({kind: "control", action: node.dataset.control});
		if (node.classList.contains("card-review")) return Object.freeze({kind: "review", action: ""});
		return null;
	}
	function validFocusAnchor(anchor, boardID) {
		return Boolean(anchor && exact(anchor, ["boardID", "cardID", "kind", "action"], []) && anchor.boardID === boardID && id(boardID) && id(anchor.cardID) &&
			(anchor.kind === "toggle" && anchor.action === "" || anchor.kind === "review" && anchor.action === "" || anchor.kind === "position" && ["up", "down", "ready", "backlog"].includes(anchor.action) || anchor.kind === "control" && ["card.pause_request", "card.resume_request", "card.cancel_request"].includes(anchor.action)));
	}
	function refreshFocusAnchor(captured, pending, boardID, currentFocus, body) {
		if (validFocusAnchor(captured, boardID)) return captured;
		if (currentFocus && currentFocus !== body && currentFocus.isConnected !== false) return null;
		return validFocusAnchor(pending, boardID) ? pending : null;
	}
	function captureFocusAnchor(focused, boardID, cardNodes) {
		if (!focused || !id(boardID) || !(cardNodes instanceof Map) || cardNodes.size > 10000) return null;
		const identity = focusKind(focused); if (!identity) return null;
		for (const [cardID, cardNode] of cardNodes) {
			if (!id(cardID) || !cardNode || typeof cardNode.contains !== "function") return null;
			if (cardNode.contains(focused)) return Object.freeze({boardID, cardID, kind: identity.kind, action: identity.action});
		}
		return null;
	}
	function visibleFocusTarget(node, body) {
		if (!node || typeof node.focus !== "function" || node.disabled || node.hidden || node.isConnected === false) return false;
		let current = node, depth = 0;
		while (current && current !== body && depth++ < 32) {
			if (current.hidden) return false;
			current = current.parentElement || current.parentNode || null;
		}
		return depth < 32 && (!body || current === body || typeof body.contains !== "function" || body.contains(node));
	}
	function matchingFocusTarget(anchor, cardNode) {
		if (!cardNode || typeof cardNode.querySelector !== "function" || typeof cardNode.querySelectorAll !== "function") return null;
		if (anchor.kind === "toggle") return cardNode.querySelector(".card-toggle");
		if (anchor.kind === "review") return cardNode.querySelector(".card-review");
		const selector = anchor.kind === "position" ? ".card-position" : ".card-control", nodes = cardNode.querySelectorAll(selector);
		if (!nodes || nodes.length > 8) return null;
		for (const node of nodes) if (focusKind(node) && (anchor.kind === "position" ? node.dataset.position : node.dataset.control) === anchor.action) return node;
		return null;
	}
	function restoreFocusAnchor(anchor, boardID, cardNodes, fallback, currentFocus, body) {
		if (currentFocus && currentFocus !== body && currentFocus.isConnected !== false) return false;
		if (!validFocusAnchor(anchor, boardID) || !(cardNodes instanceof Map) || cardNodes.size > 10000) return false;
		const cardNode = cardNodes.get(anchor.cardID), exactTarget = matchingFocusTarget(anchor, cardNode);
		const cardToggle = cardNode && typeof cardNode.querySelector === "function" ? cardNode.querySelector(".card-toggle") : null;
		const target = visibleFocusTarget(exactTarget, body) ? exactTarget : visibleFocusTarget(cardToggle, body) ? cardToggle : visibleFocusTarget(fallback, body) ? fallback : null;
		if (!target) return false;
		try { target.focus({preventScroll: true}); return true; } catch (_) { return false; }
	}
	function cardViewAnchor(boardID, cardID, expanded) {
		return id(boardID) && id(cardID) && typeof expanded === "boolean" ? Object.freeze({boardID, cardID, expanded}) : null;
	}
	function cardViewTransition(anchor, previousBoardID, boardID, cards, completeUnfiltered) {
		if ((previousBoardID !== "" && !id(previousBoardID)) || !id(boardID) || !Array.isArray(cards) || cards.length > 10000 || typeof completeUnfiltered !== "boolean") return null;
		const ids = new Set(); let match = null;
		for (const card of cards) { if (!card || card.board_id !== boardID || !id(card.id) || ids.has(card.id)) return null; ids.add(card.id); if (anchor && card.id === anchor.cardID) match = card; }
		if (!anchor) return Object.freeze({anchor: null, card: null});
		if (!exact(anchor, ["boardID", "cardID", "expanded"], []) || anchor.boardID !== previousBoardID || !id(anchor.cardID) || typeof anchor.expanded !== "boolean") return null;
		if (previousBoardID !== boardID || completeUnfiltered && !match) return Object.freeze({anchor: null, card: null});
		const retained = cardViewAnchor(anchor.boardID, anchor.cardID, anchor.expanded);
		return Object.freeze({anchor: retained, card: match});
	}
	function freezeIntent(path, body) {
		const freeze = value => { if (value && typeof value === "object" && !Object.isFrozen(value)) { for (const item of Object.values(value)) freeze(item); Object.freeze(value); } return value; };
		return Object.freeze({path, body: freeze(body), encoded: JSON.stringify(body)});
	}
	function mutationResolution(status, receiptValid) {
		if (status >= 200 && status < 300) return receiptValid ? "committed" : "ambiguous";
		return [400, 401, 403, 404, 409, 422].includes(status) ? "definitive" : "ambiguous";
	}
	function captureCurrent(capture, context) {
		if (!capture || capture.action === "board.create") return true;
		const position = capture.action === "card.move" || capture.action === "card.reorder", dependency = capture.action === "dependency.change", control = ["card.pause_request", "card.resume_request", "card.cancel_request"].includes(capture.action), acceptance = capture.action === "acceptance.accept" || capture.action === "acceptance.reject";
		if (!context || !context.board || context.board.id !== capture.boardID || context.board.revision !== capture.boardRevision || position && context.board.layout_revision !== capture.layoutRevision) return false;
		if (position) {
			const card = context.cards && context.cards.find(item => item.id === capture.cardID), anchor = capture.anchorID && context.cards.find(item => item.id === capture.anchorID);
			return Boolean(card && card.revision === capture.cardRevision && card.state === capture.sourceState && (!capture.anchorID || anchor && anchor.state === capture.sourceState && anchor.rank === capture.anchorRank));
		}
		if (control) { const card = context.cards && context.cards.find(item => item.id === capture.cardID); return Boolean(card && card.revision === capture.cardRevision && card.state === capture.sourceState && card.current_claim_id === capture.claimID && card.pause_requested === capture.pauseRequested && card.pause_phase === capture.pausePhase && card.cancel_requested === capture.cancelRequested); }
		if (acceptance) { const card = context.cards && context.cards.find(item => item.id === capture.cardID); return Boolean(card && card.revision === capture.cardRevision && card.state === "review" && card.current_attempt_id === capture.attemptID && !card.current_claim_id && !card.acceptance_id && card.criteria_revision === capture.criteriaRevision); }
		return context.graphRevision === capture.graphRevision && ((!dependency && capture.action !== "card.revise") || context.card && context.card.id === capture.cardID && context.card.revision === capture.cardRevision) &&
			(!dependency || context.complete && context.unfiltered && JSON.stringify(context.card.dependencies) === JSON.stringify(capture.dependencies));
	}
	function positionPlan(context, cardID, direction) {
		if (!context || !context.board || context.board.state !== "active" || !context.complete || !context.unfiltered || !Array.isArray(context.cards) || !id(cardID)) return null;
		const card = context.cards.find(item => item.id === cardID); if (!card) return null;
		const common = {boardID: context.board.id, boardRevision: context.board.revision, layoutRevision: context.board.layout_revision, cardID: card.id, cardRevision: card.revision, sourceState: card.state};
		if (direction === "ready" || direction === "backlog") {
			if (card.state === direction || card.state === "backlog" && direction !== "ready" || card.state === "ready" && direction !== "backlog" || !["backlog", "ready"].includes(card.state) || direction === "ready" && card.remaining_dependencies !== 0 || card.current_claim_id) return null;
			return Object.freeze({...common, action: "card.move", targetState: direction, anchorID: "", anchorRank: ""});
		}
		if (direction !== "up" && direction !== "down") return null;
		const lane = context.cards.filter(item => item.state === card.state), index = lane.findIndex(item => item.id === card.id), anchor = lane[index + (direction === "up" ? -1 : 1)];
		if (index < 0 || !anchor || anchor.id === card.id) return null;
		return Object.freeze({...common, action: "card.reorder", anchorID: anchor.id, anchorRank: anchor.rank, beforeCardID: direction === "up" ? anchor.id : "", afterCardID: direction === "down" ? anchor.id : ""});
	}
	function provisionalPosition(cards, plan) {
		if (!Array.isArray(cards) || cards.length > 10000 || !plan || !["card.move", "card.reorder"].includes(plan.action) || !id(plan.cardID)) return null;
		const ids = new Set();
		for (const card of cards) { if (!card || !id(card.id) || ids.has(card.id)) return null; ids.add(card.id); }
		const card = cards.find(item => item.id === plan.cardID);
		if (!card || card.state !== plan.sourceState || card.revision !== plan.cardRevision) return null;
		const remaining = cards.filter(item => item.id !== card.id), projected = Object.freeze({...card, state: plan.action === "card.move" ? plan.targetState : card.state, provisional: true});
		if (plan.action === "card.reorder") {
			const anchor = remaining.find(item => item.id === plan.anchorID);
			if (!anchor || anchor.state !== card.state || anchor.rank !== plan.anchorRank || Boolean(plan.beforeCardID) === Boolean(plan.afterCardID) || (plan.beforeCardID || plan.afterCardID) !== anchor.id) return null;
			const index = remaining.indexOf(anchor) + (plan.afterCardID ? 1 : 0); remaining.splice(index, 0, projected);
		} else {
			if (!([card.state, plan.targetState].includes("backlog") && [card.state, plan.targetState].includes("ready")) || plan.anchorID) return null;
			let index = plan.targetState === "backlog" ? 0 : remaining.findIndex(item => item.state !== "backlog" && item.state !== "ready");
			for (let cursor = remaining.length - 1; cursor >= 0; cursor--) if (remaining[cursor].state === plan.targetState) { index = cursor + 1; break; }
			if (index < 0) index = remaining.length; remaining.splice(index, 0, projected);
		}
		return Object.freeze(remaining);
	}
	function dependencyPlan(context, capture, mode, dependencyID) {
		if (!captureCurrent(capture, context) || capture.action !== "dependency.change" || !id(dependencyID) || dependencyID === capture.cardID || !Array.isArray(context.cards) || !context.cards.some(card => card.id === dependencyID)) return null;
		const current = capture.dependencies.includes(dependencyID), target = context.cards.find(card => card.id === dependencyID);
		if (mode === "add" && !current && capture.dependencies.length < 64 && (capture.cardState !== "ready" || target.state === "done")) return Object.freeze({action: "dependency.add", dependencyID});
		if (mode === "remove" && current) return Object.freeze({action: "dependency.remove", dependencyID});
		return null;
	}
	function controlPlan(context, cardID, action) {
		if (!context || !context.board || context.board.state !== "active" || !Array.isArray(context.cards) || !id(cardID) || !["card.pause_request", "card.resume_request", "card.cancel_request"].includes(action)) return null;
		const card = context.cards.find(item => item.id === cardID), phase = card && card.pause_phase || "", supervision = card && card.supervision;
		const allowed = supervision && supervision.state === "running" && (action === "card.pause_request" ? supervision.actions.pause_request : action === "card.resume_request" ? supervision.actions.resume_request : supervision.actions.cancel_request);
		if (!card || !allowed || card.pause_requested !== Boolean(phase) || !["in_progress", "blocked"].includes(card.state) || !id(card.current_claim_id) || action === "card.pause_request" && (phase !== "" || card.cancel_requested) || action === "card.resume_request" && (phase !== "acknowledged" || card.cancel_requested) || action === "card.cancel_request" && card.cancel_requested) return null;
		return Object.freeze({action, boardID: context.board.id, boardRevision: context.board.revision, cardID, cardRevision: card.revision, sourceState: card.state, claimID: card.current_claim_id, pauseRequested: card.pause_requested, pausePhase: phase, cancelRequested: card.cancel_requested});
	}
	function acceptancePlan(context, cardID, attempt, action) {
		const digest = value => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
		if (!context || !context.board || context.board.state !== "active" || !Array.isArray(context.cards) || !id(cardID) || !["acceptance.accept", "acceptance.reject"].includes(action) || !attempt || attempt.state !== "review" || attempt.board_id !== context.board.id || attempt.card_id !== cardID || !id(attempt.id) || !Array.isArray(attempt.criteria) || !Array.isArray(attempt.evidence) || attempt.evidence.length > 100 || !attempt.candidate) return null;
		const card = context.cards.find(item => item.id === cardID), candidate = attempt.candidate;
		if (!card || card.state !== "review" || card.current_attempt_id !== attempt.id || card.current_claim_id || card.acceptance_id || card.remaining_dependencies !== 0 || card.criteria_revision !== attempt.criteria_revision || candidate.board_id !== attempt.board_id || candidate.card_id !== cardID || candidate.attempt_id !== attempt.id || !id(candidate.id) || candidate.evidence_count !== attempt.evidence.length || !digest(candidate.digest) || !digest(candidate.evidence_digest) || !digest(attempt.criteria_digest) || !digest(attempt.policy_digest) || candidate.criteria_digest !== attempt.criteria_digest || candidate.policy_digest !== attempt.policy_digest) return null;
		const required = attempt.criteria.filter(item => item && item.required), evidence = attempt.evidence;
		const records = criterion => evidence.filter(item => item && item.criterion_id === criterion.id && item.source === criterion.required_source && (criterion.required_source !== "deterministic" || item.actor_id === criterion.validator_id));
		const canAccept = required.every(criterion => criterion.required_source === "user_feedback" ? !records(criterion).some(item => item.outcome === "failed") : records(criterion).some(item => item.outcome === "passed") && !records(criterion).some(item => item.outcome === "failed"));
		const canReject = required.some(criterion => criterion.required_source === "user_feedback" || records(criterion).some(item => item.outcome === "failed"));
		if (action === "acceptance.accept" && !canAccept || action === "acceptance.reject" && !canReject) return null;
		return Object.freeze({action, boardID: context.board.id, boardRevision: context.board.revision, cardID, cardRevision: card.revision, attemptID: attempt.id, candidateID: candidate.id, criteriaRevision: attempt.criteria_revision, evidenceHeadRevision: attempt.evidence.length, candidateDigest: candidate.digest, criteriaDigest: attempt.criteria_digest, evidenceSetDigest: candidate.evidence_digest, policyDigest: attempt.policy_digest, canAccept, canReject});
	}
	function exact(value, required, optional) { if (!value || typeof value !== "object" || Array.isArray(value)) return false; const keys = Object.keys(value), allowed = new Set([...required, ...optional]); return required.every(key => Object.hasOwn(value, key)) && keys.every(key => allowed.has(key)); }
	function id(value) { return typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value); }
	function printableKey(value) { return typeof value === "string" && value.length >= 16 && value.length <= 128 && /^[\x21-\x7e]+$/.test(value); }
	function time(value) { const parsed = typeof value === "string" ? Date.parse(value) : NaN; return Number.isFinite(parsed) && parsed >= Date.UTC(1970, 0, 1) && parsed < Date.UTC(2261, 0, 1); }
	function wellFormed(value) { for (let index = 0; index < value.length; index++) { const code = value.charCodeAt(index); if (code >= 0xd800 && code <= 0xdbff) { if (++index >= value.length || value.charCodeAt(index) < 0xdc00 || value.charCodeAt(index) > 0xdfff) return false; } else if (code >= 0xdc00 && code <= 0xdfff) return false; } return true; }
	function text(value, max, empty) { if (typeof value !== "string" || !empty && !value.trim() || !wellFormed(value) || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(value)) return false; return new TextEncoder().encode(value).length <= max; }
	function receiptMatches(body, intent) {
		const required = ["version", "board_id", "operation_id", "request_digest", "response_digest", "first_sequence", "last_sequence", "event_count", "transaction_bytes", "board_revision", "outcome", "created_at"], digest = /^[0-9a-f]{64}$/;
		if (!intent || !intent.body || !exact(body, required, ["card_id", "card_revision", "claim_revision"]) || body.version !== 1 || !id(body.board_id) || !printableKey(body.operation_id) || !digest.test(body.request_digest) || !digest.test(body.response_digest) ||
			!Number.isSafeInteger(body.first_sequence) || body.first_sequence < 1 || !Number.isSafeInteger(body.last_sequence) || body.last_sequence < body.first_sequence || !Number.isSafeInteger(body.event_count) || body.event_count < 1 || body.event_count > 128 || body.last_sequence - body.first_sequence + 1 !== body.event_count ||
			!Number.isSafeInteger(body.transaction_bytes) || body.transaction_bytes < 1 || body.transaction_bytes > 1048576 || !Number.isSafeInteger(body.board_revision) || body.board_revision < 1 || body.outcome !== "committed" || !time(body.created_at)) return false;
		const action = intent.body.action, cardAction = ["card.create", "card.revise", "card.move", "card.reorder", "dependency.add", "dependency.remove", "card.pause_request", "card.resume_request", "card.cancel_request", "acceptance.accept", "acceptance.reject"].includes(action), positioned = action === "card.move" || action === "card.reorder", graphChange = action === "dependency.add" || action === "dependency.remove", control = ["card.pause_request", "card.resume_request", "card.cancel_request"].includes(action), acceptance = action === "acceptance.accept" || action === "acceptance.reject", captured = intent.capture || {};
		const expectedBoard = action === "board.create" ? 1 : captured.boardRevision + 1;
		if ((action === "card.revise" || graphChange || control || acceptance ? body.board_revision < expectedBoard : body.board_revision !== expectedBoard) || action !== "board.create" && body.board_id !== intent.body.board_id || cardAction !== Object.hasOwn(body, "card_id") || !cardAction && (body.card_revision !== undefined || body.claim_revision !== undefined) || body.claim_revision !== undefined) return false;
		if (!cardAction || !id(body.card_id) || !Number.isSafeInteger(body.card_revision)) return !cardAction;
		return action === "card.create" ? body.card_revision === 1 : body.card_id === intent.body.card_id && body.card_revision === captured.cardRevision + 1 && (!positioned || body.board_revision === captured.boardRevision + 1);
	}
	function mutationError(body, status) {
		if (!exact(body, ["version", "code", "message", "retryable"], ["current_revision", "retry_after_ms", "subject_type", "subject_id", "current_state", "operation_id"]) || body.version !== 1 || !id(body.code) || !text(body.message, 256, false) || typeof body.retryable !== "boolean" ||
			body.current_revision !== undefined && (!Number.isSafeInteger(body.current_revision) || body.current_revision < 0) || body.retry_after_ms !== undefined && (!Number.isSafeInteger(body.retry_after_ms) || body.retry_after_ms < 1 || body.retry_after_ms > 60000) ||
			(body.subject_type === undefined) !== (body.subject_id === undefined) || body.subject_type !== undefined && (!["chat", "task", "submission", "feedback", "approval", "board", "card", "claim"].includes(body.subject_type) || !id(body.subject_id)) || body.current_state !== undefined && !id(body.current_state) || body.operation_id !== undefined && !id(body.operation_id)) return null;
		const expected = {400: ["invalid_workboard_request", false], 401: ["unauthorized", false], 403: ["request_denied", false], 404: ["not_found", false], 409: ["revision_conflict", true], 422: ["workboard_rejected", false]}[status];
		return Object.freeze({body, definitive: Boolean(expected && body.code === expected[0] && body.retryable === expected[1]), operationID: body.operation_id || ""});
	}
	function acknowledgeAllowed(intent, operationsReady, operationReadFailed, unresolvedCount) { return Boolean(intent && !intent.operationID && intent.reconciledClean && operationsReady && !operationReadFailed && unresolvedCount === 0); }
	return Object.freeze({acceptancePlan, acknowledgeAllowed, canonical, captureCurrent, captureFocusAnchor, cardViewAnchor, cardViewTransition, compareText, controlPlan, current, dependencyPlan, freezeIntent, mutationError, mutationResolution, positionPlan, provisionalPosition, receiptMatches, reparent, refreshFocusAnchor, restoreFocusAnchor});
});
