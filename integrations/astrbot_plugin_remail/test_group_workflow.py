"""Group entry, event-local scope, and platform delivery checks; no network/model calls."""

import asyncio
import json
import os
import sys
import types
from pathlib import Path
from time import monotonic
from types import SimpleNamespace as NS
from unittest.mock import AsyncMock
from typing import cast

import pytest

from .test_entry import At, AtAll, Chain, Event, MessageType, Plain, Reply, _load
from .test_entry import lifecycle as lifecycle
from .test_security import _load_welcome_functions
from .test_sessions import native as native


def helpers():
    namespace, _ = _load_welcome_functions()
    namespace.update(
        At=At,
        Plain=Plain,
        Reply=Reply,
        MessageChain=Chain,
        MessageType=MessageType,
    )
    return namespace


def tg_event(*, username=None, topic="7", message_id="123", sender="10001"):
    event = Event(
        "😀 @support_bot 怎么充值",
        platform="telegram",
        platform_id="tg-account",
        bot_id="support_bot",
        group_id="-20001" + (f"#{topic}" if topic else ""),
        sender=sender,
        message_id=message_id,
    )
    event.message_obj.raw_message = NS(
        message=NS(
            text=event.message_str,
            entities=[NS(type="mention", offset=3, length=12)],
            from_user=NS(id=int(sender), username=username),
            chat=NS(id=-20001),
            message_id=int(message_id),
            message_thread_id=int(topic) if topic else None,
        )
    )
    event.client = NS(id=99999, send_message=AsyncMock())
    return event


def test_only_current_explicit_mentions_open_group_fae():
    f = helpers()
    plugin = NS(config={"qq_group_owner_id": "30001", "qq_group_admin_ids": ["30002"]})
    for mention, sender, expected in (
        (None, "10001", False),
        ("all", "10001", False),
        ("70001", "10001", False),
        ("90001", "10001", True),
        ("30001", "10001", True),
        ("30002", "10001", True),
        ("30001", "30002", False),
        ("30002", "30001", False),
        ("90001", "30001", True),
        ("90001", "90001", False),
    ):
        event = Event("gmal 可以注册 dola 吗", mention=mention, sender=sender)
        assert f["_capture_group_request"](plugin, event) is expected
    for text in ("怎么充值", "/怎么充值", "bot 怎么充值"):
        event = Event(text)
        event.is_at_or_wake_command = True
        event.message_obj.message.insert(
            0, Reply(id="77", sender_id="90001", qq="90001", chain=[At("90001")])
        )
        event.message_obj.raw_message["message"].insert(
            0, {"type": "reply", "data": {"id": "77"}}
        )
        assert not f["_capture_group_request"](plugin, event)
        assert not f["_service_entry_requested"](plugin, event)
    # The fallback must distinguish Reply.qq from an actual At component as well.
    event.message_obj.raw_message = None
    assert not f["_mentions_bot"](event)
    event.message_obj.message.append(At("90001"))
    assert f["_mentions_bot"](event)
    # Explicit commands retain their command path without starting natural-language FAE.
    command = Event("/项目")
    assert f["_service_entry_requested"](plugin, command)
    assert not f["_capture_group_request"](plugin, command)


