"""Workflow wiring checks, not an evaluation of real-model intent accuracy."""

import asyncio
import copy
import enum
import json
from types import SimpleNamespace as NS
from unittest.mock import AsyncMock

import pytest

from .sessions import (
    SESSION_REFERENCE_KEY,
    get_session_reference,
    prepare_native_history,
    session_request_matches,
)
from .diagnostics import logged_llm_call, trace_tool
from .test_security import _fact_plan, _load_welcome_functions
from .test_sessions import Event as SessionEvent
from .test_sessions import native as native
from .test_sessions import native_messages as native_messages


class MessageType(enum.Enum):
    GROUP_MESSAGE = "GroupMessage"
    FRIEND_MESSAGE = "FriendMessage"


class Event(SessionEvent):
    def __init__(self, question="我们买的邮箱能用多长时间", **kwargs):
        super().__init__(**kwargs)
        self.message_str = question
        self.stopped = False
        self.set_extra("_remail_authorized", True)
        self.set_extra("_remail_binding_state", "bound")
        if self.group:
            self.set_extra("_remail_group_llm_allowed", True)

    def get_message_type(self):
        return MessageType.GROUP_MESSAGE if self.group else MessageType.FRIEND_MESSAGE

    def stop_event(self):
        self.stopped = True


def _flow(native, responses):
    context, db, _ = native
    runtime, _ = _load_welcome_functions()
    runtime["MessageType"] = MessageType
    order, payloads, replies = [], [], []
    planned_outputs = iter(responses)
    read_history, ensure, bind = (
        runtime[name]
        for name in (
            "read_existing_history",
            "ensure_native_session",
            "bind_native_request",
        )
    )

    async def read(*args, **kwargs):
        order.append("history:read")
        return await read_history(*args, **kwargs)

    async def resolve(*args, **kwargs):
        order.append("session:ensure")
        return await ensure(*args, **kwargs)

    def bind_request(*args, **kwargs):
        order.append("session:bind")
        return bind(*args, **kwargs)

    runtime.update(
        read_existing_history=read,
        ensure_native_session=resolve,
        bind_native_request=bind_request,
    )
    create = db.create_conversation

    async def tracked_create(**kwargs):
        order.append("session:create")
        return await create(**kwargs)

    db.create_conversation = tracked_create

    async def llm(**kwargs):
        payload = json.loads(kwargs["prompt"])
        order.append("llm:" + payload["workflowPhase"])
        payloads.append(payload)
        assert kwargs["tools"] is None and kwargs["contexts"] is None
        # The controlled model response is queued, never inferred from keywords.
        response = next(planned_outputs)
        raw = response if isinstance(response, str) else json.dumps(response.to_dict())
        return NS(role="assistant", completion_text=raw)

    context.get_current_chat_provider_id = AsyncMock(return_value="configured-provider")
    context.llm_generate = AsyncMock(side_effect=llm)

    def data_entry(event, source):
        order.append("data:" + source)
        request = event.get_extra("provider_request")
        assert session_request_matches(event, request, scope=event.scope)

    async def request(_method, path, *, event, **kwargs):
        if path == "/v1/bot/orders":
            order.append("data:" + path)
            assert _method == "GET" and not event.group
            assert event.get_extra("_remail_authorized") is True
            assert event.get_extra("_remail_binding_state") == "bound"
            return {"available": True, "items": [], "total": 0, "offset": 0}
        data_entry(event, path)
        if path == "/v1/bot/projects":
            assert kwargs["params"]["limit"] == 100
            return {"items": [], "total": 0}
        assert path == "/v1/bot/recharges/config"
        return {"enabled": False, "redemptionCodePurchaseUrl": ""}

    async def public_request(path, *, ttl):
        order.append("data:" + path)
        assert ttl == 0
        if path == "/v1/notice":
            return {"notice": ""}
        return {"items": [], "total": 0, "truncated": False}

    async def capabilities(event):
        data_entry(event, "api_capabilities")
        return '{"operations":[]}'

    async def group_context(event, **kwargs):
        data_entry(event, "group_context")
        return {"weak": True, "status": "unavailable", "items": [], "truncated": False}

    async def reply(event, text):
        order.append("reply")
        replies.append(text)
        event.stop_event()

    runtime["load_group_context"] = group_context
    plugin = NS(
        context=context,
        config={},
        _request=AsyncMock(side_effect=request),
        _public_request=AsyncMock(side_effect=public_request),
        _public_api_capability_context=AsyncMock(side_effect=capabilities),
        _reply=AsyncMock(side_effect=reply),
    )
    return NS(
        plugin=plugin,
        context=context,
        db=db,
        runtime=runtime,
        order=order,
        payloads=payloads,
        replies=replies,
        run=runtime["_prepare_fae_workflow"],
    )


