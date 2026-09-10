"""Offline regression scenarios for private order prefetch and request timeouts."""

import ast
import asyncio
import json
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import AsyncMock

import httpx
import pytest

from .test_entry import _load
from .test_fae_session_flow import Event, MessageType, _flow
from .test_security import _fact, _fact_plan, _load_welcome_functions
from .test_sessions import native as native


def _transport_functions():
    functions, _ = _load_welcome_functions()
    functions.update(
        httpx=httpx, MessageType=MessageType,
        _PendingRequest=lambda **values: NS(state="queued", **values),
        _remove_binding_log_redaction=lambda: None,
    )
    _load(
        Path(__file__).with_name("main.py"),
        {"_request", "_websocket_request", "_WebSocketUnavailable", "ReMailError", "terminate"},
        functions,
    )
    return functions


def test_private_orders_start_after_intent_and_background_reuses_the_task(native):
    plan = _fact_plan(intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private")
    flow = _flow(native, [plan, plan])

    async def run():
        release, entered = asyncio.Event(), asyncio.Event()
        timeline = []
        query = AsyncMock()
        event = Event(group="")

        async def request(method, path, *, event, **kwargs):
            assert method == "GET" and path == "/v1/bot/orders"
            assert event.get_extra("provider_request") is not None
            assert event.get_extra("_remail_authorized") is True
            assert event.get_extra("_remail_initial_intent") == plan
            timeline.append("orders-start")
            await query()
            entered.set()
            await release.wait()
            timeline.append("orders-end")
            return {"available": True, "items": [], "total": 0, "offset": 0}

        original_llm = flow.context.llm_generate

        async def llm(**kwargs):
            phase = json.loads(kwargs["prompt"])["workflowPhase"]
            timeline.append(phase)
            if phase == "intent":
                query.assert_not_awaited()
            return await original_llm(**kwargs)

        flow.plugin._request = request
        flow.context.llm_generate = llm
        workflow = asyncio.create_task(flow.run(flow.plugin, event, event.message_str))
        await asyncio.wait_for(entered.wait(), timeout=1)
        assert timeline == ["intent", "orders-start"]
        release.set()
        assert not (await workflow).failed
        assert timeline == ["intent", "orders-start", "orders-end", "planner"]
        query.assert_awaited_once()
        prefetch = event.get_extra("_remail_order_prefetch")[2]
        assert prefetch.done() and not prefetch.cancelled()
        assert flow.runtime["_start_order_prefetch"](flow.plugin, event) is prefetch
        assert event.get_extra("_remail_prefetched_order_summary")["sourceValid"] is True
        assert event.get_extra("_remail_dynamic_background")["ownOrders"]["total"] == 0
        assert not flow.plugin.order_prefetch_tasks

    asyncio.run(run())


@pytest.mark.parametrize("intent", ["social", "service"])
def test_unneeded_orders_are_never_requested(native, intent):
    plan = _fact_plan(intents=(intent,))
    flow = _flow(native, [plan, plan])

    async def run():
        event = Event(group="")
        flow.plugin._request = AsyncMock()
        result = await asyncio.wait_for(
            flow.run(flow.plugin, event, event.message_str), timeout=1
        )
        assert not result.failed and flow.context.llm_generate.await_count == 2
        assert event.get_extra("_remail_order_prefetch") is None
        assert event.get_extra("_remail_prefetched_order_summary") is None
        assert not getattr(flow.plugin, "order_prefetch_tasks", set())
        assert event.get_extra("_remail_dynamic_background")["ownOrders"] == {
            "status": "not_needed", "sourceValid": False,
        }
        flow.plugin._request.assert_not_awaited()
        flow.plugin._public_api_capability_context.assert_not_awaited()

    asyncio.run(run())


@pytest.mark.parametrize("problem", [None, "api_failure", "scope_change"])
def test_orders_tool_reuses_pending_prefetch_and_preserves_its_errors(native, problem):
    plan = _fact_plan(intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private")
    flow = _flow(native, [plan, plan])

    async def run():
        release = asyncio.Event()
        failure = RuntimeError("synthetic background order failure")

        async def request(*args, **kwargs):
            await release.wait()
            if problem == "api_failure":
                raise failure
            return {"available": True, "items": [], "total": 0, "offset": 0}

        event = Event(group="")
        event.set_extra("_remail_initial_intent", plan)
        flow.plugin._request = AsyncMock(side_effect=request)
        flow.plugin._authorize_event = AsyncMock()
        flow.plugin._private = lambda incoming: not bool(incoming.group)
        try:
            prefetch = flow.runtime["_start_order_prefetch"](flow.plugin, event)
            tool = flow.runtime["remail_orders"]
            result = asyncio.create_task(tool(flow.plugin, event, offset=0))
            await asyncio.sleep(0)
            assert not result.done() and not prefetch.done()
            flow.plugin._request.assert_awaited_once()
            if problem == "scope_change":
                event.sender = "changed-user"
            release.set()
            if problem:
                expected = RuntimeError if problem == "api_failure" else flow.runtime["ReMailError"]
                with pytest.raises(expected):
                    await result
                # Retrieving the task exception in its callback cannot turn a
                # later await into success or trigger another backend request.
                with pytest.raises(expected):
                    await tool(flow.plugin, event, offset=0)
                assert event.get_extra("_remail_prefetched_order_summary") is None
            else:
                payload = json.loads(await result)
                assert payload["sourceValid"] is True and payload["total"] == 0
                assert json.loads(await tool(flow.plugin, event, offset=0)) == payload
            flow.plugin._request.assert_awaited_once()
        finally:
            await flow.runtime["_finish_order_prefetch"](event)

    asyncio.run(run())


@pytest.mark.parametrize("handler", ["reply", "finalize"])
@pytest.mark.parametrize("send_error", [False, True])
def test_reply_paths_finish_prefetch_after_sending_and_preserve_native_save(
    handler, send_error
):
    functions = _transport_functions()

    async def run():
        entered, order = asyncio.Event(), []

        async def request(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                order.append("query-cancelled")

        async def send(*args, **kwargs):
            assert not prefetch.done()
            order.append("send")
            if send_error:
                raise RuntimeError("synthetic send failure")

        async def scoped(event, original_send, text):
            await original_send(text)

        def clear_result():
            result.value = None
            order.append("result-cleared")

        functions["_send_scoped_text"] = scoped
        functions["_install_owned_send_guard"] = AsyncMock(return_value=True)
        plugin = NS(_request=request, order_prefetch_tasks=set())
        event = Event(group="")
        event.set_extra("_remail_initial_intent", _fact_plan(
            intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private"
        ))
        event.set_extra("_remail_owned", True)
        event.set_extra("_remail_canonical_response", "安全答复。")
        event.send = AsyncMock(side_effect=send)
        event.is_stopped = lambda: event.stopped
        result = NS(value=NS(chain=[]))
        event.get_result = lambda: result.value
        event.clear_result = clear_result
        prefetch = functions["_start_order_prefetch"](plugin, event)
        await entered.wait()
        operation = (
            functions["_reply"](event, "安全答复。")
            if handler == "reply"
            else functions["finalize_safe_remail_result"](plugin, event)
        )
        if send_error:
            with pytest.raises(RuntimeError, match="synthetic send failure"):
                await operation
        else:
            await operation
        assert prefetch.cancelled() and not plugin.order_prefetch_tasks
        event.send.assert_awaited_once()
        if handler == "reply":
            assert order == ["send", "query-cancelled"] and event.stopped
        else:
            assert order == ["send", "result-cleared", "query-cancelled"]
            assert result.value is None and not event.stopped

    asyncio.run(run())


@pytest.mark.parametrize("failure", ["rejected", "cancelled", "session"])
def test_workflow_exit_before_context_does_not_start_an_order_query(native, failure):
    plan = _fact_plan(route="ignore", intents=()) if failure == "rejected" else _fact_plan(intents=("service",))
    flow = _flow(native, [plan])

    async def run():
        flow.plugin._request = AsyncMock()
        if failure == "cancelled":
            flow.context.conversation_manager.get_curr_conversation_id = AsyncMock(side_effect=asyncio.CancelledError)
        elif failure == "session":
            flow.db.fail = True
        event = Event(group="")
        if failure == "cancelled":
            with pytest.raises(asyncio.CancelledError):
                await flow.run(flow.plugin, event, event.message_str)
        else:
            result = await flow.run(flow.plugin, event, event.message_str)
            if failure == "rejected":
                assert result.route == "ignore"
            else:
                assert result.failed
        assert event.get_extra("_remail_order_prefetch") is None
        flow.plugin._request.assert_not_awaited()
        assert not getattr(flow.plugin, "order_prefetch_tasks", set())

    asyncio.run(run())


def test_cancellation_during_requested_orders_drains_the_query(native):
    plan = _fact_plan(intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private")
    flow = _flow(native, [plan])

    async def run():
        entered, closed = asyncio.Event(), []

        async def request(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                closed.append(True)

        flow.plugin._request = request
        event = Event(group="")
        task = asyncio.create_task(flow.run(flow.plugin, event, event.message_str))
        await asyncio.wait_for(entered.wait(), timeout=1)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert closed == [True]
        assert event.get_extra("_remail_order_prefetch")[2].cancelled()
        assert not flow.plugin.order_prefetch_tasks

    asyncio.run(run())


def test_prefetch_requires_private_authorization_and_refuses_foreign_or_changed_scope():
    functions = _transport_functions()
    plan = _fact_plan(intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private")

    async def run():
        request = AsyncMock(return_value={"available": True, "items": [], "total": 0, "offset": 0})
        plugin = NS(_request=request, order_prefetch_tasks=set())
        for event in (Event(), Event(group="")):
            event.set_extra("_remail_initial_intent", plan)
            if not event.group:
                event.set_extra("_remail_authorized", False)
            assert functions["_start_order_prefetch"](plugin, event) is None
        unbound = Event(group="")
        unbound.set_extra("_remail_initial_intent", plan)
        unbound.set_extra("_remail_binding_state", "unbound")
        assert functions["_start_order_prefetch"](plugin, unbound) is None
        assert functions["_start_order_prefetch"](plugin, Event(group="")) is None
        request.assert_not_awaited()

        event = Event(group="")
        event.set_extra("_remail_initial_intent", plan)
        task = functions["_start_order_prefetch"](plugin, event)
        other = Event(group="", sender="10002")
        other.set_extra("_remail_initial_intent", plan)
        other.set_extra("_remail_order_prefetch", event.get_extra("_remail_order_prefetch"))
        with pytest.raises(functions["ReMailError"]):
            functions["_start_order_prefetch"](plugin, other)
        await functions["_finish_order_prefetch"](other)
        assert not task.done(), "another event cannot cancel the owner's prefetch"
        event.sender = "10003"
        with pytest.raises(functions["ReMailError"]):
            await task
        request.assert_not_awaited()

    asyncio.run(run())


@pytest.mark.parametrize("transport", ["http", "websocket"])
def test_only_order_get_uses_the_separate_timeout(transport):
    functions = _transport_functions()

    async def run():
        request = AsyncMock(return_value=httpx.Response(200, json={"ok": True}))
        websocket = AsyncMock(return_value={"ok": True})
        plugin = NS(
            request_timeout=10, orders_timeout=120,
            client=NS(request=request, timeout=10),
            _websocket_request=websocket,
            _websocket_enabled=lambda: transport == "websocket",
            _bot_headers=lambda event: {"X-System-Key": "synthetic-test-key", "X-Bot-Subject": event.get_sender_id()},
        )
        event = Event(group="")
        for method, path, expected in (
            ("GET", "/v1/bot/orders", 120),
            ("GET", "/v1/bot/recharges/config", 10),
            ("POST", "/v1/bot/orders", 10),
        ):
            assert await functions["_request"](plugin, method, path, event=event) == {"ok": True}
            transport_call = websocket if transport == "websocket" else request
            assert transport_call.await_args.kwargs["timeout"] == expected
        assert plugin.request_timeout == 10 and plugin.client.timeout == 10

    asyncio.run(run())


def test_websocket_uses_requested_deadline_and_cleans_pending_when_send_is_cancelled(monkeypatch):
    functions = _transport_functions()

    async def run():
        ready = asyncio.Event()
        ready.set()
        plugin = NS(request_timeout=10, websocket_ready={"key": ready}, websocket_pending={})
        original_wait_for, deadlines = asyncio.wait_for, []

        async def wait_for(awaitable, timeout):
            deadlines.append(timeout)
            return await original_wait_for(awaitable, timeout=timeout)

        monkeypatch.setattr(asyncio, "wait_for", wait_for)

        async def respond(key, frame, on_sending):
            on_sending()
            plugin.websocket_pending[frame["id"]].future.set_result({"status": 200, "body": {"ok": True}})

        plugin._send_websocket = respond
        args = (plugin, "key", "GET", "/v1/bot/orders", "10001", "private", "", None, {})
        assert await functions["_websocket_request"](*args, timeout=90) == {"ok": True}
        assert await functions["_websocket_request"](*args) == {"ok": True}
        assert deadlines == [90, 10] and plugin.websocket_pending == {}

        entered, pending = asyncio.Event(), []

        async def blocked(key, frame, on_sending):
            pending.append(plugin.websocket_pending[frame["id"]].future)
            on_sending()
            entered.set()
            await asyncio.Event().wait()

        plugin._send_websocket = blocked
        task = asyncio.create_task(functions["_websocket_request"](*args, timeout=90))
        await entered.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert plugin.websocket_pending == {} and pending[0].cancelled()

    asyncio.run(run())


def test_terminate_awaits_prefetch_cancellation_before_closing_the_client():
    functions = _transport_functions()

    async def run():
        entered, finished = asyncio.Event(), []

        async def request(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                finished.append("query-cancelled")

        async def close():
            assert finished == ["query-cancelled"]
            finished.append("client-closed")

        plugin = NS(
            _request=request, order_prefetch_tasks=set(), diagnostics=None,
            context=NS(registered_web_apis=[]), _remove_entry_guard=None,
            feedback_task=None, websocket_tasks=[], launch_worker=None,
            websocket_pending={}, client=NS(aclose=close),
        )
        event = Event(group="")
        event.set_extra("_remail_initial_intent", _fact_plan(
            intents=("orders",), facts=(_fact("orders", "orders"),), privacy="private"
        ))
        task = functions["_start_order_prefetch"](plugin, event)
        await entered.wait()
        await functions["terminate"](plugin)
        assert task.cancelled() and finished == ["query-cancelled", "client-closed"]

    asyncio.run(run())


@pytest.mark.parametrize("config, expected", [({}, 90), ({"orders_timeout_seconds": 1}, 30), ({"orders_timeout_seconds": 300}, 300), ({"orders_timeout_seconds": 600}, 300)])
def test_order_timeout_configuration_default_and_bounds(config, expected):
    tree = ast.parse(Path(__file__).with_name("main.py").read_text())
    main = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Main")
    constructor = next(node for node in main.body if isinstance(node, ast.FunctionDef) and node.name == "__init__")
    assignment = next(node for node in constructor.body if isinstance(node, ast.Assign)
                      and any(isinstance(target, ast.Attribute) and target.attr == "orders_timeout" for target in node.targets))
    plugin = NS()
    exec(compile(ast.Module(body=[assignment], type_ignores=[]), "main.py", "exec"), {"self": plugin, "config": config})
    assert plugin.orders_timeout == expected
