"use strict";

(() => {
  const base = document.body.dataset.basePath;
  const code = document.querySelector("#approval-code");
  const command = document.querySelector("#command");
  const commandCode = command.querySelector("code");
  const copy = document.querySelector("#copy-command");
  const copyStatus = document.querySelector("#copy-status");
  const status = document.querySelector("#status");
  const retry = document.querySelector("#retry");
  let stopped = false;

  async function post(path, body) {
    return fetch(base + path, {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify(body),
      cache: "no-store"
    });
  }

  async function begin() {
    stopped = false;
    command.hidden = true; copy.disabled = true; copyStatus.textContent = "";
    retry.hidden = true;
    status.textContent = "Creating a secure one-time challenge.";
    const response = await post("/api/v1/session/challenges", {version: 1});
    if (!response.ok) throw new Error("challenge unavailable");
    const challenge = await response.json();
		const expiresAt = Date.parse(challenge.expires_at);
    code.textContent = challenge.approval_code;
    commandCode.textContent = "nexus web approve " + challenge.approval_code;
    command.hidden = false; copy.disabled = false;
    status.textContent = "Waiting for terminal approval.";
    while (!stopped) {
		if (!Number.isFinite(expiresAt) || Date.now() >= expiresAt) throw new Error("challenge expired");
      await new Promise(resolve => window.setTimeout(resolve, 1000));
      const completed = await post("/api/v1/session", {version: 1, challenge_id: challenge.challenge_id});
      if (completed.status === 401) continue;
      if (!completed.ok) throw new Error("session unavailable");
      status.textContent = "Connected. Opening NexusRouter.";
      window.location.replace(base + "/");
      return;
    }
  }

  function failed() {
    stopped = true;
    command.hidden = true; copy.disabled = true; commandCode.textContent = ""; copyStatus.textContent = "";
    code.textContent = "Connection unavailable";
    status.textContent = "Create a new one-time code and try again.";
    retry.hidden = false;
  }

  copy.addEventListener("click", async () => {
    const text = commandCode.textContent;
    if (copy.disabled || command.hidden || !text) return;
    try {
      await navigator.clipboard.writeText(text);
      if (text === commandCode.textContent && !command.hidden) copyStatus.textContent = "Command copied. Paste it into your terminal.";
    } catch (_) {
      if (text !== commandCode.textContent || command.hidden) return;
      const range = document.createRange(); range.selectNodeContents(commandCode);
      const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
      copyStatus.textContent = "Clipboard unavailable. The command is selected; press Command+C or Ctrl+C to copy.";
    }
  });

  retry.addEventListener("click", () => begin().catch(failed));
  begin().catch(failed);
})();
