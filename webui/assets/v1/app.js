"use strict"; (() => {
	const base = document.body.dataset.basePath || "";
	const connection = document.querySelector("#connection-state"), list = document.querySelector("#chat-list");
	const listState = document.querySelector("#chat-list-state"), chatCount = document.querySelector("#chat-count"), loadMore = document.querySelector("#load-more");
	const title = document.querySelector("#chat-title"), chatState = document.querySelector("#chat-state"), transcript = document.querySelector("#transcript");
	const transcriptState = document.querySelector("#transcript-state"), loadMoreMessages = document.querySelector("#load-more-messages");
	const provisional = document.querySelector("#provisional"), streamAnnouncement = document.querySelector("#stream-announcement");
	const taskControls = document.querySelector("#task-controls"), steeringControl = document.querySelector("#steering-control"), steeringText = document.querySelector("#steering-text");
	const steerTask = document.querySelector("#steer-task"), cancelTask = document.querySelector("#cancel-task"), approvalPanel = document.querySelector("#approval-panel");
	const approvalState = document.querySelector("#approval-state"), approvalList = document.querySelector("#approval-list"), approvalCount = document.querySelector("#approval-count");
	const feedbackPanel = document.querySelector("#feedback-panel"), feedbackSummary = document.querySelector("#feedback-summary");
	const attemptCostLabel = document.querySelector("#attempt-cost-label"), attemptCost = document.querySelector("#attempt-cost"), attemptCostHelp = document.querySelector("#attempt-cost-help");
	const feedbackAccepted = document.querySelector("#feedback-accepted"), feedbackRejected = document.querySelector("#feedback-rejected");
	const composer = document.querySelector("#composer"), composerLabel = document.querySelector("#composer-label"), composerText = document.querySelector("#composer-text");
	const composerHelp = document.querySelector("#composer-help"), composerCount = document.querySelector("#composer-count"), composerError = document.querySelector("#composer-error");
	const sendMessage = document.querySelector("#send-message"), newChat = document.querySelector("#new-chat"), mutationState = document.querySelector("#mutation-state");
	const reconcile = document.querySelector("#reconcile"), acknowledgeUnresolved = document.querySelector("#acknowledge-unresolved");
	const approvalDialog = document.querySelector("#approval-dialog"), approvalDialogMeta = document.querySelector("#approval-dialog-meta");
	const approvalDialogScope = document.querySelector("#approval-dialog-scope"), approvalDialogPrompt = document.querySelector("#approval-dialog-prompt");
	const approvalDeny = document.querySelector("#approval-deny"), approvalAllow = document.querySelector("#approval-allow"), approvalRevoke = document.querySelector("#approval-revoke");
	const approvalClose = document.querySelector("#approval-close"), approvalReconcile = document.querySelector("#approval-reconcile"), approvalAcknowledge = document.querySelector("#approval-acknowledge");
	const pageLimit = 25;
	const maxChats = 100;
	const historyPageLimit = 100;
	const maxMessages = 500;
	const maxText = 1 << 20;
	const maxProvisionalTasks = 16;
	const maxProvisionalTaskText = 256 << 10;
	const maxProvisionalText = 1 << 20;
	const maxApprovals = 25;
	const maxApprovalPrompt = 16 << 10;
	const maxApprovalScope = 4096, maxApprovalProposal = 128 << 10;
	const maxOperationItems = 25;
	const maxOperationScan = 100;
	const maxSubmissionPolls = 60;
	const presentationID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
	let csrfToken = "", nextCursor = "", selectedChat = "", eventRevision = 0, source = null;
	let loadingPage = false, loadingHistory = false, pendingHistoryReconcile = false, historyRequest = 0;
	let chatTotal = 0, historyCursor = "", historyHead = 0, lastMessageRevision = 0, historyNeedsReset = false;
	const messageIDs = new Set();
	const provisionalTasks = new Map();
	let provisionalTextSize = 0, selectedControls = null, selectedTaskID = "", feedbackContext = null, taskContextUnavailable = false;
	let queuedSubmissionID = "", queuedSubmissionCanCancel = false, pendingIntent = null, unresolvedOperations = [];
	let operationsReady = false, operationReadFailed = false, activeApproval = null, approvalOpener = null;
	let composing = false, announcementTimer = 0, submissionPollTimer = 0, submissionPollCount = 0;
	let submissionShouldNavigate = false, operationRequest = 0;
	window.NexusSession = Object.freeze({
		csrfHeader: () => csrfToken ? {"X-Darwin-CSRF": csrfToken} : {}
	});
	function element(name, className, value) { const node = document.createElement(name); if (className) node.className = className; if (value !== undefined) node.textContent = value; return node; }
	function boundedText(value) { return typeof value === "string" && value.length <= maxText ? value : ""; }
	function textBytes(value) { try { return new TextEncoder().encode(value).length; } catch (_) { return maxText + 1; } }
	function showNotice(node, message, failed) { node.textContent = message; node.classList.toggle("error", Boolean(failed)); node.hidden = false; }
	function requestJSON(path) {
		return fetch(base + path, {credentials: "same-origin", cache: "no-store", headers: {"Accept": "application/json"}}).then(response => {
			if (!response.ok) throw new Error("request unavailable");
			return response.json();
		});
	}
	function validControls(value) {
		if (!value || value.version !== 1 || typeof value !== "object" || !presentationID.test(value.task_id) || !Number.isSafeInteger(value.revision) || value.revision < 1) return null;
		for (const name of ["can_resume", "can_steer", "can_cancel", "can_feedback"]) if (typeof value[name] !== "boolean") return null;
		return Object.freeze({
			taskID: value.task_id, revision: value.revision, canResume: value.can_resume, canSteer: value.can_steer,
			canCancel: value.can_cancel, canFeedback: value.can_feedback
		});
	}
	function setBusy(value) {
		for (const node of [composer, taskControls, feedbackPanel, approvalPanel]) node.setAttribute("aria-busy", value ? "true" : "false");
		approvalDialog.setAttribute("aria-busy", value ? "true" : "false");
		updateControls();
	}
	function updateControls() {
		const blocked = Boolean(pendingIntent) || unresolvedOperations.length > 0 || !operationsReady || !csrfToken || Boolean(selectedChat && taskContextUnavailable);
		const followUp = selectedChat && selectedControls && selectedControls.canResume;
		composerLabel.textContent = selectedChat ? "Follow up in this chat" : "Start a new chat";
		sendMessage.textContent = selectedChat ? "Send follow-up" : "Start chat";
		composerHelp.textContent = selectedChat && !followUp ? "The server has not marked this chat eligible for a follow-up." : "Enter sends. Shift+Enter adds a new line.";
		composerText.disabled = blocked || Boolean(selectedChat && !followUp);
		sendMessage.disabled = blocked || Boolean(selectedChat && !followUp);
		newChat.hidden = !selectedChat;
		newChat.disabled = blocked;
		const task = selectedControls;
		taskControls.hidden = !(queuedSubmissionID && queuedSubmissionCanCancel) && (!task || (!task.canSteer && !task.canCancel));
		steeringControl.hidden = !task || !task.canSteer;
		steeringText.disabled = blocked || !task || !task.canSteer;
		steerTask.disabled = blocked || !task || !task.canSteer;
		steerTask.hidden = !task || !task.canSteer;
		cancelTask.hidden = !(queuedSubmissionID && queuedSubmissionCanCancel) && (!task || !task.canCancel);
		cancelTask.textContent = queuedSubmissionID ? "Cancel queued submission" : "Cancel task";
		cancelTask.disabled = blocked || (queuedSubmissionID ? !queuedSubmissionCanCancel : !task || !task.canCancel);
		const mayFeedback = Boolean(task && task.canFeedback && feedbackContext && feedbackContext.feedbackAllowed);
		feedbackPanel.hidden = !task || !feedbackContext;
		attemptCost.disabled = blocked || !mayFeedback;
		feedbackAccepted.disabled = blocked || !mayFeedback;
		feedbackRejected.disabled = blocked || !mayFeedback;
		approvalAllow.disabled = blocked;
		approvalDeny.disabled = blocked;
		approvalRevoke.disabled = blocked;
		approvalClose.disabled = blocked;
		approvalPanel.hidden = !task;
	}
	function idempotencyKey() {
		const bytes = new Uint8Array(18);
		window.crypto.getRandomValues(bytes);
		return "web-" + Array.from(bytes, value => value.toString(16).padStart(2, "0")).join("");
	}
	function safeError(body) {
		if (!body || body.version !== 1 || typeof body !== "object" || typeof body.code !== "string" || !presentationID.test(body.code) ||
			typeof body.message !== "string" || body.message.length > 256 || typeof body.retryable !== "boolean" ||
			(body.current_revision !== undefined && (!Number.isSafeInteger(body.current_revision) || body.current_revision < 0)) ||
			(body.operation_id !== undefined && !presentationID.test(body.operation_id))) return null;
		let message = body.message || stateLabel(body.code);
		if (body.current_revision !== undefined) message += " Current revision: " + String(body.current_revision) + ".";
		message += body.retryable ? " Reconcile before trying again." : " The request was not accepted.";
		return Object.freeze({message, operationID: body.operation_id || "", retryable: body.retryable});
	}
	function validMutationReceipt(body, request) {
		if (!body || body.version !== 1 || !presentationID.test(body.operation_id)) return false;
		switch (request.action) {
		case "submit":
		case "resume":
			return presentationID.test(body.submission_id) && ["queued", "running", "completed", "failed", "canceled"].includes(body.state) &&
				(body.chat_id === undefined || presentationID.test(body.chat_id)) && (body.task_id === undefined || presentationID.test(body.task_id)) &&
				Boolean(body.chat_id) === Boolean(body.task_id) && Boolean(body.revision) === Boolean(body.task_id) &&
				(body.revision === undefined || Number.isSafeInteger(body.revision) && body.revision >= 1) &&
				(!["running", "completed"].includes(body.state) || Boolean(body.task_id));
		case "steer":
			return presentationID.test(body.id) && body.task_id === request.task_id && ["pending", "applied"].includes(body.state) &&
				!Number.isNaN(Date.parse(body.created_at)) && (body.applied_revision === undefined || Number.isSafeInteger(body.applied_revision) && body.applied_revision >= 1) &&
				(body.state === "pending" ? body.applied_revision === undefined : body.applied_revision !== undefined);
		case "cancel":
			return body.target_kind === "task" && body.target_id === request.task_id && typeof body.requested === "boolean" &&
			["running", "completed", "failed", "canceled"].includes(body.state) && Number.isSafeInteger(body.revision) && body.revision >= 1;
		case "cancel_submission":
			return body.target_kind === "submission" && body.target_id === request.submission_id && typeof body.requested === "boolean" &&
			["queued", "running", "completed", "failed", "canceled"].includes(body.state);
		case "record":
		case "revise":
			return body.task_id === request.task_id && presentationID.test(body.feedback_id) && Number.isSafeInteger(body.revision) && body.revision >= 1 &&
			["recorded", "revised"].includes(body.state) && body.accepted === request.accepted && body.evidence_class === "subjective" && body.source === "user_feedback";
		case "allow":
		case "deny":
		case "revoke":
			return body.task_id === request.task_id && body.approval_id === request.approval_id && Number.isSafeInteger(body.revision) && body.revision >= 2 &&
				["approved", "denied", "revoked", "consumed"].includes(body.state) && !Number.isNaN(Date.parse(body.decided_at));
		default:
			return false;
		}
	}
	function showMutation(message, canReconcile, canAcknowledge) {
		mutationState.textContent = message;
		reconcile.hidden = !canReconcile;
		acknowledgeUnresolved.hidden = !canAcknowledge;
		approvalReconcile.hidden = !activeApproval || !canReconcile;
		approvalAcknowledge.hidden = !activeApproval || !canAcknowledge;
	}
	function acknowledgeOutcome() {
		if ((!pendingIntent && unresolvedOperations.length === 0 && !operationReadFailed) || !window.confirm("Acknowledge that this outcome is unresolved without retrying the request?")) return;
		pendingIntent = null;
		unresolvedOperations = [];
		operationsReady = true;
		operationReadFailed = false;
		showMutation("Unresolved outcome acknowledged. No request was replayed.", false, false);
		setBusy(false);
		if (!approvalDialog.hidden) closeApproval();
	}
	function validOperation(item) {
		const createdAt = Date.parse(item && item.created_at);
		const updatedAt = Date.parse(item && item.updated_at);
		if (!item || item.version !== 1 || !presentationID.test(item.operation_id) ||
			!window.NexusOperationContract.validAction(item.action) ||
			!["pending", "committed", "rejected"].includes(item.state) || !Number.isFinite(createdAt) || !Number.isFinite(updatedAt) || updatedAt < createdAt) return null;
		const hasSubject = item.subject_type !== undefined || item.subject_id !== undefined;
		if (hasSubject && (!window.NexusOperationContract.validSubject(item.subject_type) || !presentationID.test(item.subject_id))) return null;
		if (item.state === "committed" && !hasSubject) return null;
		return Object.freeze({operationID: item.operation_id, action: item.action, state: item.state,
			subjectType: hasSubject ? item.subject_type : "", subjectID: hasSubject ? item.subject_id : ""});
	}
	function validSubmissionStatus(body, submissionID) {
		const createdAt = Date.parse(body && body.created_at);
		const updatedAt = Date.parse(body && body.updated_at);
		if (!body || body.version !== 1 || body.submission_id !== submissionID || !["queued", "running", "completed", "failed", "canceled"].includes(body.state) ||
			typeof body.can_cancel !== "boolean" || !Number.isFinite(createdAt) || !Number.isFinite(updatedAt) || updatedAt < createdAt) return null;
		const linked = body.chat_id !== undefined || body.task_id !== undefined || body.revision !== undefined;
		if (linked && (!presentationID.test(body.chat_id) || !presentationID.test(body.task_id) || !Number.isSafeInteger(body.revision) || body.revision < 1)) return null;
		if ((body.state === "completed" && !linked) || (["completed", "failed", "canceled"].includes(body.state) && body.can_cancel)) return null;
		return Object.freeze({submissionID, state: body.state, chatID: linked ? body.chat_id : "", taskID: linked ? body.task_id : "", revision: linked ? body.revision : 0, canCancel: body.can_cancel});
	}
	function stopSubmissionPolling() { if (submissionPollTimer) window.clearTimeout(submissionPollTimer); submissionPollTimer = 0; }
	function observeSubmission(submissionID, resetCount, navigate) {
		if (!presentationID.test(submissionID)) return;
		stopSubmissionPolling();
		if (resetCount) {
			submissionPollCount = 0;
			submissionShouldNavigate = Boolean(navigate);
		}
		queuedSubmissionID = submissionID;
		queuedSubmissionCanCancel = false;
		updateControls();
		requestJSON("/api/v1/submissions/" + encodeURIComponent(submissionID)).then(body => {
			if (queuedSubmissionID !== submissionID) return;
			const status = validSubmissionStatus(body, submissionID);
			if (!status) throw new Error("invalid submission status");
			queuedSubmissionCanCancel = status.canCancel;
			if (status.chatID) {
				queuedSubmissionID = "";
				queuedSubmissionCanCancel = false;
				showMutation("Submission " + stateLabel(status.state) + ".", false, false);
				if (!selectedChat && submissionShouldNavigate) selectChat(status.chatID, status.state);
				else if (selectedChat === status.chatID) loadHistory(status.chatID, "", true, false);
				refreshChats();
				return;
			}
			if (["completed", "failed", "canceled"].includes(status.state)) {
				queuedSubmissionID = "";
				queuedSubmissionCanCancel = false;
				showMutation("Submission " + stateLabel(status.state) + ".", false, false);
				updateControls();
				refreshChats();
				return;
			}
			showMutation("Submission " + stateLabel(status.state) + ". Checking for a linked chat…", true, false);
			updateControls();
			if (++submissionPollCount < maxSubmissionPolls) submissionPollTimer = window.setTimeout(() => observeSubmission(submissionID, false, false), 2000);
		}).catch(() => {
			if (queuedSubmissionID === submissionID) {
				showMutation("Submission status could not be read. Check current status before acting.", true, false);
				updateControls();
				if (++submissionPollCount < maxSubmissionPolls) submissionPollTimer = window.setTimeout(() => observeSubmission(submissionID, false, false), 2000);
			}
		});
	}
	function finishOperationScan(items, successMessage) {
		unresolvedOperations = items.filter(item => item.state === "pending");
		operationsReady = true;
		const exact = pendingIntent && pendingIntent.operationID ? items.find(item => item.operationID === pendingIntent.operationID) : null;
		const exactResolved = exact && ["committed", "rejected"].includes(exact.state);
		const approvalResolved = exactResolved && exact.action.startsWith("approval.");
		if (exactResolved) {
			pendingIntent = null;
			setBusy(false);
		}
		if (approvalResolved && activeApproval) {
			closeApproval();
			loadApprovals();
		}
		const submission = queuedSubmissionID ? items.find(item => item.state === "committed" && item.subjectType === "submission" && item.subjectID === queuedSubmissionID) : items.find(item => item.state === "committed" && item.subjectType === "submission");
		if (submission) observeSubmission(submission.subjectID, true, false);
		if (unresolvedOperations.length) showMutation("A recent operation has an unresolved outcome. Check status or explicitly acknowledge it before acting.", true, true);
		else if (pendingIntent) showMutation("Outcome remains unknown. Check status or explicitly acknowledge it before acting.", true, true);
		else if (exact && exact.state === "rejected") showMutation("The operation was rejected. No request was replayed.", false, false);
		else if (!submission) showMutation(successMessage || "Committed operation status reconciled.", false, false);
		updateControls();
	}
	function readOperationPage(after, items, operationIDs, requestID, successMessage) {
		const pageLimit = Math.min(maxOperationItems, maxOperationScan - items.length);
		if (pageLimit < 1) throw new Error("operation scan limit reached");
		const query = new URLSearchParams({limit: String(pageLimit)});
		if (after) query.set("after", after);
		requestJSON("/api/v1/operations?" + query.toString()).then(body => {
			if (requestID !== operationRequest) return;
			if (!body || body.version !== 1 || !Array.isArray(body.items) || body.items.length > pageLimit ||
				typeof body.has_more !== "boolean" || typeof body.next_cursor !== "string" ||
				body.has_more !== Boolean(body.next_cursor) || body.has_more && body.items.length === 0 ||
				body.next_cursor && !presentationID.test(body.next_cursor)) throw new Error("invalid operation page");
			const page = body.items.map(validOperation);
			if (page.some(item => !item || operationIDs.has(item.operationID))) throw new Error("invalid operation");
			for (const item of page) {
				operationIDs.add(item.operationID);
				items.push(item);
			}
			if (body.has_more) {
				if (items.length >= maxOperationScan) throw new Error("operation scan limit reached");
				readOperationPage(body.next_cursor, items, operationIDs, requestID, successMessage);
				return;
			}
			finishOperationScan(items, successMessage);
		}).catch(() => {
			if (requestID !== operationRequest) return;
			operationsReady = false;
			operationReadFailed = true;
			showMutation(items.length >= maxOperationScan ? "Recent operation history exceeds the safe reconciliation limit. Actions remain blocked." : "Recent operation status could not be read. Actions remain blocked.", true, true);
			updateControls();
		});
	}
	function checkRecentOperations(successMessage) {
		operationsReady = false;
		operationReadFailed = false;
		updateControls();
		const requestID = ++operationRequest;
		readOperationPage("", [], new Set(), requestID, successMessage);
	}
	function mutate(path, payload, onCommitted) {
		if (pendingIntent || unresolvedOperations.length > 0 || !operationsReady || !csrfToken) return;
		let key;
		try { key = idempotencyKey(); } catch (_) {
			showMutation("Secure request identity is unavailable.", false);
			return;
		}
		const request = Object.freeze({...payload, idempotency_key: key});
		const intent = Object.freeze({path, body: JSON.stringify(request), key});
		pendingIntent = intent;
		setBusy(true);
		showMutation("Request in progress…", false);
		fetch(base + path, {
			method: "POST", credentials: "same-origin", cache: "no-store",
			headers: {"Accept": "application/json", "Content-Type": "application/json", ...window.NexusSession.csrfHeader()},
			body: intent.body
		}).then(async response => {
			let body = null;
			try { body = await response.json(); } catch (_) {
				if (response.ok) throw new Error("ambiguous response");
			}
			if (!response.ok) {
				const failure = safeError(body);
				if (response.status >= 500 || response.status === 408) {
					if (failure && failure.operationID) pendingIntent = Object.freeze({...intent, operationID: failure.operationID});
					throw new Error("ambiguous response");
				}
				pendingIntent = null;
				showMutation(failure ? failure.message : "The request was rejected.", response.status === 409, false);
				setBusy(false);
				if (activeApproval && intent.path.includes("/approvals/")) approvalClose.textContent = "Close without deciding";
				if (response.status === 409) reconcileCurrent(failure ? failure.message : "The request conflicted with current state.");
				return;
			}
			if (!validMutationReceipt(body, request)) throw new Error("ambiguous response");
			pendingIntent = null;
			showMutation("Request accepted. Checking committed state…", false);
			setBusy(false);
			onCommitted(body);
		}).catch(() => {
			if (!pendingIntent || pendingIntent.key !== intent.key) return;
			showMutation("Outcome unknown. Check current status before taking another action.", true, true);
			setBusy(true);
		});
	}
	function formatTime(value) { const date = new Date(value); return Number.isNaN(date.getTime()) ? "Unknown time" : date.toLocaleString(); }
	function stateLabel(value) { return typeof value === "string" && value ? value.replaceAll("_", " ") : "unknown"; }
	const chatDescription = id => window.NexusChatDescriptions.get(id);
	function renderChat(item) {
		if (!item || item.version !== 1 || typeof item.chat_id !== "string" || !item.chat_id || item.chat_id.length > 512) return false;
		const existing = [...list.children].find(row => row.firstElementChild?.dataset.chatId === item.chat_id);
		const row = existing || element("li");
		const button = existing ? row.firstElementChild : element("button");
		button.type = "button";
		button.dataset.chatId = item.chat_id;
		button.setAttribute("aria-current", item.chat_id === selectedChat ? "true" : "false");
		window.NexusChatDescriptions.decorate(button, item);
		if (item.chat_id === selectedChat) setTaskState(item.state);
		button.dataset.state = item.state;
		if (!existing) button.addEventListener("click", () => selectChat(item.chat_id, button.dataset.state));
		row.append(button);
		list.append(row);
		return true;
	}
	function loadChats(after, reset = false) {
		if (loadingPage || !reset && chatTotal >= maxChats) return;
		loadingPage = true;
		loadMore.disabled = true;
		if (!after && !list.children.length) showNotice(listState, "Loading chats…", false);
		const query = new URLSearchParams({limit: String(pageLimit)});
		if (after) query.set("after", after);
		requestJSON("/api/v1/chats?" + query.toString()).then(page => {
			if (!page || page.version !== 1 || !Array.isArray(page.items) || page.items.length > pageLimit) throw new Error("invalid chat page");
			const oldScroll = list.scrollTop;
			if (reset) { const ids = new Set(page.items.map(item => item.chat_id)); for (const row of [...list.children]) if (!ids.has(row.firstElementChild?.dataset.chatId)) row.remove(); chatTotal = 0; nextCursor = ""; }
			let added = 0;
			for (const item of page.items.slice(0, maxChats - chatTotal)) if (renderChat(item)) added++;
			chatTotal += added;
			list.scrollTop = oldScroll;
			nextCursor = page.has_more && typeof page.next_cursor === "string" && chatTotal < maxChats ? page.next_cursor : "";
			chatCount.textContent = String(chatTotal);
			listState.hidden = chatTotal > 0;
			if (chatTotal === 0) showNotice(listState, "No chats yet.", false);
			loadMore.hidden = !nextCursor;
		}).catch(() => {
			showNotice(listState, chatTotal ? "Could not load more chats." : "Chats could not be loaded.", true);
		}).finally(() => {
			loadingPage = false;
			loadMore.disabled = false;
		});
	}
	function messageText(message) { return !message || typeof message !== "object" ? "" : boundedText(message.text); }
	function validHistoryMessage(message, afterRevision, seen) {
		return message && typeof message === "object" && typeof message.id === "string" && message.id && message.id.length <= 512 &&
			(message.role === "user" || message.role === "assistant") && typeof message.text === "string" && message.text && message.text.length <= maxText &&
			Number.isSafeInteger(message.revision) && message.revision > afterRevision && Number.isSafeInteger(message.source_revision) &&
			message.source_revision > 0 && message.source_revision <= historyHead && !seen.has(message.id);
	}
	function appendMessage(message) {
		if (transcript.children.length < maxMessages) window.NexusChatRender.messages(transcript, [message], false);
	}

	function applyHistoryPage(body, reset) {
		if (!body || body.version !== 1 || body.chat_id !== selectedChat || typeof body.task_id !== "string" || !body.task_id || !Array.isArray(body.messages) || body.messages.length > historyPageLimit ||
			!Number.isSafeInteger(body.head_revision) || body.head_revision < 1 || typeof body.has_more !== "boolean" ||
			body.has_more !== (typeof body.next_cursor === "string" && body.next_cursor !== "" && body.next_cursor.length <= 4096)) throw new Error("invalid transcript");
		if (!reset && body.head_revision !== historyHead) throw new Error("transcript changed during pagination");
		historyNeedsReset = false;
		if (reset) historyHead = body.head_revision;
		if (reset) {
			selectedTaskID = body.task_id;
			loadTaskContext(body.task_id);
		}
		let revision = reset ? 0 : lastMessageRevision;
		const seen = reset ? new Set() : new Set(messageIDs);
		for (const message of body.messages) {
			if (!validHistoryMessage(message, revision, seen)) throw new Error("invalid transcript message");
			revision = message.revision;
			seen.add(message.id);
		}
		if (reset) {
			messageIDs.clear();
			lastMessageRevision = 0;
		}
		const remaining = maxMessages - (reset ? 0 : transcript.children.length);
		const truncatedPage = body.messages.length > remaining;
		window.NexusChatRender.messages(transcript, body.messages.slice(0, remaining), reset);
		for (const message of body.messages.slice(0, remaining)) {
			messageIDs.add(message.id);
			lastMessageRevision = message.revision;
		}
		const capped = truncatedPage || (body.has_more && transcript.children.length >= maxMessages);
		historyCursor = body.has_more && !capped ? body.next_cursor : "";
		loadMoreMessages.hidden = !historyCursor;
		if (transcript.children.length === 0) showNotice(transcriptState, "This chat has no committed messages.", false);
		else if (capped) showNotice(transcriptState, "Transcript display limit reached.", false);
		else transcriptState.hidden = true;
	}
	function loadHistory(chatID, after, reset, reconnect) {
		if (!chatID) return;
		if (loadingHistory) {
			if (reset) pendingHistoryReconcile = true;
			return;
		}
		loadingHistory = true;
		const requestID = ++historyRequest;
		loadMoreMessages.disabled = true;
		loadMoreMessages.textContent = "Loading…";
		if (reset && !transcript.children.length) showNotice(transcriptState, "Loading committed transcript…", false);
		const query = new URLSearchParams({limit: String(historyPageLimit)});
		if (after) query.set("after", after);
		requestJSON("/api/v1/chats/" + encodeURIComponent(chatID) + "/messages?" + query.toString()).then(body => {
			if (selectedChat !== chatID) return;
			applyHistoryPage(body, reset);
			if (reconnect) connect(chatID);
		}).catch(() => {
			if (selectedChat === chatID) {
				showNotice(transcriptState, reset ? "This transcript could not be loaded." : "More messages could not be loaded.", true);
				historyNeedsReset = true;
				loadMoreMessages.hidden = false;
			}
		}).finally(() => {
			if (requestID !== historyRequest) return;
			loadingHistory = false;
			loadMoreMessages.disabled = false;
			loadMoreMessages.textContent = historyNeedsReset ? "Retry transcript" : "Load more messages";
			if (pendingHistoryReconcile && selectedChat === chatID) {
				pendingHistoryReconcile = false;
				loadHistory(chatID, "", true, false);
			}
		});
	}
	function setTaskState(value) { const state = stateLabel(value); chatState.textContent = state; chatState.className = "state-pill state-" + state.replaceAll(" ", "-"); if (!pendingIntent && ["completed", "failed", "canceled"].includes(value) && /^Submission /.test(mutationState.textContent)) showMutation("Task " + state + ".", false, false); }
	function validEvidence(item, expectedClass) {
		const sources = {objective: ["deterministic", "tool_result"], subjective: ["user_feedback"], advisory: ["llm_judge"]};
		return item && typeof item === "object" && item.class === expectedClass && sources[expectedClass].includes(item.source) &&
			["accepted", "rejected", "abstained"].includes(item.outcome) &&
			Number.isSafeInteger(item.reference_count) && item.reference_count >= 1 && item.reference_count <= 1024;
	}
	function parseFeedbackContext(body, taskID) {
		if (!body || body.version !== 1 || body.task_id !== taskID || !Number.isSafeInteger(body.revision) || body.revision < 1 ||
			!Array.isArray(body.objective) || body.objective.length > 32 || typeof body.feedback_allowed !== "boolean" ||
			(body.feedback_id !== undefined && typeof body.feedback_id !== "string") || (body.feedback_id && !presentationID.test(body.feedback_id)) ||
			(body.denial_code !== undefined && (typeof body.denial_code !== "string" || body.denial_code && !presentationID.test(body.denial_code))) ||
			body.feedback_allowed === Boolean(body.denial_code)) return null;
		if (body.objective.some(item => !validEvidence(item, "objective"))) return null;
		if (body.subjective !== undefined && !validEvidence(body.subjective, "subjective")) return null;
		if (body.advisory !== undefined && !validEvidence(body.advisory, "advisory")) return null;
		if (Boolean(body.feedback_id) !== Boolean(body.subjective)) return null;
		return Object.freeze({taskID, revision: body.revision, objective: body.objective.slice(), subjective: body.subjective || null,
			advisory: body.advisory || null, feedbackID: body.feedback_id || "", feedbackAllowed: body.feedback_allowed});
	}
	function renderFeedbackContext(context) {
		feedbackSummary.replaceChildren();
		const groups = [["Objective", context.objective], ["Subjective", context.subjective ? [context.subjective] : []], ["Advisory", context.advisory ? [context.advisory] : []]];
		for (const [label, items] of groups) {
			const row = element("p", "help", label + ": " + (items.length ? items.map(item => stateLabel(item.outcome) + " (" + item.source.replaceAll("_", " ") + ", " + String(item.reference_count) + " reference" + (item.reference_count === 1 ? "" : "s") + ")").join(", ") : "none recorded"));
			feedbackSummary.append(row);
		}
		const revise = Boolean(context.feedbackID);
		attemptCostLabel.hidden = revise;
		attemptCost.hidden = revise;
		attemptCostHelp.hidden = revise;
		feedbackAccepted.textContent = revise ? "Revise as accepted" : "Record accepted";
		feedbackRejected.textContent = revise ? "Revise as rejected" : "Record rejected";
	}
	function loadTaskContext(taskID) {
		window.NexusInspector.loadTask(taskID);
		requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/controls").then(body => {
			if (selectedTaskID !== taskID) return;
			const controls = validControls(body);
			if (!controls || controls.taskID !== taskID) throw new Error("invalid task controls");
			taskContextUnavailable = false;
			const changed = JSON.stringify(selectedControls) !== JSON.stringify(controls);
			if (changed) { selectedControls = controls; loadApprovals(); } updateControls();
		}).catch(() => { if (selectedTaskID === taskID) { taskContextUnavailable = true; updateControls(); } });
		requestJSON("/api/v1/tasks/" + encodeURIComponent(taskID) + "/feedback").then(body => {
			if (selectedTaskID !== taskID) return;
			const context = parseFeedbackContext(body, taskID);
			if (!context) throw new Error("invalid feedback context");
			if (JSON.stringify(feedbackContext) !== JSON.stringify(context)) { feedbackContext = context; renderFeedbackContext(context); } updateControls();
		}).catch(() => { if (selectedTaskID === taskID) { taskContextUnavailable = true; updateControls(); } });
	}
	function validApprovalProposal(value, toolName) {
		if (!value || typeof value !== "object" || Array.isArray(value) || value.version !== 1 || !presentationID.test(value.board_id) || !presentationID.test(value.card_id) || !Number.isSafeInteger(value.expected_board_revision) || value.expected_board_revision < 1 || !Number.isSafeInteger(value.expected_card_revision) || value.expected_card_revision < 1) return "";
		const digest = /^[0-9a-f]{64}$/, integer = (number, minimum) => Number.isSafeInteger(number) && number >= minimum;
		if (toolName === "workboard_propose_criteria") {
			const keys = ["version", "kind", "board_id", "card_id", "expected_board_revision", "expected_card_revision", "expected_criteria_revision", "expected_criteria_digest", "criteria"];
			if (value.kind !== "criteria_change" || Object.keys(value).some(key => !keys.includes(key)) || Object.keys(value).length !== keys.length || !integer(value.expected_criteria_revision, 1) || !digest.test(value.expected_criteria_digest) || !Array.isArray(value.criteria) || !value.criteria.length || value.criteria.length > 32) return "";
			const ids = new Set();
			for (const criterion of value.criteria) { if (!criterion || typeof criterion !== "object" || Array.isArray(criterion) || Object.keys(criterion).length !== 7 || !["version", "id", "kind", "required_source", "validator_id", "description", "required"].every(key => Object.hasOwn(criterion, key)) || criterion.version !== 1 || !presentationID.test(criterion.id) || ids.has(criterion.id) || typeof criterion.validator_id !== "string" || !criterion.validator_id || criterion.validator_id.length > 128 || typeof criterion.description !== "string" || !criterion.description.trim() || textBytes(criterion.description) > 4096 || typeof criterion.required !== "boolean" || criterion.kind === "objective" && criterion.required_source !== "deterministic" || criterion.kind === "subjective" && criterion.required_source !== "user_feedback" || !["objective", "subjective"].includes(criterion.kind)) return ""; ids.add(criterion.id); }
		} else if (toolName === "workboard_request_candidate_decision") {
			const keys = ["version", "kind", "board_id", "card_id", "attempt_id", "candidate_id", "expected_board_revision", "expected_card_revision", "expected_attempt_revision", "criteria_revision", "evidence_head_revision", "candidate_digest", "criteria_digest", "evidence_set_digest", "policy_digest", "decision", "rationale"];
			if (value.kind !== "candidate_decision" || Object.keys(value).some(key => !keys.includes(key)) || Object.keys(value).length !== keys.length || !presentationID.test(value.attempt_id) || !presentationID.test(value.candidate_id) || !integer(value.expected_attempt_revision, 1) || !integer(value.criteria_revision, 1) || !integer(value.evidence_head_revision, 0) || ![value.candidate_digest, value.criteria_digest, value.evidence_set_digest, value.policy_digest].every(item => typeof item === "string" && digest.test(item)) || !["accepted", "rejected"].includes(value.decision) || typeof value.rationale !== "string" || !value.rationale.trim() || textBytes(value.rationale) > 65536) return "";
		} else return "";
		const rendered = JSON.stringify(value, null, 2);
		return textBytes(rendered) <= maxApprovalProposal ? rendered : "";
	}
	function validApproval(item, taskID) {
		if (!item || typeof item !== "object" || !presentationID.test(item.id) ||
			!Number.isSafeInteger(item.revision) || item.revision < 1 || typeof item.state !== "string" ||
			typeof item.prompt !== "string" || !item.prompt || textBytes(item.prompt) > maxApprovalPrompt ||
			typeof item.scope_summary !== "string" || !item.scope_summary || textBytes(item.scope_summary) > maxApprovalScope ||
			typeof item.tool_name !== "string" || !item.tool_name || item.tool_name.length > 64 || !["pending", "approved", "denied", "revoked", "consumed", "expired"].includes(item.state) ||
			!["read_only", "idempotent_write", "non_idempotent_write"].includes(item.tool_behavior) || !Number.isFinite(Date.parse(item.expires_at))) return null;
		for (const name of ["can_allow", "can_deny", "can_revoke"]) if (typeof item[name] !== "boolean") return null;
		if (item.state === "pending" && (item.revision !== 1 || !item.can_allow || !item.can_deny || item.can_revoke) ||
			item.state === "approved" && (item.revision !== 2 || item.can_allow || item.can_deny || !item.can_revoke) ||
			item.state === "denied" && item.revision !== 2 || ["revoked", "consumed"].includes(item.state) && item.revision !== 3 ||
			item.state === "expired" && ![1, 2].includes(item.revision) ||
			!["pending", "approved"].includes(item.state) && (item.can_allow || item.can_deny || item.can_revoke)) return null;
		const proposalTool = ["workboard_propose_criteria", "workboard_request_candidate_decision"].includes(item.tool_name);
		const proposalRequired = proposalTool && ["pending", "approved"].includes(item.state);
		const proposalText = proposalRequired ? validApprovalProposal(item.proposal, item.tool_name) : "";
		if (proposalRequired && !proposalText || !proposalRequired && Object.hasOwn(item, "proposal")) return null;
		return Object.freeze({id: item.id, taskID, revision: item.revision, state: item.state,
			prompt: item.prompt, scopeSummary: item.scope_summary, toolName: item.tool_name, behavior: boundedText(item.tool_behavior) || "unspecified",
			proposalText, canAllow: item.can_allow, canDeny: item.can_deny, canRevoke: item.can_revoke});
	}
	function closeApproval() {
		approvalDialog.hidden = true;
		activeApproval = null;
		approvalClose.textContent = "Close without deciding";
		approvalReconcile.hidden = true;
		approvalAcknowledge.hidden = true;
		const opener = approvalOpener;
		approvalOpener = null;
		if (opener && opener.isConnected) opener.focus();
	}
	function openApproval(item, opener) {
		activeApproval = item;
		approvalOpener = opener;
		approvalDialogMeta.textContent = item.toolName + " · " + stateLabel(item.behavior) + " · " + stateLabel(item.state);
		approvalDialogScope.textContent = "Scope: " + item.scopeSummary;
		approvalDialogPrompt.textContent = item.proposalText ? item.prompt + "\n\nExact proposal:\n" + item.proposalText : item.prompt;
		approvalAllow.hidden = !item.canAllow;
		approvalDeny.hidden = !item.canDeny;
		approvalRevoke.hidden = !item.canRevoke;
		approvalDialog.hidden = false;
		approvalReconcile.hidden = true;
		approvalAcknowledge.hidden = true;
		(item.canDeny ? approvalDeny : approvalClose).focus();
	}
	function renderApproval(item) {
		const row = element("li");
		const button = element("button", "", item.toolName + " — " + stateLabel(item.state));
		button.type = "button";
		button.addEventListener("click", () => openApproval(item, button));
		row.append(button);
		approvalList.append(row);
	}
	function loadApprovals() {
		const task = selectedControls;
		approvalList.replaceChildren();
		approvalCount.textContent = "";
		if (!task || !selectedChat) {
			approvalPanel.hidden = true;
			return;
		}
		approvalPanel.hidden = false;
		showNotice(approvalState, "Loading approvals…", false);
		requestJSON("/api/v1/tasks/" + encodeURIComponent(task.taskID) + "/approvals?limit=" + String(maxApprovals)).then(body => {
			if (selectedControls !== task) return;
			if (!body || body.version !== 1 || body.task_id !== task.taskID || !Array.isArray(body.items) || body.items.length > maxApprovals ||
				typeof body.has_more !== "boolean" || typeof body.next_cursor !== "string" || body.next_cursor.length > 512 || body.has_more !== Boolean(body.next_cursor)) throw new Error("invalid approvals");
			const items = body.items.map(item => validApproval(item, task.taskID));
			if (items.some(item => !item || item.taskID !== task.taskID)) throw new Error("invalid approval");
			for (const item of items) renderApproval(item);
			approvalCount.textContent = String(items.length);
			if (body.has_more) showNotice(approvalState, "Showing the first " + String(items.length) + " approvals.", false);
			else if (items.length) approvalState.hidden = true;
			else showNotice(approvalState, "No approvals need attention.", false);
		}).catch(() => {
			if (selectedControls === task) showNotice(approvalState, "Approvals could not be loaded.", true);
		});
	}
	function decideApproval(action) {
		const item = activeApproval;
		if (!item || pendingIntent) return;
		approvalClose.textContent = "Decision in progress";
		mutate("/api/v1/tasks/" + encodeURIComponent(item.taskID) + "/approvals/" + encodeURIComponent(item.id) + "/decision", {
			version: 1, task_id: item.taskID, approval_id: item.id, action, expected_revision: item.revision
		}, () => {
			closeApproval();
			loadApprovals();
		});
	}
	function appendProvisional(taskID, text) {
		if (!presentationID.test(taskID) || typeof text !== "string" || !text) return;
		let item = provisionalTasks.get(taskID);
		if (!item) {
			if (provisionalTasks.size >= maxProvisionalTasks || provisionalTextSize >= maxProvisionalText) return;
			const node = element("section", "provisional-item");
			node.dataset.taskId = taskID;
			node.append(element("span", "provisional-label", "Assistant · provisional · " + taskID));
			const textNode = element("p");
			node.append(textNode);
			provisional.append(node);
			item = {node, textNode, size: 0};
			provisionalTasks.set(taskID, item);
		}
		const remaining = Math.min(maxProvisionalTaskText - item.size, maxProvisionalText - provisionalTextSize);
		if (remaining <= 0) return;
		const addition = text.slice(0, remaining);
		item.textNode.textContent += addition;
		item.size += addition.length;
		provisionalTextSize += addition.length;
		provisional.hidden = provisionalTasks.size === 0;
		if (!announcementTimer) announcementTimer = window.setTimeout(() => {
			announcementTimer = 0;
			streamAnnouncement.textContent = "Provisional assistant output updated for " + String(provisionalTasks.size) + " task" + (provisionalTasks.size === 1 ? "." : "s.");
		}, 1000);
	}
	function clearProvisionalTask(taskID) {
		const item = provisionalTasks.get(taskID);
		if (!item) return;
		item.node.remove();
		provisionalTasks.delete(taskID);
		provisionalTextSize -= item.size;
		provisional.hidden = provisionalTasks.size === 0;
	}
	function clearAllProvisional() {
		provisional.replaceChildren();
		provisionalTasks.clear();
		provisionalTextSize = 0;
		provisional.hidden = true;
		if (announcementTimer) window.clearTimeout(announcementTimer);
		announcementTimer = 0;
		streamAnnouncement.textContent = "";
	}
	function validEnvelope(event) {
		return event && event.version === 1 && typeof event.kind === "string" &&
			(event.durability === "provisional" || event.durability === "committed") &&
			typeof event.data === "object" && event.data !== null;
	}
	function applyPresentationEvent(payload) {
		if (!validEnvelope(payload) || payload.subject !== selectedChat) return;
		if (payload.kind === "chat.delta" && payload.durability === "provisional" && payload.revision === 0) {
			appendProvisional(payload.data.task_id, boundedText(payload.data.text));
			return;
		}
		const committedStreamError = payload.durability === "committed" && Number.isSafeInteger(payload.revision) && payload.revision > eventRevision && typeof payload.cursor === "string" && payload.cursor;
		if (payload.kind === "stream.error" && ((payload.durability === "provisional" && payload.revision === 0) || committedStreamError)) {
			clearAllProvisional();
			showNotice(transcriptState, "Live updates need attention.", true);
			if (committedStreamError) eventRevision = payload.revision;
			return;
		}
		if (payload.durability !== "committed" || !Number.isSafeInteger(payload.revision) || payload.revision <= eventRevision || typeof payload.cursor !== "string" || !payload.cursor) return;
		if (payload.kind === "chat.snapshot" && payload.data.chat_id === selectedChat && Array.isArray(payload.data.messages) && payload.data.messages.length <= 100) {
			messageIDs.clear();
			lastMessageRevision = 0;
			historyCursor = "";
			loadMoreMessages.hidden = true;
			window.NexusChatRender.messages(transcript, payload.data.messages, true);
			transcriptState.hidden = payload.data.messages.length > 0;
			clearAllProvisional();
			eventRevision = payload.revision;
		} else if (payload.kind === "chat.final" && payload.data.message) {
			clearProvisionalTask(payload.data.task_id);
			appendMessage(payload.data.message);
			setTaskState(payload.data.state);
			eventRevision = payload.revision;
		} else if (payload.kind === "lifecycle.event" && typeof payload.data.state === "string") {
			setTaskState(payload.data.state);
			eventRevision = payload.revision;
		} else if (payload.kind === "task.terminal" && typeof payload.data.state === "string") {
			clearProvisionalTask(payload.data.task_id);
			setTaskState(payload.data.state);
			eventRevision = payload.revision;
			if (payload.data.task_id === selectedTaskID && payload.data.state !== "completed") loadTaskContext(selectedTaskID);
			if (payload.data.state === "completed") loadHistory(selectedChat, "", true, false);
			} else if (payload.kind === "model.changed") {
				eventRevision = payload.revision;
				window.NexusInspector.modelChanged();
			} else if (payload.kind === "tool.changed") {
				eventRevision = payload.revision;
				window.NexusInspector.toolChanged(payload.data.task_id);
			} else if (payload.kind === "route.changed") {
				eventRevision = payload.revision;
				window.NexusInspector.routeChanged(payload.data.task_id);
			} else if (["worker.changed", "error.changed"].includes(payload.kind)) {
				eventRevision = payload.revision;
			} else if (payload.kind === "approval.changed" && payload.data.task_id === (selectedControls && selectedControls.taskID)) {
				eventRevision = payload.revision;
				loadApprovals();
			} else if (payload.kind === "feedback.changed" && payload.data.task_id === (selectedControls && selectedControls.taskID)) {
				eventRevision = payload.revision;
				loadHistory(selectedChat, "", true, false);
			}
	}
	function connect(chatID) {
		if (source) source.close();
		clearAllProvisional();
		if (!("EventSource" in window)) {
			connection.textContent = "Live updates unavailable";
			return;
		}
		source = new EventSource(base + "/api/v1/chats/" + encodeURIComponent(chatID) + "/events");
		source.onopen = () => { connection.textContent = "Live"; };
		const receive = event => {
			try { applyPresentationEvent(JSON.parse(event.data)); } catch (_) { connection.textContent = "Invalid live update"; }
		};
		source.onmessage = receive;
		for (const eventName of ["event", "chat.snapshot", "chat.delta", "chat.final", "lifecycle.event", "model.changed", "tool.changed", "route.changed", "worker.changed", "error.changed", "task.terminal", "approval.changed", "feedback.changed", "stream.error"]) source.addEventListener(eventName, receive);
		source.onerror = () => {
			clearAllProvisional();
			connection.textContent = "Reconnecting…";
		};
	}
	function selectChat(chatID, state) {
		if (typeof chatID !== "string" || !chatID || chatID.length > 512) return;
		if (source) {
			source.close();
			source = null;
		}
		selectedChat = chatID;
		approvalDialog.hidden = true;
		activeApproval = null;
		approvalOpener = null;
		selectedControls = null; taskContextUnavailable = false;
		selectedTaskID = "";
		feedbackContext = null;
		window.NexusInspector.clearTask();
		queuedSubmissionID = "";
		eventRevision = 0;
		historyCursor = "";
		historyHead = 0;
		lastMessageRevision = 0;
		historyNeedsReset = false;
		messageIDs.clear();
		pendingHistoryReconcile = false;
		historyRequest++;
		loadingHistory = false;
		loadMoreMessages.hidden = true;
		approvalList.replaceChildren();
		approvalPanel.hidden = true;
		clearAllProvisional();
		for (const button of list.querySelectorAll("button[data-chat-id]")) button.setAttribute("aria-current", button.dataset.chatId === chatID ? "true" : "false");
		title.textContent = "Loading description…";
  title.title = "Chat ID: " + chatID;
  chatDescription(chatID).then(description=>{if(selectedChat===chatID)title.textContent=description;});
		if (typeof state === "string") setTaskState(state);
		else chatState.textContent = "";
		transcript.replaceChildren();
		window.history.replaceState(null, "", base + "/chats/" + encodeURIComponent(chatID));
		loadHistory(chatID, "", true, true);
		updateControls();
	}
	function startNewChat() {
		if (pendingIntent) return;
		stopSubmissionPolling(); submissionShouldNavigate = false;
		if (source) {
			source.close();
			source = null;
		}
		selectedChat = "";
		approvalDialog.hidden = true;
		activeApproval = null;
		approvalOpener = null;
		selectedControls = null; taskContextUnavailable = false;
		selectedTaskID = "";
		feedbackContext = null;
		window.NexusInspector.clearTask();
		queuedSubmissionID = "";
		eventRevision = 0;
		transcript.replaceChildren();
		clearAllProvisional();
		approvalList.replaceChildren();
		approvalPanel.hidden = true;
		taskControls.hidden = true;
		feedbackPanel.hidden = true;
		title.textContent = "New chat";
		chatState.textContent = "";
		showNotice(transcriptState, "Write a message to start a chat.", false);
		for (const button of list.querySelectorAll("button[data-chat-id]")) button.setAttribute("aria-current", "false");
		window.history.replaceState(null, "", base + "/chats");
		updateControls();
		composerText.focus();
	}
	function refreshChats() {
		if (loadingPage) return;
		loadChats("", true);
	}
	function reconcileCurrent(successMessage) {
		showMutation(successMessage || "Checking committed state…", Boolean(pendingIntent), false);
		checkRecentOperations(successMessage);
		if (queuedSubmissionID) observeSubmission(queuedSubmissionID, true, submissionShouldNavigate);
		refreshChats();
		if (selectedChat) loadHistory(selectedChat, "", true, false);
	}
	function submitComposer() {
		const text = composerText.value;
		composerError.hidden = true;
		if (!text.trim()) {
			showNotice(composerError, "Enter a message first.", true);
			composerText.focus();
			return;
		}
		if (textBytes(text) > maxText) {
			showNotice(composerError, "The message is too large.", true);
			return;
		}
		let path = "/api/v1/chats";
		let payload = {version: 1, action: "submit", text};
		if (selectedChat) {
			const controls = selectedControls;
			if (!controls || !controls.canResume) return;
			path = "/api/v1/chats/" + encodeURIComponent(selectedChat) + "/resume";
			payload = {version: 1, action: "resume", chat_id: selectedChat, task_id: controls.taskID, text, expected_revision: controls.revision};
		}
		mutate(path, payload, body => {
			composerText.value = "";
			composerCount.textContent = "0";
			if (typeof body.chat_id === "string" && presentationID.test(body.chat_id)) selectChat(body.chat_id, body.state);
			else {
				queuedSubmissionID = typeof body.submission_id === "string" && presentationID.test(body.submission_id) ? body.submission_id : "";
				if (queuedSubmissionID) observeSubmission(queuedSubmissionID, true, !selectedChat);
				else reconcileCurrent();
			}
		});
	}
	function submitSteering() {
		const controls = selectedControls;
		const text = steeringText.value;
		if (!controls || !controls.canSteer || !text.trim() || textBytes(text) > 65536) {
			showMutation("Enter bounded guidance for an active task.", false);
			return;
		}
		mutate("/api/v1/tasks/" + encodeURIComponent(controls.taskID) + "/steering", {
			version: 1, action: "steer", task_id: controls.taskID, text, expected_revision: controls.revision
		}, () => {
			steeringText.value = "";
			showMutation("Guidance queued. Application will be shown only after committed confirmation.", false);
			reconcileCurrent();
		});
	}
	function submitCancellation() {
		const controls = selectedControls;
		if (queuedSubmissionID) {
			if (!window.confirm("Cancel this queued submission?")) return;
			const submissionID = queuedSubmissionID;
			mutate("/api/v1/submissions/" + encodeURIComponent(submissionID) + "/cancel", {
				version: 1, action: "cancel_submission", submission_id: submissionID
			}, body => {
				if (["queued", "running"].includes(body.state)) {
					showMutation(body.requested ? "Cancellation requested; submission is still " + stateLabel(body.state) + "." : "Submission remains " + stateLabel(body.state) + ".", true, false);
					observeSubmission(submissionID, true, submissionShouldNavigate);
				} else {
					queuedSubmissionID = "";
					queuedSubmissionCanCancel = false;
					showMutation("Submission is " + stateLabel(body.state) + ".", false, false);
					updateControls();
					refreshChats();
				}
			});
			return;
		}
		if (!controls || !controls.canCancel || !window.confirm("Request cancellation for this task?")) return;
		mutate("/api/v1/tasks/" + encodeURIComponent(controls.taskID) + "/cancel", {
			version: 1, action: "cancel", task_id: controls.taskID, expected_revision: controls.revision
		}, body => {
			showMutation(body.requested ? "Cancellation requested. Waiting for committed task state." : "Task is already " + stateLabel(body.state) + ".", false, false);
			reconcileCurrent();
		});
	}
	function submitFeedback(accepted) {
		const controls = selectedControls;
		const context = feedbackContext;
		const revise = Boolean(context && context.feedbackID);
		const cost = Number(attemptCost.value);
		if (!controls || !controls.canFeedback || !context || !context.feedbackAllowed || (!revise && (attemptCost.value.trim() === "" || !Number.isFinite(cost) || cost < 0))) {
			showMutation("Enter the observed final-attempt cost before recording evidence.", false);
			return;
		}
		const payload = {version: 1, action: revise ? "revise" : "record", task_id: controls.taskID, accepted};
		if (revise) {
			payload.feedback_id = context.feedbackID;
			payload.expected_revision = context.revision;
		} else {
			payload.attempt_cost = cost;
		}
		mutate(revise ? "/api/v1/feedback/revisions" : "/api/v1/feedback", payload, () => {
			showMutation("Subjective outcome evidence recorded.", false);
			loadHistory(selectedChat, "", true, false);
		});
	}
	composer.addEventListener("submit", event => {
		event.preventDefault();
		if (!composing) submitComposer();
	});
	composerText.addEventListener("compositionstart", () => { composing = true; });
	composerText.addEventListener("compositionend", () => { composing = false; });
	composerText.addEventListener("input", () => { composerCount.textContent = String(textBytes(composerText.value)); });
	composerText.addEventListener("keydown", event => {
		if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !composing) {
			event.preventDefault();
			composer.requestSubmit();
		}
	});
	newChat.addEventListener("click", startNewChat);
	steerTask.addEventListener("click", submitSteering);
	cancelTask.addEventListener("click", submitCancellation);
	feedbackAccepted.addEventListener("click", () => submitFeedback(true));
	feedbackRejected.addEventListener("click", () => submitFeedback(false));
	reconcile.addEventListener("click", () => reconcileCurrent());
	acknowledgeUnresolved.addEventListener("click", acknowledgeOutcome);
	approvalReconcile.addEventListener("click", () => reconcileCurrent());
	approvalAcknowledge.addEventListener("click", acknowledgeOutcome);
	approvalAllow.addEventListener("click", () => decideApproval("allow"));
	approvalDeny.addEventListener("click", () => decideApproval("deny"));
	approvalRevoke.addEventListener("click", () => decideApproval("revoke"));
	approvalClose.addEventListener("click", closeApproval);
	approvalDialog.addEventListener("keydown", event => {
		if (event.key === "Escape" && !pendingIntent && unresolvedOperations.length === 0 && operationsReady) {
			event.preventDefault();
			closeApproval();
			return;
		}
		if (event.key !== "Tab") return;
		const focusable = [approvalDeny, approvalAllow, approvalRevoke, approvalClose, approvalReconcile, approvalAcknowledge].filter(node => !node.hidden && !node.disabled);
		if (!focusable.length) return;
		const first = focusable[0];
		const last = focusable[focusable.length - 1];
		if (event.shiftKey && document.activeElement === first) {
			event.preventDefault();
			last.focus();
		} else if (!event.shiftKey && document.activeElement === last) {
			event.preventDefault();
			first.focus();
		}
	});
	loadMore.addEventListener("click", () => loadChats(nextCursor));
	loadMoreMessages.addEventListener("click", () => loadHistory(selectedChat, historyNeedsReset ? "" : historyCursor, historyNeedsReset, false));
	window.addEventListener("beforeunload", () => { if (source) source.close(); });
	const relativePath = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	const cronRoute = window.NexusRoutes.cron(relativePath); const statusRoute = window.NexusRoutes.status(relativePath); const jobsRoute = window.NexusRoutes.jobs(relativePath);
	const skillsRoute = window.NexusRoutes.skills(relativePath), statsRoute = window.NexusRoutes.stats(relativePath);
	const workboardRoute = window.NexusRoutes.workboards(relativePath), settingsRoute = window.NexusRoutes.settings(relativePath), modelsRoute = window.NexusRoutes.models(relativePath), routingRoute = window.NexusRoutes.routing(relativePath), eliminationRoute = window.NexusRoutes.elimination(relativePath);	const routeMatch = relativePath.match(/^\/chats\/([^/]+)$/);
	if (routeMatch) {
		try { selectChat(decodeURIComponent(routeMatch[1])); } catch (_) { showNotice(transcriptState, "The chat address is invalid.", true); }
	}
	if (!cronRoute && !statusRoute && !jobsRoute && !skillsRoute && !statsRoute && !workboardRoute && !settingsRoute && !modelsRoute && !routingRoute && !eliminationRoute) {
	loadChats(""); checkRecentOperations(); window.NexusInspector.loadGlobals(); updateControls();
 window.NexusLive.chats({base,list,stateLabel,renderChat,ready:()=>!loadingPage,busy:value=>{loadingPage=value;},total:()=>chatTotal,added:()=>{chatTotal++;},max:maxChats,done:()=>{chatCount.textContent=String(chatTotal);if(chatTotal)listState.hidden=true;},reconnect:()=>{if(selectedChat&&(!source||source.readyState===2))return loadHistory(selectedChat,"",true,true);}});
	fetch(base + "/api/v1/session/csrf", {
		method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/json"},
		body: JSON.stringify({version: 1}), cache: "no-store"
	}).then(response => {
		if (!response.ok) throw new Error("session unavailable");
		return response.json();
	}).then(session => {
		if (!session || session.version !== 1 || typeof session.csrf_token !== "string" || !session.csrf_token) throw new Error("invalid session");
		csrfToken = session.csrf_token;
		updateControls();
		if (!selectedChat) connection.textContent = "Connected";
	}).catch(() => { connection.textContent = "Session needs attention"; }); }
	for (const link of document.querySelectorAll("[data-view]")) {
		const selected = link.dataset.view === "cron" ? cronRoute : link.dataset.view === "status" ? statusRoute : link.dataset.view === "jobs" ? jobsRoute : link.dataset.view === "skills" ? skillsRoute : link.dataset.view === "stats" ? statsRoute : link.dataset.view === "workboards" ? workboardRoute : link.dataset.view === "settings" ? settingsRoute : link.dataset.view === "models" ? modelsRoute : link.dataset.view === "routing" ? routingRoute : link.dataset.view === "elimination" ? eliminationRoute : window.NexusRoutes.chats(relativePath);
		if (selected) link.setAttribute("aria-current", "page");
		else link.removeAttribute("aria-current");
	}
})();
