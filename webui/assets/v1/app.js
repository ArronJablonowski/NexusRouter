"use strict";

(() => {
	const base = document.body.dataset.basePath;
	const connection = document.querySelector("#connection-state");
	let csrfToken = "";
	window.DarwinSession = Object.freeze({
		csrfHeader: () => csrfToken ? {"X-Darwin-CSRF": csrfToken} : {}
	});
	fetch(base + "/api/v1/session/csrf", {
		method: "POST",
		credentials: "same-origin",
		headers: {"Content-Type": "application/json"},
		body: JSON.stringify({version: 1}),
		cache: "no-store"
	}).then(response => {
		if (!response.ok) throw new Error("session unavailable");
		return response.json();
	}).then(session => {
		csrfToken = session.csrf_token;
		connection.textContent = "Connected";
	}).catch(() => {
		connection.textContent = "Session needs attention";
	});

  const links = document.querySelectorAll("[data-view]");
  const current = window.location.pathname;
  for (const link of links) {
    const selected = current.startsWith(link.pathname);
    if (selected) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
})();
