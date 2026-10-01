// Host-generated configuration is prepended in the private per-run directory.
// This extension has no local tool implementations and never sends arguments.
export default function (pi) {
  const names = new Set(nexusBridge.tools.map(tool => tool.name));
  const unavailable = () => new Error("NexusRouter tool unavailable; inspect the host journal");
  pi.on("session_start", () => { pi.setActiveTools([...names]); });
  pi.on("tool_call", event => {
    if (!names.has(event.toolName) || typeof event.toolCallId !== "string" || event.toolCallId.includes("/") || event.parentToolCallId) {
      return { block: true, reason: "Only host-registered model tool calls are allowed" };
    }
  });
  for (const tool of nexusBridge.tools) {
    pi.registerTool({
      name: tool.name,
      label: tool.name,
      description: tool.description,
      parameters: tool.parameters,
      exposure: "model-only",
      executionMode: "sequential",
      async execute(callId, _arguments, signal) {
        if (typeof callId !== "string" || !/^[^\s/\u0000-\u001f\u007f]{1,256}$/u.test(callId)) throw unavailable();
        let data;
        try {
          const deadline = AbortSignal.timeout(nexusBridge.timeout_ms);
          const response = await fetch(nexusBridge.base_url + "/tool", {
            method: "POST",
            redirect: "error",
            headers: { "Content-Type": "application/json", "Authorization": "Bearer " + nexusBridge.token },
            body: JSON.stringify({ call_id: callId }),
            signal: signal ? AbortSignal.any([signal, deadline]) : deadline,
          });
          if (!response.ok || response.headers.get("content-type")?.split(";")[0] !== "application/json" || !response.body) throw unavailable();
          const reader = response.body.getReader();
          const chunks = []; let length = 0;
          try {
            for (;;) {
              const { done, value } = await reader.read();
              if (done) break;
              length += value.byteLength;
              if (length > 8 * 1024 * 1024) throw unavailable();
              chunks.push(value);
            }
          } finally { await reader.cancel(); }
          const bytes = new Uint8Array(length); let offset = 0;
          for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
          data = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
          if (!data || typeof data !== "object" || Array.isArray(data) || Object.keys(data).sort().join(",") !== "content,end_tool_use,failed" || typeof data.content !== "string" || typeof data.failed !== "boolean" || typeof data.end_tool_use !== "boolean" || new TextEncoder().encode(data.content).byteLength > 1024 * 1024) throw unavailable();
        } catch { throw unavailable(); }
        if (data.end_tool_use && !data.failed) pi.setActiveTools([]);
        return { content: [{ type: "text", text: data.content }], isError: data.failed, details: undefined };
      },
    });
  }
}
