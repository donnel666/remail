"""Run native provider-call methods offline and check observation stays event-local."""

import ast
import asyncio
import copy
import logging
import os
import sys
from contextlib import suppress
from dataclasses import dataclass, field
from pathlib import Path
from types import ModuleType, SimpleNamespace as NS

import pytest

from . import diagnostics, native_trace
from .native_trace import install_native_trace


@dataclass
class Response:
    completion_text: str = ""
    role: str = "assistant"
    is_chunk: bool = False
    tools_call_ids: list = field(default_factory=list)
    tools_call_name: list = field(default_factory=list)
    tools_call_args: list = field(default_factory=list)
    raw_completion: dict = field(default_factory=dict)


class ToolSet:
    def __init__(self, tools=()):
        self.tools = list(tools)

    def add_tool(self, tool):
        self.tools.append(tool)

    def get_tool(self, name):
        return next((tool for tool in self.tools if tool.name == name), None)

    def openai_schema(self):
        return [
            {
                "type": "function",
                "function": {
                    "name": tool.name,
                    "description": tool.description,
                    "parameters": tool.parameters,
                },
            }
            for tool in self.tools
        ]


class Provider:
    def __init__(self, respond, name="model-one"):
        self.respond, self.name = respond, name
        self.provider_config = {"id": name, "api_key": "never-copy-shared-config"}
        self.calls = []

    def meta(self):
        return NS(id=self.name)

    def get_model(self):
        return self.name

    async def text_chat(self, **kwargs):
        self.calls.append(kwargs)
        return await self.respond(kwargs)


class Event:
    def __init__(self, umo):
        self.unified_msg_origin = umo
        self.extras = {
            diagnostics.TRACE_KEY: (NS(enabled=True, closed=False), "trace", 0)
        }

    def get_extra(self, key, default=None):
        return self.extras.get(key, default)

    def set_extra(self, key, value):
        self.extras[key] = value


@pytest.fixture
def capture(monkeypatch):
    registry, rows = {}, {}
    module = ModuleType("astrbot.core.pipeline.process_stage.follow_up")
    module._ACTIVE_AGENT_RUNNERS = registry
    monkeypatch.setitem(sys.modules, module.__name__, module)
    response_module = ModuleType("astrbot.core.provider.entities")
    response_module.LLMResponse = Response
    monkeypatch.setitem(sys.modules, response_module.__name__, response_module)

    def record(event, stage, outcome, **details):
        rows[event].append(
            {
                "stage": stage,
                "outcome": outcome,
                "details": diagnostics.snapshot_response(details),
            }
        )

    monkeypatch.setattr(diagnostics, "trace_note", record)

    def make(provider, umo="qq:GroupMessage:100_200", cls=NS, bounded=False):
        event = Event(umo)
        if bounded:
            event.extras.update(
                _remail_owned=True,
                _remail_llm_timeout=90,
                _remail_llm_deadline=diagnostics.monotonic() + 240,
            )
        rows[event] = []
        runner = cls()
        runner.run_context = NS(context=NS(event=event), messages=[])
        runner.provider, runner.fallback_providers = provider, []
        runner.req = NS(
            session_id=umo, extra_user_content_parts=[], model=None, func_tool=None
        )
        runner._abort_signal = asyncio.Event()
        runner.streaming = False
        runner.request_max_retries = 0

        async def no_tools(*args, **kwargs):
            for result in ():
                yield result

        runner._handle_function_tools = no_tools
        registry[umo] = runner
        return event, runner

    return NS(rows=rows, registry=registry, make=make)