def test_untriggered_native_active_reply_is_filtered_without_changing_other_handlers():
    f = helpers()

    class ProviderRequest:
        pass

    f["ProviderRequest"] = ProviderRequest
    context_updates = []
    ordinary_result = NS(message="keep native non-request output")
    native_request = ProviderRequest()
    other_request = ProviderRequest()

    async def native_on_message(event):
        context_updates.append(event)
        yield ordinary_result
        yield native_request

    async def other_on_message(event):
        yield other_request

    native_handler = NS(
        handler_module_path="astrbot.builtin_stars.astrbot.main",
        handler_name="on_message",
        handler=native_on_message,
    )
    other_handler = NS(
        handler_module_path="another_plugin.main",
        handler_name="on_message",
        handler=other_on_message,
    )
    original_handlers = [native_handler, other_handler]
    event = Event("普通群消息，不应触发机器人")
    event.is_at_or_wake_command = True
    event.set_extra("activated_handlers", original_handlers)
    f["_suppress_untriggered_group_reply"](event)
    selected = event.get_extra("activated_handlers")
    assert selected is not original_handlers and selected[0] is not native_handler
    assert selected[1] is other_handler
    assert original_handlers == [native_handler, other_handler]
    assert native_handler.handler is native_on_message
    assert not hasattr(native_handler, "_remail_context_only")

    async def collect():
        return (
            [item async for item in selected[0].handler(event)],
            [item async for item in selected[1].handler(event)],
        )

    native_results, other_results = asyncio.run(collect())
    assert native_results == [ordinary_result]
    assert other_results == [other_request]
    assert context_updates == [event]
    assert not event.is_at_or_wake_command and not event.is_stopped()


def test_group_snapshot_keeps_target_scope_and_personal_context_spans_groups():
    f = helpers()
    plugin = NS(config={}, remail_intent_contexts={})
    original = Event("怎么充值", mention="90001")
    assert f["_capture_group_request"](plugin, original)
    key = f["_intent_context_key"](original)
    plugin.remail_intent_contexts[key] = (monotonic(), "上次询问充值")
    assert f["_recent_intent_context"](plugin, original) == "上次询问充值"
    isolated = [
        Event("gmal 可以注册 dola 吗", sender="10002"),
        Event("gmal 可以注册 dola 吗", platform_id="second-qq"),
        Event("gmal 可以注册 dola 吗", bot_id="90002"),
        tg_event(topic="7"),
    ]
    assert len({key, *(f["_intent_context_key"](event) for event in isolated)}) == 5
    assert all(f["_recent_intent_context"](plugin, event) == "" for event in isolated)
    for same_user in (
        Event("gmal 可以注册 dola 吗", group_id="20002"),
        Event("gmal 可以注册 dola 吗", private=True),
    ):
        assert f["_intent_context_key"](same_user) == key
        assert f["_recent_intent_context"](plugin, same_user) == "上次询问充值"
        assert f["_event_reply_target"](same_user) != f["_event_reply_target"](original)
    first_topic, second_topic = tg_event(topic="7"), tg_event(topic="8")
    topic_key = f["_intent_context_key"](first_topic)
    plugin.remail_intent_contexts[topic_key] = (monotonic(), "Telegram 用户的安全上下文")
    assert f["_intent_context_key"](second_topic) == topic_key
    assert f["_recent_intent_context"](plugin, second_topic) == "Telegram 用户的安全上下文"
    assert f["_event_reply_target"](first_topic) != f["_event_reply_target"](second_topic)
    # Sanitization may clear components; it must not erase the original trigger/target.
    original.message_obj.message.clear()
    assert f["_capture_group_request"](plugin, original)
    original.sender = "10002"
    assert not f["_capture_group_request"](plugin, original)
    assert original.get_extra("_remail_group_llm_allowed") is False
    assert not f["_reply_components"](original, "必须拒发")


def test_telegram_mentions_use_original_utf16_entities_not_quoted_or_forged_ats():
    f = helpers()
    event = tg_event()
    assert f["_mentions_bot"](event)
    # Adapter output can contain synthetic mentions/prefixes for a quoted bot.
    event.message_obj.message = [At("support_bot"), Reply("77", qq="99999")]
    event.message_obj.raw_message.message.entities = []
    assert not f["_mentions_bot"](event)
    event.message_obj.raw_message.message.entities = [
        NS(type="text_mention", offset=3, length=12, user=NS(id=99999))
    ]
    assert f["_mentions_bot"](event)
    event.message_obj.raw_message.message.entities[0].user.id = 88888
    assert not f["_mentions_bot"](event)
    event.message_obj.raw_message.message.entities = [
        NS(type="mention", offset=2, length=12)
    ]
    assert not f["_mentions_bot"](event)


