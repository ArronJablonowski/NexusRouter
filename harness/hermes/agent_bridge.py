"""Pinned Hermes embedding; the parent owns tools, admission and canonical state."""
import contextlib
import http.client
import json
import re
import sys
import threading
from urllib.parse import urlsplit


def unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError("duplicate key")
        out[key] = value
    return out


def main():
    config = json.loads(sys.stdin.buffer.read(1048577), object_pairs_hook=unique)
    expected = {"source", "base_url", "api_key", "model", "prompt", "max_output_tokens", "timeout_seconds", "tools", "tool_token", "max_turns"}
    if set(config) != expected:
        raise ValueError("invalid bridge config")
    endpoint = urlsplit(config["base_url"])
    if (endpoint.scheme != "http" or endpoint.hostname != "127.0.0.1" or not endpoint.port
            or endpoint.path != "/v1" or endpoint.username or endpoint.password or endpoint.query or endpoint.fragment):
        raise ValueError("private gateway required")
    if (type(config["max_turns"]) is not int or not 1 <= config["max_turns"] <= 64
            or type(config["max_output_tokens"]) is not int or not 1 <= config["max_output_tokens"] <= 65536
            or type(config["timeout_seconds"]) is not int or not 1 <= config["timeout_seconds"] <= 900
            or not re.fullmatch(r"[a-zA-Z0-9_-]{20,128}", config["tool_token"])
            or not isinstance(config["tools"], list) or not 1 <= len(config["tools"]) <= 128):
        raise ValueError("invalid bounds")
    sys.path.insert(0, config["source"])
    import hermes_bootstrap
    import model_tools
    from tools.registry import registry
    from tools.approval_context import _approval_tool_call_id
    from run_agent import AIAgent

    specs = {"nexus__" + t["name"]: t for t in config["tools"]}
    if len(specs) != len(config["tools"]):
        raise ValueError("duplicate tools")
    lock = threading.Lock()
    proposed = {}
    executed = {}
    fault = []
    ended = False

    class HostAgent(AIAgent):
        @staticmethod
        def _deduplicate_tool_calls(calls):
            # Native name/argument dedup would drop distinct verified calls.
            # Host IDs and the durable journal own duplicate-effect prevention.
            with lock:
                for call in calls:
                    call_id = call.id
                    if (fault or ended or not isinstance(call_id, str)
                            or not re.fullmatch(r"[^\s\x00-\x1f\x7f]{1,256}", call_id)
                            or call_id in proposed or len(proposed) >= 128 or call.function.name not in specs):
                        fault.append(True)
                        raise RuntimeError("unbound proposal")
                    args = json.loads(call.function.arguments, object_pairs_hook=unique)
                    if not isinstance(args, dict):
                        fault.append(True)
                        raise RuntimeError("invalid proposal arguments")
                    proposed[call_id] = (call.function.name, args)
            return calls

    def make_handler(name):
        def handler(args, **kwargs):
            nonlocal ended
            connection = None
            call_id = _approval_tool_call_id.get()
            try:
                with lock:
                    if fault or ended or call_id in executed or proposed.get(call_id) != (name, args):
                        raise RuntimeError("unbound invocation")
                connection = http.client.HTTPConnection(endpoint.hostname, endpoint.port, timeout=config["timeout_seconds"])
                connection.request("POST", "/v1/tool", body=json.dumps({"call_id": call_id}), headers={"Authorization": "Bearer " + config["tool_token"], "Content-Type": "application/json"})
                response = connection.getresponse()
                body = response.read(8 * 1024 * 1024 + 1)
                if response.status != 200 or response.getheader("Content-Type", "").split(";")[0] != "application/json" or len(body) > 8 * 1024 * 1024:
                    raise RuntimeError("host refused")
                result = json.loads(body.decode("utf-8", errors="strict"), object_pairs_hook=unique)
                if (set(result) != {"content", "failed", "end_tool_use"}
                        or type(result["content"]) is not str or len(result["content"].encode("utf-8")) > 1024 * 1024
                        or type(result["failed"]) is not bool or type(result["end_tool_use"]) is not bool):
                    raise RuntimeError("invalid host result")
                with lock:
                    ended = ended or (result["end_tool_use"] and not result["failed"])
                    executed[call_id] = True
                return json.dumps({"error" if result["failed"] else "result": result["content"]})
            except Exception:
                with lock:
                    fault.append(True)
                raise RuntimeError("NexusRouter tool unavailable; inspect host journal") from None
            finally:
                if connection is not None:
                    connection.close()
        return handler

    for name, spec in specs.items():
        registry.register(name=name, toolset="nexus_host", schema={"name": name, "description": spec["description"], "parameters": spec["parameters"]}, handler=make_handler(name))
    agent = HostAgent(base_url=config["base_url"], api_key=config["api_key"], provider="custom", api_mode="chat_completions", model=config["model"], max_iterations=config["max_turns"], max_tokens=config["max_output_tokens"], enabled_toolsets=["nexus_host"], disabled_toolsets=[], save_trajectories=False, verbose_logging=False, quiet_mode=True, skip_context_files=True, load_soul_identity=False, skip_memory=True, skip_background_review=True, checkpoints_enabled=False, run_budget_seconds=config["timeout_seconds"], reasoning_config={"enabled": False})
    if agent.valid_tool_names != set(specs):
        raise ValueError("unexpected native tool catalogue")
    result = agent.run_conversation(config["prompt"])
    if (fault or set(proposed) != set(executed) or result.get("completed") is not True
            or any(result.get(k) for k in ["failed", "partial", "interrupted", "response_transformed"])
            or result.get("model") != config["model"] or result.get("requested_model") != config["model"]
            or result.get("served_model") not in [None, config["model"]] or result.get("provider") != "custom"):
        raise ValueError("incomplete native execution")
    messages = result["messages"]
    if not messages or messages[0].get("role") != "user" or messages[0].get("content") != config["prompt"]:
        raise ValueError("unexpected initial native context")
    return {"version": 1, "model": config["model"], "text": result["final_response"], "messages": messages[1:]}


if __name__ == "__main__":
    # Native diagnostics never mix with the bounded machine envelope.
    with contextlib.redirect_stdout(sys.stderr):
        output = main()
    print(json.dumps(output, ensure_ascii=False, separators=(",", ":")))