def _native_runner_class():
    source = os.environ.get("ASTRBOT_SOURCE")
    if not source:
        pytest.skip(
            "Set ASTRBOT_SOURCE to an actual AstrBot checkout for native checks"
        )
    from tenacity import (
        AsyncRetrying,
        retry_if_exception_type,
        stop_after_attempt,
        wait_exponential,
    )

    path = Path(source) / "astrbot/core/agent/runners/tool_loop_agent_runner.py"
    native = next(
        node
        for node in ast.parse(path.read_text()).body
        if isinstance(node, ast.ClassDef) and node.name == "ToolLoopAgentRunner"
    )
    names = {
        "_await_or_stop",
        "_iter_llm_responses",
        "_iter_llm_responses_with_fallback",
        "_sanitize_contexts_for_provider",
        "_func_tool_for_provider",
        "_close_executor",
        "_is_stop_requested",
        "_resolve_tool_exec",
        "_build_tool_subset",
        "_build_tool_requery_context",
        "_has_meaningful_assistant_reply",
        "_sanitize_malformed_tool_calls",
    }
    methods = [
        node
        for node in native.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name in names
    ]
    assert {node.name for node in methods} == names
    constants = [
        node
        for node in native.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id.isupper()
            for target in node.targets
        )
    ]
    module = ast.Module(
        body=[
            ast.ImportFrom(
                module="__future__", names=[ast.alias(name="annotations")], level=0
            ),
            ast.ClassDef(
                name="NativeRunner",
                bases=[],
                keywords=[],
                body=[*constants, *methods],
                decorator_list=[],
            ),
        ],
        type_ignores=[],
    )
    namespace = {
        "asyncio": asyncio,
        "copy": copy,
        "suppress": suppress,
        "ToolSet": ToolSet,
        "LLMResponse": Response,
        "logger": logging.getLogger(__name__),
        "AsyncRetrying": AsyncRetrying,
        "retry_if_exception_type": retry_if_exception_type,
        "stop_after_attempt": stop_after_attempt,
        "wait_exponential": wait_exponential,
        "EmptyModelOutputError": type("EmptyModelOutputError", (Exception,), {}),
    }
    exec(compile(ast.fix_missing_locations(module), str(path), "exec"), namespace)
    return namespace["NativeRunner"]


def test_native_two_rounds_and_skills_like_capture_actual_payloads(capture):
    async def run():
        tool_call = Response(
            "raw draft",
            tools_call_ids=["call-1"],
            tools_call_name=["remail_projects"],
            tools_call_args=[{"query": "QQ 123456", "extra": "untouched"}],
            raw_completion={"raw": "unredacted original"},
        )
        responses = [tool_call, Response("second round"), Response("parameter requery")]

        async def respond(kwargs):
            assert capture.rows[event][-1]["outcome"] == "started", (
                "input must precede provider execution"
            )
            return responses.pop(0)

        provider = Provider(respond)
        event, runner = capture.make(provider, cls=_native_runner_class())
        tool = NS(
            name="remail_projects",
            description="full description",
            parameters={"type": "object", "properties": {"query": {"type": "string"}}},
        )
        runner.req.func_tool = ToolSet([tool])
        runner.run_context.messages = [
            {"role": "system", "content": "原始系统提示词"},
            {"role": "user", "content": "QQ 123456 的完整问题"},
        ]
        assert install_native_trace(event, runner.run_context)
        observer = runner.provider
        assert (
            install_native_trace(event, runner.run_context)
            and runner.provider is observer
        )
        assert runner.provider.provider_config is provider.provider_config
        first = [response async for response in runner._iter_llm_responses()][0]
        assert first is tool_call
        calls = event.get_extra("_remail_react_tool_calls")
        assert (
            calls[0]["id"] == "call-1"
            and calls[0]["actionId"] == capture.rows[event][0]["details"]["actionId"]
        )
        first.completion_text = "business gate changed output"
        first.raw_completion["raw"] = "changed after return"
        first.tools_call_args[0]["query"] = "changed argument"
        assert calls[0]["arguments"]["query"] == "QQ 123456"
        runner.run_context.messages.extend(
            [
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "call-1",
                            "type": "function",
                            "function": {
                                "name": "remail_projects",
                                "arguments": '{"query":"QQ 123456"}',
                            },
                        }
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "call-1",
                    "content": '{"private":"raw tool result"}',
                },
            ]
        )
        second = [response async for response in runner._iter_llm_responses()][0]
        assert second.completion_text == "second round"
        runner._tool_schema_param_set = ToolSet([tool])
        result, _ = await runner._resolve_tool_exec(tool_call)
        assert result.completion_text == "parameter requery"
        starts = [
            row["details"] for row in capture.rows[event] if row["outcome"] == "started"
        ]
        ends = [
            row["details"]
            for row in capture.rows[event]
            if row["outcome"] == "completed"
        ]
        assert [row["attempt"] for row in starts] == [1, 2, 3]
        assert len(starts[0]["input"]["contexts"]) == 2
        assert (
            starts[1]["input"]["contexts"][-1]["content"]
            == '{"private":"raw tool result"}'
        )
        assert (
            starts[2]["input"]["contexts"][0]["content"]
            == provider.calls[2]["contexts"][0]["content"]
        )
        assert "remail_projects" in starts[2]["input"]["contexts"][0]["content"]
        assert starts[0]["input"]["func_tool"] == diagnostics.snapshot_response(
            runner.req.func_tool
        )
        assert provider.calls[0]["func_tool"] is runner.req.func_tool
        assert provider.calls[0]["abort_signal"] is runner._abort_signal
        assert ends[0]["output"]["completion_text"] == "raw draft"
        assert ends[0]["output"]["raw_completion"] == {"raw": "unredacted original"}
        assert "never-copy-shared-config" not in str(capture.rows[event])
        assert "text_chat" not in vars(provider)

    asyncio.run(run())