def test_reply_and_final_decorator_pin_current_question_and_only_current_sender():
    f = helpers()
    plugin = NS(config={})
    event = Event("怎么充值", mention="90001", message_id="567")
    assert f["_capture_group_request"](plugin, event)
    event.set_extra("_remail_owned", True)
    assert asyncio.run(f["_install_owned_send_guard"](event))
    asyncio.run(f["_reply"](event, "可以先去充值页看看当前渠道。"))
    assert len(event.sent_chains) == 1
    chain = event.sent_chains[0]
    assert [type(component) for component in chain] == [Reply, At, Plain]
    assert chain[0].id == "567" and chain[1].qq == "10001"
    assert not chain[0].chain and not chain[0].qq
    # A decorator cannot replace the captured current target with another member/quote.
    event.stopped = False
    result = Chain([Reply("77"), At("10002"), Plain("错误目标")])
    result.result_content_type = f["ResultContentType"].LLM_RESULT
    event.set_result(result)
    asyncio.run(f["finalize_safe_remail_result"](plugin, event))
    assert len(event.sent_chains) == 1 and event.get_result() is None
    assert not event.is_stopped()
    mismatch = Event("怎么充值", mention="90001", message_id="567")
    assert f["_capture_group_request"](plugin, mismatch)
    mismatch.set_extra("_remail_owned", True)
    mismatch.set_extra("_remail_canonical_response", "安全答复。")
    mismatch.set_result(Chain([Plain("待发送草稿")]))
    mismatch.message_obj.message_id = "568"
    asyncio.run(f["finalize_safe_remail_result"](plugin, mismatch))
    assert not mismatch.sent and mismatch.get_result() is None
    assert mismatch.is_stopped()
    assert f["_reply_components"](Event("问题"), "") == []
    raw_mismatch = Event("问题", mention="90001")
    assert f["_capture_group_request"](plugin, raw_mismatch)
    raw_mismatch.message_obj.raw_message["self_id"] = "90002"
    assert f["_reply_components"](raw_mismatch, "不能串机器人") == []


def test_unguarded_or_stale_group_markers_cannot_start_planner_or_agent():
    f = helpers()
    for extras in (
        {"_remail_owned": True},
        {"_remail_group_trigger_verified": True},
        {"_remail_admin_handoff_role": "群主"},
    ):
        event = Event("gmal 可以注册 dola 吗")
        event._extras.update(extras)
        plugin = NS(config={}, _authorize_event=AsyncMock())
        request = NS(contexts=["other member history"], prompt="must be removed")
        asyncio.run(f["authorize_llm"](plugin, event, request))
        assert event.is_stopped() and not event.is_at_or_wake_command
        assert request.contexts == [] and request.prompt == ""
        plugin._authorize_event.assert_not_awaited()


def test_telegram_without_username_uses_real_text_mention_and_stops_on_partial_failure(
    monkeypatch,
):
    f = helpers()
    telegram = types.ModuleType("telegram")

    class MessageEntity:
        def __init__(self, kind, offset, length, **kwargs):
            self.type, self.offset, self.length = kind, offset, length
            self.__dict__.update(kwargs)

    telegram.MessageEntity = MessageEntity
    monkeypatch.setitem(sys.modules, "telegram", telegram)
    event = tg_event()
    assert f["_capture_group_request"](NS(config={}), event)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_canonical_response", "这是一份安全答复。")
    assert asyncio.run(f["_install_owned_send_guard"](event))
    asyncio.run(event.send(Chain([Plain("替换我")])))
    sent = event.client.send_message.await_args.kwargs
    assert sent["chat_id"] == "-20001" and sent["message_thread_id"] == 7
    assert sent["reply_to_message_id"] == 123 and "@10001" not in sent["text"]
    entity = sent["entities"][0]
    assert (entity.type, entity.offset, entity.length, entity.user.id) == (
        "text_mention",
        0,
        1,
        10001,
    )
    assert event._has_send_oper
    event = tg_event(message_id="124")
    assert f["_capture_group_request"](NS(config={}), event)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_canonical_response", "安全答复" * 3000)
    event.client.send_message.side_effect = [
        None,
        RuntimeError("transport unavailable"),
    ]
    assert asyncio.run(f["_install_owned_send_guard"](event))
    with pytest.raises(RuntimeError):
        asyncio.run(event.send(Chain([Plain("忽略")])))
    assert event.client.send_message.await_count == 2
    assert event._has_send_oper
    assert len(event.client.send_message.await_args_list[0].kwargs["text"]) <= 4096
    assert not event.client.send_message.await_args_list[1].kwargs["entities"]
    asyncio.run(event.send(Chain([Plain("不得重发已发送分段")])))
    assert event.client.send_message.await_count == 2
    event = tg_event()
    assert f["_capture_group_request"](NS(config={}), event)
    event.message_obj.raw_message.message.from_user.id = 10002
    asyncio.run(f["_send_scoped_text"](event, event.send, "不得发送给另一个人"))
    event.client.send_message.assert_not_awaited()
    assert event.is_stopped()