@pytest.mark.parametrize("group", ["20001", ""])
@pytest.mark.parametrize("decision", ["ignore", "failed"])
def test_unrelated_or_failed_intent_only_allows_authorized_private_order_prefetch(
    native, group, decision
):
    output = _fact_plan(route="ignore", intents=())
    responses = (
        [output] if decision == "ignore" else ["invalid model output", "still invalid"]
    )
    flow = _flow(native, responses)
    event = Event(question="客户的实际表述由模型理解", group=group)
    result = asyncio.run(flow.run(flow.plugin, event, event.message_str))
    assert result.failed == (decision == "failed") and result.route == "ignore"
    prefetch = ["data:/v1/bot/orders"] if not group else []
    assert flow.order == [*prefetch, "history:read", *(["llm:intent"] * len(responses))]
    assert flow.db.created == 0 and flow.db.rows == {}
    assert flow.plugin._request.await_count == (0 if group else 1)
    assert flow.plugin._public_request.await_count == 0
    assert event.get_extra(SESSION_REFERENCE_KEY) is None
    assert event.get_extra("provider_request") is None
    assert event.get_extra("_remail_dynamic_background") is None
    assert all("dynamicBackground" not in payload for payload in flow.payloads)
    assert all(
        payload["messageContext"]["isGroup"] == bool(group) for payload in flow.payloads
    )


@pytest.mark.parametrize("group", ["20001", ""])
@pytest.mark.parametrize("mode", ["refuse_internal", "refuse_group_mail"])
def test_refusal_paths_keep_their_session_and_prefetch_rules(native, group, mode):
    refusal = _fact_plan(
        intents=(),
        answer_mode=mode,
        privacy="group_sensitive" if mode == "refuse_group_mail" else "public",
    )
    flow = _flow(native, [refusal] * (2 if mode == "refuse_internal" else 1))
    event = Event(group=group)
    actual = asyncio.run(flow.run(flow.plugin, event, event.message_str))
    assert actual.answer_mode == mode
    if mode == "refuse_internal":
        assert not event.stopped and flow.db.created == 1 and not flow.replies
        assert flow.context.llm_generate.await_count == 2
        return
    assert event.stopped
    prefetch = ["data:/v1/bot/orders"] if not group else []
    assert flow.order == [*prefetch, "history:read", "llm:intent", "reply"]
    assert flow.db.created == 0 and flow.db.rows == {}
    assert event.get_extra(SESSION_REFERENCE_KEY) is None
    assert event.get_extra("provider_request") is None
    assert flow.plugin._request.await_count == (0 if group else 1)
    assert flow.plugin._public_request.await_count == 0
    assert len(flow.replies) == 1


@pytest.mark.parametrize("group", ["20001", ""])
def test_accepted_intent_binds_native_request_before_real_background_and_planner(
    native, group
):
    admission, planning = (
        _fact_plan(intents=("project",)),
        _fact_plan(intents=("service",), answer_mode="clarify"),
    )
    flow = _flow(native, [admission, planning])
    event = Event(group=group)
    original_umo = event.unified_msg_origin
    actual = asyncio.run(flow.run(flow.plugin, event, event.message_str))
    assert actual == planning
    preparation = [step for step in flow.order if step != "data:/v1/bot/orders"]
    assert preparation[:5] == [
        "history:read",
        "llm:intent",
        "session:ensure",
        "session:create",
        "session:bind",
    ]
    assert flow.order[-1] == "llm:planner"
    assert all(step.startswith("data:") for step in preparation[5:-1])
    assert bool("data:/v1/bot/orders" in flow.order) == (not group)
    if not group:
        assert flow.order[0] == "data:/v1/bot/orders"
        assert flow.order.count("data:/v1/bot/orders") == 1
    reference = get_session_reference(event, scope=event.scope)
    request = event.get_extra("provider_request")
    assert request.session_id == reference.owner_umo == request.conversation.user_id
    assert (
        request.conversation.cid == reference.cid
        and request.conversation.platform_id == event.platform
    )
    assert session_request_matches(event, request, scope=event.scope)
    assert event.unified_msg_origin == original_umo
    assert all(
        call.args == (original_umo,)
        for call in flow.context.get_current_chat_provider_id.await_args_list
    )
    assert "dynamicBackground" not in flow.payloads[0]
    assert (
        flow.payloads[1]["dynamicBackground"]["projectCatalog"]["sourceValid"] is True
    )
    assert flow.payloads[1]["initialIntent"] == admission.to_dict()
    assert event.get_extra("_remail_diagnostic_session_ref") == reference.session_ref
    assert flow.db.rows[reference.cid].content is None, (
        "workflow preparation must not append conversation history"
    )


