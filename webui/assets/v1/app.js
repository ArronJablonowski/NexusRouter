"use strict";

(() => {
	const base = document.body.dataset.basePath || "";
	const connection = document.querySelector("#connection-state");
	const list = document.querySelector("#chat-list");
	const listState = document.querySelector("#chat-list-state");
	const chatCount = document.querySelector("#chat-count");
	const loadMore = document.querySelector("#load-more");
	const title = document.querySelector("#chat-title");
	const chatState = document.querySelector("#chat-state");
	const transcript = document.querySelector("#transcript");
	const transcriptState = document.querySelector("#transcript-state");
	const loadMoreMessages = document.querySelector("#load-more-messages");
	const provisional = document.querySelector("#provisional");
	const pageLimit = 25;
	const maxChats = 100;
	const historyPageLimit = 100;
	const maxMessages = 500;
	const maxText = 1 << 20;
	const maxProvisionalTasks = 16;
	const maxProvisionalTaskText = 256 << 10;
	const maxProvisionalText = 1 << 20;
	const presentationID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
	let csrfToken = "";
	let nextCursor = "";
	let selectedChat = "";
	let eventRevision = 0;
	let source = null;
	let loadingPage = false;
	let loadingHistory = false;
	let pendingHistoryReconcile = false;
	let historyRequest = 0;
	let chatTotal = 0;
	let historyCursor = "";
	let historyHead = 0;
	let lastMessageRevision = 0;
	let historyNeedsReset = false;
	const messageIDs = new Set();
	const provisionalTasks = new Map();
	let provisionalTextSize = 0;

	window.DarwinSession = Object.freeze({
		csrfHeader: () => csrfToken ? {"X-Darwin-CSRF": csrfToken} : {}
	});

	function element(name, className, value) {
		const node = document.createElement(name);
		if (className) node.className = className;
		if (value !== undefined) node.textContent = value;
		return node;
	}

	function boundedText(value) {
		return typeof value === "string" && value.length <= maxText ? value : "";
	}

	function showNotice(node, message, failed) {
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

	function formatTime(value) {
		const date = new Date(value);
		return Number.isNaN(date.getTime()) ? "Unknown time" : date.toLocaleString();
	}

	function stateLabel(value) {
		return typeof value === "string" && value ? value.replaceAll("_", " ") : "unknown";
	}

	function renderChat(item) {
		if (!item || item.version !== 1 || typeof item.chat_id !== "string" || !item.chat_id || item.chat_id.length > 512) return false;
		const row = element("li");
		const button = element("button");
		button.type = "button";
		button.dataset.chatId = item.chat_id;
		button.setAttribute("aria-current", item.chat_id === selectedChat ? "true" : "false");
		button.append(element("span", "chat-name", item.chat_id));
		const meta = element("span", "chat-meta");
		meta.append(element("span", "", stateLabel(item.state)), element("time", "", formatTime(item.started_at)));
		button.append(meta);
		if (item.chat_id === selectedChat) setTaskState(item.state);
		button.addEventListener("click", () => selectChat(item.chat_id, item.state));
		row.append(button);
		list.append(row);
		return true;
	}

	function loadChats(after) {
		if (loadingPage || chatTotal >= maxChats) return;
		loadingPage = true;
		loadMore.disabled = true;
		if (!after) showNotice(listState, "Loading chats…", false);
		const query = new URLSearchParams({limit: String(pageLimit)});
		if (after) query.set("after", after);
		requestJSON("/api/v1/chats?" + query.toString()).then(page => {
			if (!page || page.version !== 1 || !Array.isArray(page.items) || page.items.length > pageLimit) throw new Error("invalid chat page");
			let added = 0;
			for (const item of page.items.slice(0, maxChats - chatTotal)) if (renderChat(item)) added++;
			chatTotal += added;
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

	function messageText(message) {
		if (!message || typeof message !== "object") return "";
		return boundedText(message.text);
	}

	function validHistoryMessage(message, afterRevision, seen) {
		return message && typeof message === "object" && typeof message.id === "string" && message.id && message.id.length <= 512 &&
			(message.role === "user" || message.role === "assistant") && typeof message.text === "string" && message.text && message.text.length <= maxText &&
			Number.isSafeInteger(message.revision) && message.revision > afterRevision && Number.isSafeInteger(message.source_revision) &&
			message.source_revision > 0 && message.source_revision <= historyHead && !seen.has(message.id);
	}

	function appendMessage(message) {
		if (!message || typeof message !== "object" || transcript.children.length >= maxMessages) return;
		const role = message.role;
		if (role !== "user" && role !== "assistant") return;
		const row = element("li", "message " + role);
		row.append(element("span", "message-label", message.role));
		const content = messageText(message);
		if (content) row.append(element("p", "", content));
		transcript.append(row);
	}

	function applyHistoryPage(body, reset) {
		if (!body || body.version !== 1 || body.chat_id !== selectedChat || typeof body.task_id !== "string" || !body.task_id || !Array.isArray(body.messages) || body.messages.length > historyPageLimit ||
			!Number.isSafeInteger(body.head_revision) || body.head_revision < 1 || typeof body.has_more !== "boolean" ||
			body.has_more !== (typeof body.next_cursor === "string" && body.next_cursor !== "" && body.next_cursor.length <= 4096)) throw new Error("invalid transcript");
		if (!reset && body.head_revision !== historyHead) throw new Error("transcript changed during pagination");
		historyNeedsReset = false;
		if (reset) historyHead = body.head_revision;
		let revision = reset ? 0 : lastMessageRevision;
		const seen = reset ? new Set() : new Set(messageIDs);
		for (const message of body.messages) {
			if (!validHistoryMessage(message, revision, seen)) throw new Error("invalid transcript message");
			revision = message.revision;
			seen.add(message.id);
		}
		if (reset) {
			transcript.replaceChildren();
			messageIDs.clear();
			lastMessageRevision = 0;
		}
		const remaining = maxMessages - transcript.children.length;
		const truncatedPage = body.messages.length > remaining;
		for (const message of body.messages.slice(0, remaining)) {
			appendMessage(message);
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
		if (reset) showNotice(transcriptState, "Loading committed transcript…", false);
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

	function setTaskState(value) {
		const state = stateLabel(value);
		chatState.textContent = state;
		chatState.className = "state-pill state-" + state.replaceAll(" ", "-");
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
			transcript.replaceChildren();
			messageIDs.clear();
			lastMessageRevision = 0;
			historyCursor = "";
			loadMoreMessages.hidden = true;
			for (const message of payload.data.messages) appendMessage(message);
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
			if (payload.data.state === "completed") loadHistory(selectedChat, "", true, false);
		} else if (["model.changed", "tool.changed", "route.changed", "worker.changed", "error.changed"].includes(payload.kind)) {
			eventRevision = payload.revision;
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
		for (const eventName of ["event", "chat.snapshot", "chat.delta", "chat.final", "lifecycle.event", "model.changed", "tool.changed", "route.changed", "worker.changed", "error.changed", "task.terminal", "stream.error"]) source.addEventListener(eventName, receive);
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
		clearAllProvisional();
		for (const button of list.querySelectorAll("button[data-chat-id]")) button.setAttribute("aria-current", button.dataset.chatId === chatID ? "true" : "false");
		title.textContent = chatID;
		if (typeof state === "string") setTaskState(state);
		else chatState.textContent = "";
		transcript.replaceChildren();
		window.history.replaceState(null, "", base + "/chats/" + encodeURIComponent(chatID));
		loadHistory(chatID, "", true, true);
	}

	loadMore.addEventListener("click", () => loadChats(nextCursor));
	loadMoreMessages.addEventListener("click", () => loadHistory(selectedChat, historyNeedsReset ? "" : historyCursor, historyNeedsReset, false));
	window.addEventListener("beforeunload", () => { if (source) source.close(); });

	const relativePath = window.location.pathname.startsWith(base) ? window.location.pathname.slice(base.length) : "";
	const routeMatch = relativePath.match(/^\/chats\/([^/]+)$/);
	if (routeMatch) {
		try { selectChat(decodeURIComponent(routeMatch[1])); } catch (_) { showNotice(transcriptState, "The chat address is invalid.", true); }
	}
	loadChats("");

	fetch(base + "/api/v1/session/csrf", {
		method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/json"},
		body: JSON.stringify({version: 1}), cache: "no-store"
	}).then(response => {
		if (!response.ok) throw new Error("session unavailable");
		return response.json();
	}).then(session => {
		csrfToken = session.csrf_token;
		if (!selectedChat) connection.textContent = "Connected";
	}).catch(() => { connection.textContent = "Session needs attention"; });

	for (const link of document.querySelectorAll("[data-view]")) {
		const selected = window.location.pathname.startsWith(link.pathname);
		if (selected) link.setAttribute("aria-current", "page");
		else link.removeAttribute("aria-current");
	}
})();
