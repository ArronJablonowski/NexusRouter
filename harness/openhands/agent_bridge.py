"""Pinned OpenHands SDK bridge. Parent owns admission, gateway and process bounds."""
import importlib.metadata
import json
import sys
import http.client
import re
from urllib.parse import urlsplit


def main():
    if importlib.metadata.version("openhands-sdk") != "1.50.1":
        raise ValueError("unsupported OpenHands SDK")
    config = json.loads(sys.stdin.buffer.read(1048577))
    if set(config) != {"base_url", "api_key", "model", "workspace", "max_output_tokens", "context_tokens", "timeout_seconds", "tools", "tool_token", "max_turns"}:
        raise ValueError("invalid bridge config")
    endpoint = urlsplit(config["base_url"])
    if (endpoint.scheme != "http" or endpoint.hostname != "127.0.0.1"
            or not endpoint.port or endpoint.path != "/v1"
            or endpoint.username or endpoint.password or endpoint.query or endpoint.fragment):
        raise ValueError("bridge requires private loopback gateway")
    if type(config["max_output_tokens"]) is not int or not 1 <= config["max_output_tokens"] <= 65536:
        raise ValueError("invalid output limit")
    if (type(config["context_tokens"]) is not int or config["context_tokens"] < 8192
            or config["max_output_tokens"] >= config["context_tokens"]
            or type(config["timeout_seconds"]) is not int or not 1 <= config["timeout_seconds"] <= 900):
        raise ValueError("invalid host bounds")
    from openhands.sdk import LLM, Agent, Conversation
    from openhands.sdk.tool import Tool, register_tool
    from openhands.sdk.tool.tool import ToolDefinition, ToolExecutor
    from openhands.sdk.tool.client_tool import ClientTool, ClientToolSpec, ClientToolObservation
    if (type(config["max_turns"]) is not int or not 1 <= config["max_turns"] <= 64
            or not re.fullmatch(r"[a-zA-Z0-9_-]{20,128}", config["tool_token"])
            or not isinstance(config["tools"], list) or not 1 <= len(config["tools"]) <= 128):
        raise ValueError("invalid tool configuration")
    specs = {t["name"]: ClientToolSpec(**t) for t in config["tools"]}
    if len(specs) != len(config["tools"]):
        raise ValueError("duplicate tools")
    events = []
    pending = {}
    seen = set()
    executed = {}
    observed = set()
    fault = []
    ended = False

    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate result key")
            result[key] = value
        return result

    class HostExecutor(ToolExecutor):
        def __call__(self, action, conversation=None):
            nonlocal ended
            connection = None
            try:
                entry = pending.pop(id(action))
                if entry[0] is not action or ended or fault:
                    raise RuntimeError("unbound tool call")
                call_id = entry[1]
                connection = http.client.HTTPConnection(endpoint.hostname, endpoint.port, timeout=config["timeout_seconds"])
                # Only the registered host ID crosses the boundary. Child arguments
                # never become tool authority; host gateway owns canonical arguments.
                connection.request("POST", "/v1/tool", body=json.dumps({"call_id": call_id}), headers={"Authorization": "Bearer " + config["tool_token"], "Content-Type": "application/json"})
                response = connection.getresponse()
                body = response.read(8 * 1024 * 1024 + 1)
                if response.status != 200 or response.getheader("Content-Type", "").split(";")[0] != "application/json" or len(body)>8*1024*1024:
                    raise RuntimeError("host tool refused")
                result = json.loads(body.decode("utf-8", errors="strict"), object_pairs_hook=unique_object)
                if (set(result) != {"content", "failed", "end_tool_use"}
                        or type(result["content"]) is not str or len(result["content"].encode("utf-8")) > 1024*1024
                        or type(result["failed"]) is not bool or type(result["end_tool_use"]) is not bool):
                    raise RuntimeError("invalid host result")
                ended = result["end_tool_use"] and not result["failed"]
                executed[call_id] = (result["content"], result["failed"])
                return ClientToolObservation.from_text(text=result["content"], is_error=result["failed"])
            except Exception:
                fault.append(True)
                raise RuntimeError("NexusRouter tool unavailable; inspect host journal") from None
            finally:
                if connection is not None:
                    connection.close()

    class HostTool(ToolDefinition):
        @classmethod
        def create(cls, conv_state=None, **params):
            return [ClientTool.from_spec(specs[params["name"]]).set_executor(HostExecutor())]

    for name in specs:
        register_tool(name, HostTool)


    def consume_event(event):
        if event.kind == "ActionEvent":
            call_id = event.tool_call_id
            if (event.tool_name not in specs or not re.fullmatch(r"[^\s/\x00-\x1f\x7f]{1,256}", call_id)
                    or call_id in seen or len(seen)>=128 or ended or events):
                raise RuntimeError("unsupported native action")
            seen.add(call_id)
            pending[id(event.action)] = (event.action, call_id)
            return
        if event.kind == "ObservationEvent":
            call_id = event.tool_call_id
            if call_id not in executed or call_id in observed or (event.observation.text, event.observation.is_error) != executed[call_id]:
                raise RuntimeError("unbound native observation")
            observed.add(call_id)
            return

        raw = event.model_dump(mode="json")
        if raw["kind"] == "SystemPromptEvent":
            return
        if raw["kind"] != "MessageEvent":
            raise ValueError("unsupported native event")
        if raw["source"] == "user":
            return
        if raw["source"] != "agent" or events:
            raise ValueError("unexpected assistant event")
        message = raw["llm_message"]
        if (message["role"] != "assistant" or message.get("tool_calls")
                or message.get("reasoning_content") or message.get("thinking_blocks")
                or message.get("responses_reasoning_item") or raw.get("extended_content")
                or raw.get("activated_skills") or raw.get("critic_result")):
            raise ValueError("unsupported native result")
        content = message["content"]
        if not content or any(c["type"] != "text" for c in content):
            raise ValueError("expected text-only result")
        events.append({"kind": raw["kind"], "source": raw["source"],
                       "llm_response_id": raw["llm_response_id"],
                       "llm_message": {"role": "assistant",
                           "content": [{"type": "text", "text": c["text"]} for c in content],
                           "tool_calls": None, "reasoning_content": None}})

    def on_event(event):
        try:
            consume_event(event)
        except Exception:
            fault.append(True)
            raise RuntimeError("unsupported OpenHands event") from None

    llm = LLM(model=config["model"], api_key=config["api_key"], base_url=config["base_url"],
              num_retries=0, timeout=config["timeout_seconds"], stream=True,
              max_input_tokens=config["context_tokens"],
              max_output_tokens=config["max_output_tokens"], reasoning_effort=None,
              caching_prompt=False, capability_overrides={"supports_prompt_cache_key": False})
    agent = Agent(llm=llm, tools=[Tool(name=name, params={"name": name}) for name in specs], tool_concurrency_limit=1, include_default_tools=[], mcp_config={},
                  condenser=None, critic=None)
    conversation = Conversation(agent=agent, workspace=config["workspace"], visualizer=None,
                                token_callbacks=[lambda *args: None], max_iteration_per_run=config["max_turns"],
                                callbacks=[on_event])
    try:
        # The gateway replaces this placeholder with authoritative host messages.
        conversation.send_message("Execute the host-provided task.")
        conversation.run()
        if conversation.state.execution_status.value != "finished" or len(events) != 1 or fault or pending or seen != observed:
            raise ValueError("native conversation did not finish")
        result = {"sdk_version": "1.50.1", "model": config["model"],
                  "status": "finished", "events": events}
    finally:
        conversation.close()
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
