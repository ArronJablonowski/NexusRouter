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
		const position = capture.action === "card.move" || capture.action === "card.reorder";
		if (!context || !context.board || context.board.id !== capture.boardID || context.board.revision !== capture.boardRevision || position && context.board.layout_revision !== capture.layoutRevision) return false;
		if (position) {
			const card = context.cards && context.cards.find(item => item.id === capture.cardID), anchor = capture.anchorID && context.cards.find(item => item.id === capture.anchorID);
			return Boolean(card && card.revision === capture.cardRevision && card.state === capture.sourceState && (!capture.anchorID || anchor && anchor.state === capture.sourceState && anchor.rank === capture.anchorRank));
		}
		return context.graphRevision === capture.graphRevision && (capture.action !== "card.revise" || context.card && context.card.id === capture.cardID && context.card.revision === capture.cardRevision);
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
		const action = intent.body.action, cardAction = ["card.create", "card.revise", "card.move", "card.reorder"].includes(action), positioned = action === "card.move" || action === "card.reorder", captured = intent.capture || {};
		const expectedBoard = action === "board.create" ? 1 : captured.boardRevision + 1;
		if ((action === "card.revise" ? body.board_revision < expectedBoard : body.board_revision !== expectedBoard) || action !== "board.create" && body.board_id !== intent.body.board_id || cardAction !== Object.hasOwn(body, "card_id") || !cardAction && (body.card_revision !== undefined || body.claim_revision !== undefined) || body.claim_revision !== undefined) return false;
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
	return Object.freeze({acknowledgeAllowed, canonical, captureCurrent, compareText, current, freezeIntent, mutationError, mutationResolution, positionPlan, receiptMatches, reparent});
});