def test_shared_provider_and_fallback_stay_isolated(capture):
    async def run():
        async def respond(kwargs):
            await asyncio.sleep(0)
            return Response(kwargs["prompt"])

        provider = Provider(respond)
        a, ra = capture.make(provider, "qq:GroupMessage:111_200")
        b, rb = capture.make(provider, "qq:GroupMessage:222_200")
        assert install_native_trace(a, ra.run_context)
        assert install_native_trace(b, rb.run_context)
        await asyncio.gather(
            ra.provider.text_chat(prompt="111 original"),
            rb.provider.text_chat(prompt="222 original"),
        )
        assert (
            capture.rows[a][-1]["details"]["output"]["completion_text"]
            == "111 original"
        )
        assert (
            capture.rows[b][-1]["details"]["output"]["completion_text"]
            == "222 original"
        )
        assert a.get_extra("_remail_react_action_id") != b.get_extra(
            "_remail_react_action_id"
        )
        assert "text_chat" not in vars(provider)

        async def unavailable(kwargs):
            raise RuntimeError("first model raw failure")

        async def recovered(kwargs):
            return Response("fallback original")

        event, runner = capture.make(Provider(unavailable), cls=_native_runner_class())
        fallback = Provider(recovered, "fallback-model")
        runner.fallback_providers = [fallback]
        assert install_native_trace(event, runner.run_context)
        result = [
            response async for response in runner._iter_llm_responses_with_fallback()
        ]
        assert result[0].completion_text == "fallback original"
        assert any(
            row["outcome"] == "failed"
            and row["details"]["error"] == "first model raw failure"
            for row in capture.rows[event]
        )
        assert capture.rows[event][-2]["details"]["providerId"] == "fallback-model"
        assert "model" not in fallback.calls[0], (
            "native fallback payload must remain unchanged"
        )
        assert "text_chat" not in vars(fallback)

    asyncio.run(run())