def test_real_waking_stage_ignores_implicit_triggers_and_preserves_qq_unique_session(
    lifecycle,
):
    case = lifecycle
    case.plugin.bound = case.plugin.available = True
    case.stage.unique_session = True
    quoted = Event("怎么充值")
    quoted.message_obj.message.insert(0, Reply("77", sender_id="90001", qq="90001"))
    events = [
        Event("怎么充值"),
        Event("/怎么充值"),
        Event("怎么充值", mention="all"),
        quoted,
    ]
    events[2].message_obj.message[0] = AtAll()
    for event in events:
        asyncio.run(case.run(event))
        assert not event.sent and not event.is_at_or_wake_command
        assert event.get_extra("_remail_group_llm_allowed") is False
    assert not case.plugin.calls
    case.plugin.context.llm_generate.assert_not_awaited()
    for sender in ("10001", "10002"):
        event = Event("怎么充值", sender=sender, mention="90001")
        asyncio.run(case.run(event, dispatch=False))
        assert event.session_id == f"{sender}_20001"
        assert event.get_extra("_remail_group_llm_allowed") is True
        assert event.get_extra("_remail_reply_target")[0][-1] == sender
    case.stage.unique_session = False
    event = Event("怎么充值", mention="90001")
    asyncio.run(case.run(event, dispatch=False))
    assert event.session_id == "20001"
    event = tg_event(topic="7")
    asyncio.run(case.run(event, dispatch=False))
    assert event.session_id == "-20001#7"
    assert event.get_extra("_remail_group_llm_allowed") is True