def test_followup_reads_only_same_scope_history_without_resuming_until_accepted(native):
    admission = _fact_plan(intents=("service",))
    unrelated = _fact_plan(route="ignore", intents=())
    flow = _flow(native, [admission, admission, unrelated, admission, admission])

    async def run():
        original = Event(question="我们买的邮箱能用多长时间")
        await flow.run(flow.plugin, original, original.message_str)
        reference = get_session_reference(original, scope=original.scope)
        saved = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": json.dumps(
                            {"untrustedQuestion": "我们买的邮箱能用多长时间"},
                            ensure_ascii=False,
                        ),
                    },
                    {"type": "text", "text": "old background price 987654 points"},
                ],
            },
            {"role": "tool", "content": "private@example.com raw mail body"},
            {"role": "assistant", "content": "购买邮箱的使用期限和质保期是两回事。"},
        ]
        flow.db.rows[reference.cid].content = copy.deepcopy(saved)
        before = flow.db.created
        flow.order.clear()
        unrelated_turn = Event(question="此轮无关问题")
        result = await flow.run(flow.plugin, unrelated_turn, unrelated_turn.message_str)
        assert result.route == "ignore"
        assert flow.order == ["history:read", "llm:intent"]
        assert (
            flow.db.created == before and flow.db.rows[reference.cid].content == saved
        )
        assert unrelated_turn.get_extra(SESSION_REFERENCE_KEY) is None
        assert unrelated_turn.get_extra("provider_request") is None
        followup = Event(question="那过保了呢")
        flow.order.clear()
        await flow.run(flow.plugin, followup, followup.message_str)
        assert "session:create" not in flow.order and flow.db.created == before
        latest = get_session_reference(followup, scope=followup.scope)
        assert latest.cid == reference.cid
        assert flow.db.rows[reference.cid].content == saved
        for payload in flow.payloads[-2:]:
            history = payload["untrustedRecentContext"]
            assert "使用期限和质保期" in history and "不可信弱线索" in history
            assert (
                "987654" not in history
                and "private@example.com" not in history
                and "raw mail" not in history
            )
        assert flow.payloads[-2]["untrustedQuestion"] == "那过保了呢"

    asyncio.run(run())


def test_accepted_workflow_reuses_same_user_without_cross_bot_group_or_private_history(
    native,
):
    accepted = _fact_plan(intents=("service",))
    flow = _flow(native, [accepted] * 14)

    async def run():
        events = [
            Event(),
            Event(),
            Event(bot="90002"),
            Event(group="20002"),
            Event(group=""),
            Event(sender="10002"),
            Event(adapter="telegram", group="-20001#7"),
        ]
        refs = []
        for index, event in enumerate(events):
            await flow.run(flow.plugin, event, event.message_str)
            reference = get_session_reference(event, scope=event.scope)
            refs.append(reference)
            if index == 0:
                flow.db.rows[reference.cid].content = [
                    {"role": "user", "content": "第一位客户自己的购买问题"},
                    {"role": "assistant", "content": "购买服务和接码服务不同。"},
                ]
            if index >= 2:
                assert flow.payloads[-2]["untrustedRecentContext"] == ""
                assert flow.payloads[-1]["untrustedRecentContext"] == ""
        assert refs[0].cid == refs[1].cid
        assert len({ref.cid for ref in refs}) == 6 and flow.db.created == 6
        assert events[0].unified_msg_origin == events[2].unified_msg_origin, (
            "fixture must expose the native multi-bot UMO collision"
        )
        assert refs[0].owner_umo != refs[2].owner_umo

    asyncio.run(run())


def test_session_failure_stops_before_dynamic_context_or_second_llm(native):
    flow = _flow(native, [_fact_plan(intents=("service",))])
    flow.db.fail = True
    event = Event()
    plan = asyncio.run(flow.run(flow.plugin, event, event.message_str))
    assert plan.failed and plan.error == "session_unavailable"
    assert flow.context.llm_generate.await_count == 1
    assert not any(step.startswith("data:") for step in flow.order)
    assert event.get_extra("provider_request") is None


