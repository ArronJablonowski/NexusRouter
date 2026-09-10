"use strict";
(() => {
	const actions = new Set([
		"submit", "resume", "steer", "cancel", "cancel_submission", "record", "revise",
		"approval.allow", "approval.deny", "approval.revoke",
		"board.create", "board.revise", "board.archive",
		"card.create", "card.revise", "card.move", "card.reorder",
		"dependency.add", "dependency.remove", "card.claim", "claim.heartbeat", "claim.recover",
		"criteria.revise", "checkpoint.append", "candidate.submit",
		"acceptance.accept", "acceptance.reject", "card.pause_request", "card.cancel_request",
		"card.cancel_finalize", "card.block", "card.unblock"
	]);
	const subjects = new Set(["chat", "task", "submission", "feedback", "approval", "board", "card"]);
	window.DarwinOperationContract = Object.freeze({
		validAction: value => typeof value === "string" && actions.has(value),
		validSubject: value => typeof value === "string" && subjects.has(value)
	});
})();
