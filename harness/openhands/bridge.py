"""Pinned OpenHands SDK bridge. Parent owns admission, gateway and process bounds."""
import importlib.metadata
import json
import sys
from urllib.parse import urlsplit


def main():
    if importlib.metadata.version("openhands-sdk") != "1.50.1":
        raise ValueError("unsupported OpenHands SDK")
    config = json.loads(sys.stdin.buffer.read(65537))
    if set(config) != {"base_url", "api_key", "model", "workspace", "max_output_tokens", "context_tokens", "timeout_seconds"}:
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
    events = []

    def on_event(event):
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

    llm = LLM(model=config["model"], api_key=config["api_key"], base_url=config["base_url"],
              num_retries=0, timeout=config["timeout_seconds"], stream=True,
              max_input_tokens=config["context_tokens"],
              max_output_tokens=config["max_output_tokens"], reasoning_effort=None,
              caching_prompt=False, capability_overrides={"supports_prompt_cache_key": False})
    agent = Agent(llm=llm, tools=[], include_default_tools=[], mcp_config={},
                  condenser=None, critic=None)
    conversation = Conversation(agent=agent, workspace=config["workspace"], visualizer=None,
                                token_callbacks=[lambda *args: None], max_iteration_per_run=1,
                                callbacks=[on_event])
    try:
        # The gateway replaces this placeholder with authoritative host messages.
        conversation.send_message("Execute the host-provided task.")
        conversation.run()
        if conversation.state.execution_status.value != "finished" or len(events) != 1:
            raise ValueError("native conversation did not finish")
        result = {"sdk_version": "1.50.1", "model": config["model"],
                  "status": "finished", "events": events}
    finally:
        conversation.close()
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