def test_existing_request_is_rebound_before_background_without_old_contexts(native):
    accepted = _fact_plan(intents=("service",))
    flow = _flow(native, [accepted, accepted])
    event = Event(group="")
    previous = NS(cid="unrelated-cid", user_id="different-owner")
    request = NS(
        prompt="already sanitized question",
        conversation=previous,
        contexts=["old raw context"],
    )
    result = asyncio.run(
        flow.run(flow.plugin, event, event.message_str, request=request)
    )
    assert not result.failed
    reference = get_session_reference(event, scope=event.scope)
    assert event.get_extra("provider_request") is request
    assert (
        request.conversation is reference.conversation
        and request.conversation is not previous
    )
    assert request.contexts == [] and request.session_id == reference.owner_umo
    assert flow.order[0] == "data:/v1/bot/orders"
    assert all(
        flow.order.index("session:bind") < index
        for index, stage in enumerate(flow.order)
        if stage.startswith("data:") and stage != "data:/v1/bot/orders"
    )


def test_scope_mutation_during_intent_cannot_create_or_enter_another_session(native):
    flow = _flow(native, [_fact_plan(intents=("service",))])
    event = Event()
    llm = flow.context.llm_generate

    async def changed(**kwargs):
        response = await llm(**kwargs)
        event.sender = "10002"
        return response

    flow.context.llm_generate = changed
    plan = asyncio.run(flow.run(flow.plugin, event, event.message_str))
    assert plan.failed and flow.db.created == 0
    assert event.get_extra("provider_request") is None
    assert not any(stage.startswith("data:") for stage in flow.order)


def test_model_tools_and_delivery_cannot_escape_the_accepted_session(native):
    accepted = _fact_plan(intents=("service",))
    flow = _flow(native, [accepted, accepted])
    event = Event(group="")
    assert not asyncio.run(flow.run(flow.plugin, event, event.message_str)).failed
    model = NS(llm_generate=AsyncMock())
    called = []

    @trace_tool
    async def remail_projects(self, event):
        called.append(True)
        return '{"items":[]}'

    event.sender = "other-user"
    with pytest.raises(ValueError, match="session boundary changed"):
        asyncio.run(
            logged_llm_call(model, event, "writer", prompt="must not leave process")
        )
    model.llm_generate.assert_not_awaited()
    rejected = json.loads(asyncio.run(remail_projects(None, event)))
    assert rejected["ok"] is False and not called
    runtime, _ = _load_welcome_functions()
    send = AsyncMock()
    asyncio.run(
        runtime["_send_scoped_text"](event, send, "must not reach a different user")
    )
    send.assert_not_awaited()
    assert event.stopped


def test_multiple_completed_turns_preserve_native_archive_and_supply_recent_qa(native, native_messages):
    accepted = _fact_plan(intents=("service",))
    flow = _flow(native, [accepted] * 10)

    async def run():
        conversation_id = None
        for index in range(4):
            event = Event(question=f"购买问题{index + 1}")
            assert not (await flow.run(flow.plugin, event, event.message_str)).failed
            reference = get_session_reference(event, scope=event.scope)
            conversation_id = conversation_id or reference.cid
            assert reference.cid == conversation_id
            request = event.get_extra("provider_request")
            run_context = NS(context=NS(event=event), messages=[
                native_messages.Message(role="system", content="current execution prompt"),
                native_messages.Message(role="assistant", content="current internal draft"),
            ])
            result = await prepare_native_history(
                flow.context, event, run_context, scope=event.scope,
                question=event.message_str, answer=f"公开答复{index + 1}",
            )
            assert result.status == "ready" and request.contexts == []
            await flow.context.conversation_manager.update_conversation(
                reference.owner_umo, reference.cid,
                history=native_messages.dump_messages_with_checkpoints(run_context.messages),
            )
        saved = flow.db.rows[conversation_id].content
        assert len(saved) == 8
        assert all(item["role"] in {"user", "assistant"} for item in saved)
        followup = Event(question="接着讲")
        assert not (await flow.run(flow.plugin, followup, followup.message_str)).failed
        assert get_session_reference(followup, scope=followup.scope).cid == conversation_id
        for payload in flow.payloads[-2:]:
            history = payload["untrustedRecentContext"]
            assert all(f"购买问题{index}" in history for index in (2, 3, 4))
            assert "购买问题1" not in history and "internal draft" not in history
        assert flow.db.rows[conversation_id].content == saved, "preparing the next turn must not replace its archive"

    asyncio.run(run())