def test_current_mentioned_question_and_only_same_sender_context_reach_planner(
    lifecycle,
    native,
):
    case = lifecycle
    native_context, db, _ = native
    case.plugin.context.conversation_manager = native_context.conversation_manager
    case.plugin.bound = case.plugin.available = True
    case.plugin.remail_intent_contexts = {}
    original = Event("怎么充值", sender="10001", mention="90001")
    asyncio.run(case.run(original, dispatch=False))
    case.plugin.remail_intent_contexts[
        case.namespace["_intent_context_key"](original)
    ] = (
        monotonic(),
        "上次安全答复：当前有充值渠道",
    )
    second = Event(
        "gmal 可以接码注册 dola 吗，好像没有这个项目", sender="10002", mention="90001"
    )
    asyncio.run(case.run(second, dispatch=False))
    prefetch = AsyncMock(return_value={"publicApiCapabilities": ""})
    case.namespace["_prepare_fae_context"] = prefetch
    case.plugin.context.get_current_chat_provider_id = AsyncMock(
        return_value="provider"
    )
    case.plugin.context.llm_generate.return_value = NS(
        role="assistant",
        completion_text=json.dumps(
            {
                "route": "remail",
                "intents": ["project"],
                "answer_mode": "normal",
                "privacy": "public",
                "entities": {},
                "facts": [
                    {
                        "id": "projects",
                        "claim": "projects",
                        "required": True,
                        "params": {"projectQuery": "dola"},
                        "dependsOn": [],
                    }
                ],
            }
        ),
    )
    asyncio.run(case.plugin.classify_mentioned_group_question(second))
    prefetch.assert_awaited_once()
    payload = json.loads(case.plugin.context.llm_generate.await_args.kwargs["prompt"])
    assert "dola" in payload["untrustedQuestion"]
    assert "充值" not in json.dumps(payload, ensure_ascii=False)
    assert case.plugin.context.llm_generate.await_count == 2
    assert [
        json.loads(call.kwargs["prompt"])["workflowPhase"]
        for call in case.plugin.context.llm_generate.await_args_list
    ] == ["intent", "planner"]
    assert case.namespace["session_request_matches"](
        second,
        second.get_extra("provider_request"),
        scope=case.namespace["_event_scope"](second),
    )
    assert db.created == 1
    assert second.get_extra("_remail_group_trigger_verified") is True
    irrelevant = Event("讲个笑话", sender="10003", mention="90001")
    asyncio.run(case.run(irrelevant, dispatch=False))
    case.plugin.context.llm_generate.return_value = NS(
        role="assistant",
        completion_text=json.dumps(
            {
                "route": "ignore",
                "answer_mode": "normal",
                "privacy": "public",
                "intents": [],
                "entities": {},
                "facts": [],
            }
        ),
    )
    case.plugin.context.llm_generate.reset_mock()
    asyncio.run(case.plugin.classify_mentioned_group_question(irrelevant))
    assert irrelevant.is_stopped()
    assert (
        case.namespace["_intent_context_key"](irrelevant)
        not in case.plugin.remail_intent_contexts
    )
    assert not irrelevant.get_extra("_remail_group_trigger_verified", False)
    case.plugin.context.llm_generate.assert_awaited_once()
    assert irrelevant.get_extra("provider_request") is None
    assert db.created == 1


def test_real_qq_and_telegram_send_contracts_use_quote_and_current_sender(lifecycle):
    f = lifecycle.namespace
    source = Path(os.environ["ASTRBOT_SOURCE"]) / "astrbot/core/platform/sources"
    f.update(
        cast=cast,
        Image=type("Image", (), {}),
        Record=type("Record", (), {}),
        File=type("File", (), {}),
        Video=type("Video", (), {}),
        Node=type("Node", (), {}),
        Nodes=type("Nodes", (), {}),
        ChatAction=NS(
            TYPING="typing",
            UPLOAD_PHOTO="photo",
            UPLOAD_VOICE="voice",
            UPLOAD_VIDEO="video",
            UPLOAD_DOCUMENT="document",
        ),
        telegramify_markdown=NS(markdownify=lambda text: text),
        BadRequest=type("BadRequest", (Exception,), {}),
    )
    _load(source / "aiocqhttp/aiocqhttp_message_event.py", {"AiocqhttpMessageEvent"}, f)
    event = Event("怎么充值", mention="90001", message_id="567")
    assert f["_capture_group_request"](lifecycle.plugin, event)
    chain = Chain(f["_reply_components"](event, "你好，可以去充值页。"))
    segments = asyncio.run(f["AiocqhttpMessageEvent"]._parse_onebot_json(chain))
    assert segments[:2] == [
        {"type": "reply", "data": {"id": "567"}},
        {"type": "at", "data": {"qq": "10001"}},
    ]
    _load(source / "telegram/tg_event.py", {"TelegramPlatformEvent"}, f)
    event = tg_event(username="customer_one")
    assert f["_capture_group_request"](lifecycle.plugin, event)
    client = NS(send_message=AsyncMock(), send_chat_action=AsyncMock())
    chain = Chain(f["_reply_components"](event, "可以去充值页看看。"))
    asyncio.run(f["TelegramPlatformEvent"].send_with_client(client, chain, "-20001#7"))
    sent = client.send_message.await_args.kwargs
    assert sent["text"].startswith("@customer_one ")
    assert sent["reply_to_message_id"] == "123" and sent["message_thread_id"] == "7"
    assert sent["chat_id"] == "-20001"
