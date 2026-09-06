"""Exercise two QQ turns through the actual workflow, gates and native call adapter."""

import asyncio
import json
import sys
from pathlib import Path
from types import MethodType, ModuleType, SimpleNamespace as NS
from unittest.mock import AsyncMock

import httpx

from .diagnostics import DiagnosticLog, TRACE_KEY, trace_api, trace_tool
from .native_trace import install_native_trace
from .persona import CRITIC_SYSTEM_PROMPT, PERSONA_SYSTEM_PROMPT
from .test_entry import At, Chain, Event, MessageType, Plain, Reply, _load
from .test_fae_session_flow import _flow
from .test_native_trace import Provider, Response, ToolSet, _native_runner_class
from .test_security import _fact, _fact_plan
from .test_sessions import native as native


def test_two_turn_recording_keeps_raw_inputs_retries_and_gated_delivery(
    native, tmp_path, monkeypatch
):
    async def run():
        plan = _fact_plan(
            intents=("recharge",), facts=(_fact("recharge", "recharge_config"),)
        )
        invalid_plan = "invalid planner JSON: raw@example.test 123456789 原封不动"
        flow = _flow(native, [plan, invalid_plan, plan, plan, plan])
        functions, plugin = flow.runtime, flow.plugin
        functions.update(
            MessageType=MessageType,
            MessageChain=Chain,
            Plain=Plain,
            At=At,
            Reply=Reply,
            install_native_trace=install_native_trace,
            httpx=httpx,
        )
        for cls in (Plain, At, Reply):
            monkeypatch.setattr(
                cls, "model_dump", lambda self, **kwargs: self.toDict(), raising=False
            )
        monkeypatch.setattr(
            functions["TextPart"],
            "model_dump",
            lambda self, **kwargs: {"type": "text", "text": self.text},
            raising=False,
        )
        functions["_restrict_remail_tools"] = lambda *_: True
        _load(
            Path(__file__).with_name("main.py"),
            {"trace_remail_agent_begin", "remail_recharge_config", "_request"},
            functions,
        )
        log = DiagnosticLog(tmp_path / "recordings")
        plugin.diagnostics = log
        plugin._authorize_event = AsyncMock()
        plugin.remail_intent_contexts = {}
        planner = flow.context.llm_generate
        answer = (
            "去 ReMail 钱包选 USDT（TRON 网络）充值积分即可，支付金额以本次页面为准。"
        )
        normalized_answer = functions["normalize_security_text"](answer)

        async def generate(**kwargs):
            if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
                payload = json.loads(kwargs["prompt"])
                assert payload["authoritativeAnswer"] == normalized_answer
                assert "evidence" not in payload and "agentDraft" not in payload
                return Response(
                    json.dumps(
                        {"answer": answer, "usedEvidence": ["recharge"], "seals": []},
                        ensure_ascii=False,
                    ),
                    raw_completion={"wire": "writer original private@example.test"},
                )
            if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
                payload = json.loads(kwargs["prompt"])
                assert payload["candidateAnswer"] == normalized_answer
                assert payload["approvedAnswer"] == (
                    "" if payload["reviewMode"] == "facts" else normalized_answer
                )
                return Response(
                    json.dumps(
                        {
                            "decision": "approve",
                            "supportedEvidence": ["recharge"],
                            "violations": [],
                        }
                    ),
                    raw_completion={"wire": "critic original 987654321"},
                )
            return await planner(**kwargs)

        flow.context.llm_generate = AsyncMock(side_effect=generate)
        module = ModuleType("astrbot.core.pipeline.process_stage.follow_up")
        module._ACTIVE_AGENT_RUNNERS = {}
        monkeypatch.setitem(sys.modules, module.__name__, module)
        original_request = plugin._request
        tool_call = trace_tool(functions["remail_recharge_config"])
        payment = {
            "enabled": True,
            "paymentMethods": ["epusdt_usdt_tron"],
            "paymentCurrencies": {"epusdt_usdt_tron": "USDT"},
            "minPoints": "10000",
            "feeRate": "0.06",
            "feeCapPoints": "0",
            "tiers": [],
            "redemptionCodePurchaseUrl": "",
            "serverOnlyRaw": "api原文 raw@example.test 123456789",
        }

        async def http_request(method, path, **kwargs):
            return httpx.Response(
                200,
                json=payment,
                request=httpx.Request(method, "https://remail.invalid" + path),
            )

        plugin.client = NS(request=http_request)
        plugin._bot_headers = lambda event: {
            "X-System-Key": "synthetic-key",
            "X-Bot-Subject": event.get_sender_id(),
        }
        plugin._websocket_enabled = lambda: False
        conversation_ids, trace_ids, events = [], [], []
        for turn in (1, 2):
            event = Event("怎么充值", mention="90001", message_id=str(100 + turn))
            event.scope = (
                event.get_platform_id(),
                event.get_platform_name(),
                event.get_self_id(),
                event.get_group_id(),
                event.get_sender_id(),
            )
            event.set_extra("_remail_binding_state", "bound")
            assert functions["_capture_group_request"](plugin, event)
            await functions["require_bound_service_user"](plugin, event)
            plugin._request = original_request
            await functions["classify_mentioned_group_question"](plugin, event)
            assert event.get_extra("_remail_intent_plan_v1") == plan
            assert event.get_extra("_remail_group_trigger_verified") is True
            request = event.get_extra("provider_request")
            request.func_tool = ToolSet(
                [
                    NS(
                        name="remail_recharge_config",
                        description="查询当前充值配置",
                        parameters={"type": "object", "properties": {}},
                    )
                ]
            )
            request.model = None
            await functions["authorize_llm"](plugin, event, request)
            assert not event.is_stopped()
            conversation_ids.append(request.conversation.cid)

            first = Response(
                "先查询当前充值配置。",
                tools_call_ids=[f"call-{turn}"],
                tools_call_name=["remail_recharge_config"],
                tools_call_args=[{}],
                raw_completion={"wire": f"tool model original {turn}"},
            )
            last = Response(
                answer,
                raw_completion={"wire": f"agent original private@example.test {turn}"},
            )
            model_outputs = iter((first, last))

            async def react_model(kwargs):
                return next(model_outputs)

            runner = _native_runner_class()()
            runner.provider = Provider(react_model)
            runner.fallback_providers = []
            runner.req, runner.streaming, runner.request_max_retries = request, False, 0
            runner._abort_signal = asyncio.Event()
            runner.run_context = NS(
                context=NS(event=event),
                messages=[
                    {"role": "system", "content": request.system_prompt},
                    {"role": "user", "content": request.prompt},
                ],
            )

            async def native_tools(_request, response):
                yield NS(message_chain=NS(type="tool_call", chain=[NS(data={
                    "id": response.tools_call_ids[0],
                    "name": response.tools_call_name[0],
                    "args": response.tools_call_args[0],
                })]))
                yield await tool_call(plugin, event)

            runner._handle_function_tools = native_tools
            module._ACTIVE_AGENT_RUNNERS[event.unified_msg_origin] = runner
            await functions["trace_remail_agent_begin"](
                plugin, event, runner.run_context
            )
            assert [item async for item in runner._iter_llm_responses()] == [first]
            plugin._request = MethodType(trace_api(functions["_request"]), plugin)
            tool_steps = [
                item async for item in runner._handle_function_tools(request, first)
            ]
            tool_output = tool_steps[-1]
            runner.run_context.messages.extend(
                [
                    {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": f"call-{turn}",
                                "type": "function",
                                "function": {
                                    "name": "remail_recharge_config",
                                    "arguments": "{}",
                                },
                            }
                        ],
                    },
                    {
                        "role": "tool",
                        "tool_call_id": f"call-{turn}",
                        "content": tool_output,
                    },
                ]
            )
            assert [item async for item in runner._iter_llm_responses()] == [last]
            runner.run_context.messages.append(
                NS(role="assistant", content=last.completion_text)
            )
            await functions["enforce_redemption_channel_priority"](plugin, event, last)
            await functions["snapshot_safe_remail_response"](plugin, event, last)
            await functions["sync_safe_response_history"](
                plugin, event, runner.run_context, last
            )
            result = Chain([Plain("untrusted result decorator text")])
            result.result_content_type = functions["ResultContentType"].LLM_RESULT
            event.set_result(result)
            await functions["finalize_safe_remail_result"](plugin, event)
            assert event.get_result() is None and not event.is_stopped()
            await functions["finalize_safe_remail_result"](plugin, event)
            assert event.sent == [normalized_answer]
            assert [type(part) for part in event.sent_chains[0]] == [Reply, At, Plain]
            assert event.sent_chains[0][0].id == str(100 + turn)
            assert event.sent_chains[0][1].qq == event.get_sender_id()
            assert runner.run_context.messages[-1].content == event.sent[0]
            assert request.contexts == [], (
                "diagnostic records must not enter provider history"
            )
            trace_ids.append(event.get_extra(TRACE_KEY)[1])
            events.append(event)

        assert conversation_ids[0] == conversation_ids[1]
        assert trace_ids[0] != trace_ids[1]
        for turn, (trace_id, event) in enumerate(zip(trace_ids, events), 1):
            rows = [row for row in log.entries if row["traceId"] == trace_id]
            assert {
                "entry",
                "session",
                "intent",
                "background",
                "planner",
                "agent",
                "react",
                "tool",
                "api",
                "privacy",
                "writer",
                "critic",
                "delivery",
                "end",
            } <= {row["stage"] for row in rows}
            assert [row["outcome"] for row in rows if row["stage"] == "end"] == ["sent"]
            compose = next(
                row
                for row in rows
                if row["stage"] == "background"
                and row["details"].get("name") == "compose_background"
                and row["outcome"] == "started"
            )
            assert all(
                row["details"]["parentId"] == compose["details"]["actionId"]
                for row in rows
                if row["stage"] == "background"
                and row["outcome"] == "started"
                and row["details"].get("name") not in {None, "compose_background"}
            )
            react_starts = [
                row
                for row in rows
                if row["stage"] == "react" and row["outcome"] == "started"
                and row["details"].get("name") not in {"final_answer_check", "final_answer_repair"}
            ]
            assert len(react_starts) == 2
            final_checks = [
                row for row in rows
                if row["stage"] == "react" and row["outcome"] == "started"
                and row["details"].get("name") == "final_answer_check"
            ]
            assert len(final_checks) == 1
            assert final_checks[0]["details"]["parentId"] == event.get_extra("_remail_agent_action_id")
            assert json.loads(final_checks[0]["details"]["input"]["prompt"])["reviewMode"] == "facts"
            assert react_starts[0]["details"]["parentId"] == event.get_extra(
                "_remail_agent_action_id"
            )
            tool = next(
                row
                for row in rows
                if row["stage"] == "tool" and row["outcome"] == "started"
            )
            api = next(
                row
                for row in rows
                if row["stage"] == "api" and row["outcome"] == "started"
            )
            assert tool["details"]["parentId"] == react_starts[0]["details"]["actionId"]
            assert tool["details"]["toolCallId"] == f"call-{turn}"
            assert api["details"]["parentId"] == tool["details"]["actionId"]
            assert react_starts[1]["details"]["input"]["contexts"][-1]["role"] == "tool"
            raw = json.dumps(rows, ensure_ascii=False)
            assert f"agent original private@example.test {turn}" in raw
            assert "api原文 raw@example.test 123456789" in raw
            assert "writer original private@example.test" in raw
            assert "critic original 987654321" in raw
            assert all(
                secret not in event.sent[0]
                for secret in ("private@example.test", "raw@example.test", "987654321")
            )
        first_rows = [
            row
            for row in log.entries
            if row["traceId"] == trace_ids[0] and row["stage"] == "planner"
        ]
        starts = [row for row in first_rows if row["outcome"] == "started"]
        assert [row["details"]["attempt"] for row in starts] == [1, 2]
        assert starts[0]["details"]["actionId"] != starts[1]["details"]["actionId"]
        assert invalid_plan in json.dumps(first_rows, ensure_ascii=False)
        log.close()

    asyncio.run(run())


def test_unavailable_native_capture_marks_the_whole_recording_incomplete(
    tmp_path, monkeypatch
):
    for cls in (Plain, At):
        monkeypatch.setattr(
            cls, "model_dump", lambda self, **kwargs: self.toDict(), raising=False
        )
    event = Event("怎么充值", mention="90001")
    log = DiagnosticLog(tmp_path / "capture-failure")
    log.attach(event)
    module = ModuleType("astrbot.core.pipeline.process_stage.follow_up")
    module._ACTIVE_AGENT_RUNNERS = {}
    monkeypatch.setitem(sys.modules, module.__name__, module)
    assert not install_native_trace(event, NS(context=NS(event=event)))
    turn = log.snapshot(view="turns", qq=event.get_sender_id())["items"][0]
    assert not turn["captureComplete"]
    assert "No active native runner" in turn["recordingError"]
    log.close()