@pytest.mark.parametrize(
    "error",
    [
        RuntimeError("private provider error"),
        TimeoutError("provider timeout without a ReMail deadline"),
        asyncio.CancelledError("cancelled original"),
    ],
)
def test_provider_exception_and_cancellation_identity(capture, error, monkeypatch):
    async def run():
        async def respond(kwargs):
            raise error

        event, runner = capture.make(Provider(respond))
        assert install_native_trace(event, runner.run_context)
        with pytest.raises(type(error)) as raised:
            await runner.provider.text_chat(prompt="original input")
        assert raised.value is error
        failed = next(
            row for row in reversed(capture.rows[event]) if row["stage"] == "react"
        )
        assert failed["outcome"] == (
            "cancelled" if isinstance(error, asyncio.CancelledError) else "failed"
        )
        assert failed["details"]["error"] == str(error)
        assert type(error).__name__ in failed["details"]["traceback"]
        ends = [row for row in capture.rows[event] if row["stage"] == "end"]
        if isinstance(error, asyncio.CancelledError):
            assert len(ends) == 1 and ends[0]["outcome"] == "cancelled"
            assert ends[0]["details"]["reason"] == "provider_cancelled"

            def fail_finish(*args, **kwargs):
                raise RuntimeError("synthetic recording failure")

            monkeypatch.setattr(diagnostics, "trace_finish", fail_finish)
            with pytest.raises(asyncio.CancelledError) as repeated:
                await runner.provider.text_chat(prompt="recording failure")
            assert repeated.value is error
        else:
            assert ends == [], "a provider failure may still recover via fallback"

    asyncio.run(run())


def test_stream_chunks_snapshot_identity_and_close(capture):
    async def run():
        chunks = [
            Response("chunk 原文", is_chunk=True),
            Response("final 原文", raw_completion={"raw": "final"}),
        ]

        class StreamingProvider(Provider):
            async def text_chat_stream(self, **kwargs):
                try:
                    for chunk in chunks:
                        yield chunk
                finally:
                    self.closed = True

        provider = StreamingProvider(None)
        event, runner = capture.make(provider, cls=_native_runner_class())
        assert install_native_trace(event, runner.run_context)
        runner.streaming = True
        returned = [response async for response in runner._iter_llm_responses()]
        assert returned[0] is chunks[0] and returned[1] is chunks[1]
        returned[0].completion_text = "changed later"
        assert provider.closed
        assert capture.rows[event][1]["outcome"] == "chunk"
        assert (
            capture.rows[event][1]["details"]["output"]["completion_text"]
            == "chunk 原文"
        )
        assert capture.rows[event][-1]["outcome"] == "completed"
        assert not any(row["stage"] == "end" for row in capture.rows[event])
        early, early_runner = capture.make(
            StreamingProvider(None), "qq:FriendMessage:333"
        )
        assert install_native_trace(early, early_runner.run_context)
        stream = early_runner.provider.text_chat_stream(prompt="close early")
        await anext(stream)
        await stream.aclose()
        assert early_runner.provider.closed
        assert capture.rows[early][-1]["outcome"] == "cancelled"
        assert capture.rows[early][-1]["stage"] == "end"
        assert capture.rows[early][-1]["details"]["reason"] == "provider_cancelled"

    asyncio.run(run())


def test_install_is_disabled_or_visible_on_registry_mismatch(capture):
    provider = Provider(None)
    event, runner = capture.make(provider)
    event.extras[diagnostics.TRACE_KEY][0].enabled = False
    assert not install_native_trace(event, runner.run_context)
    assert capture.rows[event] == [] and runner.provider is provider
    event.extras[diagnostics.TRACE_KEY][0].enabled = True
    assert not install_native_trace(event, NS(context=NS(event=event)))
    assert runner.provider is provider
    assert capture.rows[event][-1]["details"]["reason"] == "capture_unavailable"
    del capture.registry[event.unified_msg_origin]
    assert not install_native_trace(event, runner.run_context)
    assert "No active native runner" in capture.rows[event][-1]["details"]["error"]
    capture.registry[event.unified_msg_origin] = runner
    del runner._handle_function_tools
    assert not install_native_trace(event, runner.run_context)
    assert "tool-call iterator" in capture.rows[event][-1]["details"]["error"]


def test_tool_iterator_uses_current_id_after_a_rejected_same_name_call(capture):
    async def run():
        event, runner = capture.make(Provider(None))
        event.set_extra("_remail_react_action_id", "model-round")
        first, second = [
            NS(message_chain=NS(type="tool_call", chain=[NS(data={
                "id": call_id, "name": "remail_projects", "args": arguments,
            })]))
            for call_id, arguments in (("rejected", {"query": []}), ("executed", {"query": "actual"}))
        ]
        observed, closed = [], []

        async def native_tools(*args, **kwargs):
            try:
                yield first
                # Native validation rejects this call before reaching the decorator.
                yield second
                observed.append(event.get_extra("_remail_active_tool_call"))
            finally:
                closed.append(True)

        runner._handle_function_tools = native_tools
        assert install_native_trace(event, runner.run_context)
        wrapped = runner._handle_function_tools
        assert install_native_trace(event, runner.run_context)
        assert runner._handle_function_tools is wrapped
        results = [result async for result in wrapped(None, None)]
        assert results[0] is first and results[1] is second
        assert observed == [{"id": "executed", "name": "remail_projects", "arguments": {"query": "actual"}, "actionId": "model-round"}]
        assert event.get_extra("_remail_native_tool_observer") is True
        assert event.get_extra("_remail_active_tool_call") is None and closed == [True]
        stream = wrapped(None, None)
        assert await anext(stream) is first
        await stream.aclose()
        assert event.get_extra("_remail_active_tool_call") is None and closed == [True, True]

    asyncio.run(run())


@pytest.mark.parametrize("error", [RuntimeError("native tool error"), asyncio.CancelledError("native tool cancellation")])
def test_tool_iterator_preserves_errors_and_clears_active_identity(capture, error):
    async def run():
        event, runner = capture.make(Provider(None))

        async def native_tools(*args, **kwargs):
            yield NS(message_chain=NS(type="tool_call", chain=[NS(data={
                "id": "exact-id", "name": "remail_projects", "args": {},
            })]))
            raise error

        runner._handle_function_tools = native_tools
        assert install_native_trace(event, runner.run_context)
        stream = runner._handle_function_tools(None, None)
        await anext(stream)
        with pytest.raises(type(error)) as raised:
            await anext(stream)
        assert raised.value is error
        assert event.get_extra("_remail_active_tool_call") is None

    asyncio.run(run())


@pytest.mark.parametrize("streaming", [False, True])
def test_provider_deadline_cancels_work_and_returns_one_safe_completion(
    capture, monkeypatch, streaming
):
    async def run():
        closed = []

        async def respond(kwargs):
            try:
                await asyncio.Event().wait()
            finally:
                closed.append(True)

        class StreamingProvider(Provider):
            async def text_chat_stream(self, **kwargs):
                yield await self.text_chat(**kwargs)

        provider = StreamingProvider(respond)
        original_config = provider.provider_config.copy()
        event, runner = capture.make(provider, bounded=True)
        monkeypatch.setattr(diagnostics, "llm_time_budget", lambda *_: 0.01)
        assert install_native_trace(event, runner.run_context)
        if streaming:
            returned = [
                response
                async for response in runner.provider.text_chat_stream(prompt="input")
            ]
        else:
            returned = [await runner.provider.text_chat(prompt="input")]
        assert len(returned) == 1 and isinstance(returned[0], Response)
        assert returned[0].role == "assistant" and not returned[0].is_chunk
        assert returned[0].completion_text == diagnostics.LLM_TIMEOUT_TEXT
        assert returned[0].tools_call_name == []
        assert event.get_extra("_remail_llm_timeout_stage") == "react"
        assert event.get_extra("_remail_react_tool_calls") == []
        assert closed == [True] and provider.calls == [{"prompt": "input"}]
        assert provider.provider_config == original_config
        assert "text_chat" not in vars(provider)
        assert [row["outcome"] for row in capture.rows[event]] == [
            "started", "failed"
        ]
        failure = capture.rows[event][-1]["details"]
        assert failure["reason"] == "llm_timeout"
        assert failure["errorType"] == "LLMDeadlineExceeded"
        assert not any(row["stage"] == "end" for row in capture.rows[event]), (
            "the normal native completion still needs to gate, save and send"
        )

    asyncio.run(run())


def test_exhausted_owned_deadline_works_without_diagnostics_or_provider_fallback(
    capture,
):
    async def run():
        async def respond(kwargs):
            return Response("unrelated event still works")

        provider = Provider(respond)
        fallback = Provider(respond, "fallback")
        event, runner = capture.make(
            provider, cls=_native_runner_class(), bounded=True
        )
        event.extras[diagnostics.TRACE_KEY][0].enabled = False
        event.set_extra("_remail_llm_deadline", diagnostics.monotonic() - 1)
        runner.fallback_providers = [fallback]
        del runner._handle_function_tools
        assert install_native_trace(event, runner.run_context)
        returned = [
            response async for response in runner._iter_llm_responses_with_fallback()
        ]
        assert len(returned) == 1 and returned[0].role == "assistant"
        assert returned[0].completion_text == diagnostics.LLM_TIMEOUT_TEXT
        assert provider.calls == [] and fallback.calls == []
        assert event.get_extra("_remail_native_tool_observer") is None

        other, other_runner = capture.make(provider, "qq:FriendMessage:another")
        other.extras[diagnostics.TRACE_KEY][0].enabled = False
        other.set_extra("_remail_llm_deadline", event.get_extra("_remail_llm_deadline"))
        assert not install_native_trace(other, other_runner.run_context)
        assert other_runner.provider is provider
        response = await other_runner.provider.text_chat(prompt="normal")
        assert response.completion_text == "unrelated event still works"
        assert other.get_extra("_remail_llm_timeout_stage") is None
        assert "text_chat" not in vars(provider)

    asyncio.run(run())


@pytest.mark.parametrize("streaming", [False, True])
def test_active_deadline_preserves_external_cancellation(capture, streaming):
    async def run():
        entered, closed = asyncio.Event(), []

        async def respond(kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                closed.append(True)

        class StreamingProvider(Provider):
            async def text_chat_stream(self, **kwargs):
                yield await self.text_chat(**kwargs)

        event, runner = capture.make(StreamingProvider(respond), bounded=True)
        assert install_native_trace(event, runner.run_context)
        stream = runner.provider.text_chat_stream() if streaming else None
        operation = anext(stream) if streaming else runner.provider.text_chat()
        task = asyncio.create_task(operation)
        await entered.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert closed == [True]
        assert event.get_extra("_remail_llm_timeout_stage") is None
        assert [row["outcome"] for row in capture.rows[event]] == [
            "started", "cancelled", "cancelled"
        ]
        assert capture.rows[event][-1]["stage"] == "end"

    asyncio.run(run())


def test_stream_uses_one_deadline_across_chunks_and_native_anext_tasks(
    capture, monkeypatch
):
    async def run():
        clock, deadlines, closed = [100.0], [], []
        original_wait_for = asyncio.wait_for
        monkeypatch.setattr(native_trace, "monotonic", lambda: clock[0])
        monkeypatch.setattr(diagnostics, "llm_time_budget", lambda *_: 90.0)

        async def wait_for(awaitable, timeout):
            deadlines.append(timeout)
            return await original_wait_for(awaitable, timeout=1)

        monkeypatch.setattr(asyncio, "wait_for", wait_for)

        class StreamingProvider(Provider):
            async def text_chat_stream(self, **kwargs):
                try:
                    yield Response("first", is_chunk=True)
                    yield Response("second", is_chunk=True)
                    pytest.fail("the expired deadline must prevent another read")
                finally:
                    closed.append(True)

        event, runner = capture.make(StreamingProvider(None), bounded=True)
        assert install_native_trace(event, runner.run_context)
        stream = runner.provider.text_chat_stream()
        first = await asyncio.create_task(anext(stream))
        clock[0] = 135.0
        second = await asyncio.create_task(anext(stream))
        clock[0] = 191.0
        final = await asyncio.create_task(anext(stream))
        assert first.completion_text == "first" and second.completion_text == "second"
        assert final.role == "assistant" and not final.is_chunk
        assert final.completion_text == diagnostics.LLM_TIMEOUT_TEXT
        assert deadlines == [90.0, 55.0]
        await stream.aclose()
        assert closed == [True]
        assert [row["outcome"] for row in capture.rows[event]] == [
            "started", "chunk", "chunk", "failed"
        ]

    asyncio.run(run())
