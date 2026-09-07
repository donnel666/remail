"""AST-load native ConversationManager with an offline DB; never import a server."""

import ast
import asyncio
import json
import os
import sys
from collections import defaultdict
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from pathlib import Path
from types import ModuleType, SimpleNamespace as NS
from typing import Any, Literal
from unittest.mock import AsyncMock
from uuid import uuid4

import pytest

from .security import normalize_security_text
from .sessions import (
    MAX_HISTORY_CHARS,
    MAX_HISTORY_INPUT_CHARS,
    SESSION_REFERENCE_KEY,
    NativeSessionResetTarget,
    _LOCKS,
    _owner_umo,
    _safe_history,
    bind_native_request,
    ensure_native_session,
    get_session_reference,
    prepare_native_history,
    read_existing_history,
    reset_native_sessions,
    session_request_matches,
)


class Event:
    def __init__(
        self,
        *,
        platform="qq-one",
        adapter="aiocqhttp",
        bot="90001",
        group="20001",
        sender="10001",
    ):
        self.platform, self.adapter, self.bot, self.group, self.sender = (
            platform,
            adapter,
            bot,
            group,
            sender,
        )
        self.extras = {}

    def get_platform_id(self):
        return self.platform

    def get_platform_name(self):
        return self.adapter

    def get_self_id(self):
        return self.bot

    def get_group_id(self):
        return self.group

    def get_sender_id(self):
        return self.sender

    def get_message_type(self):
        return NS(value="GroupMessage" if self.group else "FriendMessage")

    @property
    def unified_msg_origin(self):
        session = f"{self.sender}_{self.group}" if self.group else self.sender
        return f"{self.platform}:{self.get_message_type().value}:{session}"

    @property
    def scope(self):
        return self.platform, self.adapter, self.bot, self.group, self.sender

    def get_extra(self, key, default=None):
        return self.extras.get(key, default)

    def set_extra(self, key, value):
        self.extras[key] = value


def _load_native(path, names, namespace):
    nodes = [
        node
        for node in ast.parse(path.read_text(encoding="utf-8")).body
        if isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name in names
    ]
    assert {node.name for node in nodes} == names
    module = ast.Module(
        body=[
            ast.ImportFrom(
                module="__future__", names=[ast.alias(name="annotations")], level=0
            ),
            *nodes,
        ],
        type_ignores=[],
    )
    exec(compile(ast.fix_missing_locations(module), str(path), "exec"), namespace)


@pytest.fixture
def native():
    source = os.environ.get("ASTRBOT_SOURCE")
    if not source:
        pytest.skip(
            "Set ASTRBOT_SOURCE to an actual AstrBot checkout for native checks"
        )
    source = Path(source) / "astrbot" / "core"
    prefs = {}

    async def session_get(umo, key, default=None):
        await asyncio.sleep(0)
        return prefs.get((umo, key), default)

    async def session_put(umo, key, value):
        await asyncio.sleep(0)
        prefs[umo, key] = value

    class DB:
        def __init__(self):
            self.rows, self.created, self.fail = {}, 0, False

        async def create_conversation(self, **values):
            await asyncio.sleep(0)
            if self.fail:
                raise RuntimeError("synthetic private database error")
            self.created += 1
            row = NS(
                conversation_id=str(uuid4()),
                created_at=datetime.now(timezone.utc),
                updated_at=datetime.now(timezone.utc),
                token_usage=0,
                **values,
            )
            self.rows[row.conversation_id] = row
            return row

        async def get_conversation_by_id(self, *, cid):
            await asyncio.sleep(0)
            if self.fail:
                raise RuntimeError("synthetic private database error")
            return self.rows.get(cid)

        async def get_conversations(self, *, user_id=None, platform_id=None):
            await asyncio.sleep(0)
            if self.fail:
                raise RuntimeError("synthetic private database error")
            return [
                row for row in self.rows.values()
                if (user_id is None or row.user_id == user_id)
                and (platform_id is None or row.platform_id == platform_id)
            ]

        async def update_conversation(self, *, cid, content=None, **values):
            await asyncio.sleep(0)
            if self.fail:
                raise RuntimeError("synthetic private database error")
            if content is not None:
                self.rows[cid].content = content
            for name, value in values.items():
                if value is not None:
                    setattr(self.rows[cid], name, value)
            self.rows[cid].updated_at = datetime.now(timezone.utc)

    namespace = {
        "__name__": __name__,
        "dataclass": dataclass,
        "datetime": datetime,
        "timezone": timezone,
        "json": json,
        "sp": NS(session_get=session_get, session_put=session_put),
        "deprecated": lambda **kwargs: lambda function: function,
    }
    _load_native(source / "db" / "po.py", {"Conversation"}, namespace)
    _load_native(
        source / "utils" / "datetime_utils.py",
        {"normalize_datetime_utc", "to_utc_timestamp"},
        namespace,
    )
    _load_native(source / "conversation_mgr.py", {"ConversationManager"}, namespace)
    db = DB()
    context = NS(conversation_manager=namespace["ConversationManager"](db))
    return context, db, prefs


@pytest.fixture
def native_activity(monkeypatch):
    source = os.environ.get("ASTRBOT_SOURCE")
    if not source:
        pytest.skip("Set ASTRBOT_SOURCE for native active-event registry checks")
    module = ModuleType("astrbot.core.utils.active_event_registry")
    module.__dict__["defaultdict"] = defaultdict
    monkeypatch.setitem(sys.modules, module.__name__, module)
    _load_native(
        Path(source) / "astrbot/core/utils/active_event_registry.py",
        {"ActiveEventRegistry"}, module.__dict__,
    )
    module.active_event_registry = module.ActiveEventRegistry()
    return module.active_event_registry


@pytest.fixture
def native_messages(monkeypatch):
    """Load the native text-message/checkpoint schema without starting AstrBot."""
    source = os.environ.get("ASTRBOT_SOURCE")
    if not source:
        pytest.skip("Set ASTRBOT_SOURCE for native history serialization checks")
    from pydantic import (
        BaseModel, PrivateAttr, ValidationError, model_serializer, model_validator,
    )

    module = ModuleType("astrbot.core.agent.message")
    module.__dict__.update(
        Any=Any, Literal=Literal, BaseModel=BaseModel, PrivateAttr=PrivateAttr,
        ValidationError=ValidationError, model_serializer=model_serializer,
        model_validator=model_validator, CHECKPOINT_ROLE="_checkpoint",
        ContentPart=Any,  # Archived Q/A uses strings, never multimodal parts.
    )
    monkeypatch.setitem(sys.modules, module.__name__, module)
    _load_native(
        Path(source) / "astrbot/core/agent/message.py",
        {"ToolCall", "CheckpointData", "Message", "CheckpointMessageSegment",
         "is_checkpoint_message", "_get_checkpoint_data", "bind_checkpoint_messages",
         "dump_messages_with_checkpoints"},
        module.__dict__,
    )
    return module


def test_read_only_missing_then_concurrent_native_creation_and_reuse(native):
    context, db, _ = native

    async def run():
        events = [Event() for _ in range(20)]
        missing = await read_existing_history(context, events[0], scope=events[0].scope)
        assert missing.status == "missing"
        assert db.created == 0 and events[0].get_extra(SESSION_REFERENCE_KEY) is None
        original = events[0].unified_msg_origin
        results = await asyncio.gather(
            *(
                ensure_native_session(context, event, scope=event.scope)
                for event in events
            )
        )
        assert all(result.status == "ready" for result in results)
        assert sum(result.created for result in results) == 1
        assert db.created == 1
        refs = [get_session_reference(event, scope=event.scope) for event in events]
        assert len({ref.cid for ref in refs}) == 1
        assert refs[0].owner_umo != original
        assert events[0].unified_msg_origin == original, (
            "preserve native provider/profile key"
        )
        assert len(results[0].session_ref) == 12
        assert "20001" not in repr(refs[0]) and refs[0].cid not in repr(refs[0])
        # Reload only the manager: native shared preferences keep the selected CID.
        context.conversation_manager = type(context.conversation_manager)(db)
        again = Event()
        result = await ensure_native_session(context, again, scope=again.scope)
        assert result.status == "ready" and not result.created and db.created == 1
        assert get_session_reference(again, scope=again.scope).cid == refs[0].cid

    asyncio.run(run())


@pytest.mark.parametrize(
    "different",
    [
        {"sender": "10002"},
        {"group": "20002"},
        {"group": ""},
        {"platform": "qq-two"},
        {"bot": "90002"},
        {"adapter": "telegram", "group": "-20001#7"},
        {"adapter": "telegram", "group": "-20001#8"},
    ],
)
def test_native_owner_isolates_users_bots_groups_topics_and_private(native, different):
    context, db, _ = native

    async def run():
        first, second = Event(), Event(**different)
        await ensure_native_session(context, first, scope=first.scope)
        await ensure_native_session(context, second, scope=second.scope)
        a, b = (
            get_session_reference(event, scope=event.scope) for event in (first, second)
        )
        same_qq_bot = (
            different.get("platform", "qq-one") == "qq-one"
            and different.get("adapter", "aiocqhttp") == "aiocqhttp"
            and different.get("bot", "90001") == "90001"
            and different.get("sender", "10001") == "10001"
        )
        if same_qq_bot:
            assert a.cid == b.cid and a.owner_umo == b.owner_umo
            assert a.session_ref == b.session_ref and db.created == 1
        else:
            assert (
                a.cid != b.cid
                and a.owner_umo != b.owner_umo
                and a.session_ref != b.session_ref
            )
            assert db.created == 2
        second.set_extra(SESSION_REFERENCE_KEY, a)
        refused = await ensure_native_session(context, second, scope=second.scope)
        assert refused.status == "scope_mismatch" and not refused.history
        assert db.created == (1 if same_qq_bot else 2)

    asyncio.run(run())


def test_owner_mismatch_fails_closed_without_repair_or_history(native):
    context, db, _ = native

    async def run():
        foreign, target = Event(sender="10002"), Event()
        await ensure_native_session(context, foreign, scope=foreign.scope)
        reference = get_session_reference(foreign, scope=foreign.scope)
        db.rows[reference.cid].content = [
            {"role": "user", "content": "foreign private question"}
        ]
        owner, _ = _owner_umo(target.scope, target.unified_msg_origin)
        context.conversation_manager.session_conversations[owner] = reference.cid
        # Real native getter accepts mismatched UMO: our additional owner guard is required.
        assert await context.conversation_manager.get_conversation(owner, reference.cid)
        for operation in (read_existing_history, ensure_native_session):
            result = await operation(context, target, scope=target.scope)
            assert result.status == "owner_mismatch" and not result.history
        assert db.created == 1 and target.get_extra(SESSION_REFERENCE_KEY) is None

    asyncio.run(run())


def test_stale_cid_read_never_creates_but_accepted_ensure_repairs(native):
    context, db, _ = native

    async def run():
        event = Event()
        owner, _ = _owner_umo(event.scope, event.unified_msg_origin)
        context.conversation_manager.session_conversations[owner] = str(uuid4())
        result = await read_existing_history(context, event, scope=event.scope)
        assert result.status == "missing" and db.created == 0
        result = await ensure_native_session(context, event, scope=event.scope)
        assert result.status == "ready" and result.created and db.created == 1

    asyncio.run(run())


def test_db_failure_missing_api_and_invalid_cid_have_explicit_status(native):
    context, db, _ = native

    async def run():
        event = Event()
        owner, _ = _owner_umo(event.scope, event.unified_msg_origin)
        db.fail = True
        failed = await ensure_native_session(context, event, scope=event.scope)
        assert failed.status == "storage_error" and not failed.history
        assert "database" not in repr(failed)
        missing = await ensure_native_session(NS(), event, scope=event.scope)
        assert missing.status == "unavailable"
        context.conversation_manager.session_conversations[owner] = {"invalid": "cid"}
        invalid = await ensure_native_session(context, event, scope=event.scope)
        assert invalid.status == "invalid_conversation"
        context.conversation_manager.get_curr_conversation_id = AsyncMock(
            side_effect=asyncio.CancelledError
        )
        with pytest.raises(asyncio.CancelledError):
            await ensure_native_session(context, event, scope=event.scope)

    asyncio.run(run())


def test_request_and_scope_are_pinned_and_rechecked(native):
    context, _, _ = native

    async def run():
        event = Event()
        await ensure_native_session(context, event, scope=event.scope)
        reference = get_session_reference(event, scope=event.scope)
        request = NS(prompt="怎么充值", contexts=["old raw tools"], conversation=None)
        assert bind_native_request(event, request, scope=event.scope)
        assert request.contexts == [] and request.conversation is reference.conversation
        assert request.session_id == reference.owner_umo
        assert session_request_matches(event, request, scope=event.scope)
        request.session_id = event.unified_msg_origin
        assert not session_request_matches(event, request, scope=event.scope)
        request.session_id = reference.owner_umo
        other_request = NS(
            prompt="changed", contexts=[], conversation=reference.conversation
        )
        assert not bind_native_request(event, other_request, scope=event.scope)
        event.set_extra("provider_request", other_request)
        assert not session_request_matches(event, request, scope=event.scope)
        event.set_extra("provider_request", request)
        request.conversation = NS(
            cid=reference.cid, user_id=reference.owner_umo, platform_id=event.platform
        )
        assert not session_request_matches(event, request, scope=event.scope)
        request.conversation = reference.conversation
        reference.conversation.user_id = "foreign-owner"
        assert get_session_reference(event, scope=event.scope) is None

    asyncio.run(run())


def test_concurrent_callbacks_for_same_event_do_not_replace_bound_request(native):
    context, db, _ = native

    async def run():
        event = Event()
        first = asyncio.create_task(
            ensure_native_session(context, event, scope=event.scope)
        )
        second = asyncio.create_task(
            ensure_native_session(context, event, scope=event.scope)
        )
        assert (await first).status == "ready"
        reference = get_session_reference(event, scope=event.scope)
        request = NS(prompt="怎么充值", contexts=[], conversation=None)
        assert bind_native_request(event, request, scope=event.scope)
        assert (await second).status == "ready"
        assert (
            get_session_reference(event, scope=event.scope).conversation
            is reference.conversation
        )
        assert session_request_matches(event, request, scope=event.scope)
        assert db.created == 1

    asyncio.run(run())


def test_event_scope_changed_during_db_read_is_rejected(native):
    context, db, _ = native

    async def run():
        first = Event()
        await ensure_native_session(context, first, scope=first.scope)
        event = Event()
        original = db.get_conversation_by_id

        async def changed(*, cid):
            row = await original(cid=cid)
            event.sender = "10002"
            return row

        db.get_conversation_by_id = changed
        result = await ensure_native_session(context, event, scope=event.scope)
        assert result.status == "scope_mismatch"
        assert event.get_extra(SESSION_REFERENCE_KEY) is None

    asyncio.run(run())


def test_history_retains_only_bounded_safe_final_qa_not_tool_or_background():
    raw = [
        {"role": "system", "content": "system-prompt-private"},
        {
            "role": "user",
            "content": [
                {
                    "type": "text",
                    "text": json.dumps(
                        {"untrustedQuestion": "gmal可以接码注册dola吗"},
                        ensure_ascii=False,
                    ),
                },
                {"type": "text", "text": "old full background prices 600000000 points"},
                {"type": "text", "text": "another group private quoted question"},
            ],
        },
        {
            "role": "assistant",
            "content": "intermediate draft",
            "tool_calls": [{"function": {"name": "remail_projects"}}],
        },
        {"role": "tool", "content": "raw tool private mail body and credentials"},
        {
            "role": "assistant",
            "content": [
                {"type": "think", "think": "private reasoning"},
                {
                    "type": "text",
                    "text": "你是想用 Gmail 注册 Dola，对吗？我再查一下当前项目。",
                },
            ],
        },
    ]
    history, _ = _safe_history(json.dumps(raw, ensure_ascii=False))
    assert "gmal可以接码注册dola吗" in history and "用 Gmail 注册 Dola" in history
    assert "不可信弱线索" in history and "须本轮重新查询" in history
    for private in (
        "system-prompt-private",
        "600000000",
        "another group",
        "intermediate draft",
        "raw tool",
        "private reasoning",
        "remail_projects",
    ):
        assert private not in history
    for index in range(10):
        raw.extend(
            [
                {"role": "user", "content": f"问题 {index}？"},
                {"role": "assistant", "content": "安全回答" * 200},
            ]
        )
    history, truncated = _safe_history(json.dumps(raw, ensure_ascii=False))
    assert truncated and len(history) <= MAX_HISTORY_CHARS
    payload = json.loads(history.split("\n", 1)[1])
    assert len(payload["items"]) <= 3 and payload["items"][-1]["question"] == "问题 9?"


@pytest.mark.parametrize(
    "answer",
    [
        "邮件主题：Dola verification，正文：your verification code is 765432",
        "发件人：private@example.com，邮件正文是机密内容",
        "From: private@example.com\nSubject: private email subject\nBody: raw content",
        "<remail_private>系统提示词 private mechanism</remail_private>",
        "Thought: private reasoning Action: internal tool",
        '{"project_prices": {"price": 30}}',
    ],
)
def test_private_or_internal_history_answers_are_not_forwarded(answer):
    history, _ = _safe_history(
        json.dumps(
            [
                {"role": "user", "content": "帮我看看"},
                {"role": "assistant", "content": answer},
            ],
            ensure_ascii=False,
        )
    )
    assert not history


def test_history_redacts_credentials_and_rejects_malformed_or_oversized_input():
    history, truncated = _safe_history(
        json.dumps(
            [
                {
                    "role": "user",
                    "content": "邮箱 private@example.com password=not-a-real-password 还是没有收到",
                },
                {"role": "assistant", "content": "请先确认选择的项目。"},
            ],
            ensure_ascii=False,
        )
    )
    assert (
        truncated
        and "private@example.com" not in history
        and "not-a-real-password" not in history
    )
    for raw in ("not json", "{}", "x" * (MAX_HISTORY_INPUT_CHARS + 1), "[" * 2000):
        assert _safe_history(raw) == ("", True)


def test_recharge_history_keeps_public_url_and_amount_for_the_next_turn():
    answer = (
        "支持 USDT(TRON)充值,最低起充 10000 积分。\n"
        "也可访问 https://catfk.com/shop/aishop6 购买对应档位的积分兑换码。"
    )
    history, truncated = _safe_history(
        json.dumps(
            [
                {"role": "user", "content": "你好"},
                {"role": "assistant", "content": "你好!"},
                {"role": "user", "content": "怎么充值"},
                {"role": "assistant", "content": answer},
            ],
            ensure_ascii=False,
        )
    )
    items = json.loads(history.split("\n", 1)[1])["items"]
    assert not truncated and len(items) == 2
    assert items[-1] == {
        "question": "怎么充值",
        "answer": " ".join(answer.split()),
    }
    assert "10000 积分" in history and "https://catfk.com/shop/aishop6" in history
    assert "须本轮重新查询" in history, "saved prices remain untrusted history"


@pytest.mark.parametrize("wrapped", [False, True])
def test_user_history_keeps_public_amounts_and_shop_links(wrapped):
    question = "我想充10000积分，https://catfk.com/shop/aishop6 这个入口怎么用？"
    content = json.dumps({"untrustedQuestion": question}, ensure_ascii=False) if wrapped else question
    history, truncated = _safe_history(json.dumps([
        {"role": "user", "content": content},
        {"role": "assistant", "content": "在钱包输入购买到的兑换码。"},
    ], ensure_ascii=False))
    assert not truncated
    items = json.loads(history.split("\n", 1)[1])["items"]
    assert items[0]["question"] == normalize_security_text(question)
    assert "10000积分" in history and "https://catfk.com/shop/aishop6" in history


def test_user_history_still_protects_explicit_secrets_and_mail():
    question = "充值10000积分；QQ 12345678；验证码：654321；password=synthetic-secret。"
    history, truncated = _safe_history(json.dumps([
        {"role": "user", "content": question},
        {"role": "assistant", "content": "请在本人钱包操作。"},
    ], ensure_ascii=False))
    assert truncated and "10000积分" in history
    assert all(value not in history for value in ("12345678", "654321", "synthetic-secret"))
    history, truncated = _safe_history(json.dumps([
        {"role": "user", "content": "邮件主题：private subject；正文：private message"},
        {"role": "assistant", "content": "请在本人页面查看。"},
    ], ensure_ascii=False))
    assert not history and truncated


def test_assistant_history_still_hides_explicit_credentials_and_personal_values():
    answer = (
        "邮箱 private@example.com; account=member42; api_key=synthetic-secret; "
        "QQ 12345678; 手机 13912345678; 订单号 old-order-42。请查看本人页面。"
    )
    history, truncated = _safe_history(
        json.dumps(
            [
                {"role": "user", "content": "帮我看看"},
                {"role": "assistant", "content": answer},
            ],
            ensure_ascii=False,
        )
    )
    assert truncated and "请查看本人页面" in history
    for private in (
        "private@example.com",
        "member42",
        "synthetic-secret",
        "12345678",
        "13912345678",
        "old-order-42",
    ):
        assert private not in history


async def _bound_history_event(context, question="当前问题", **scope):
    event = Event(**scope)
    assert (await ensure_native_session(context, event, scope=event.scope)).status == "ready"
    reference = get_session_reference(event, scope=event.scope)
    request = NS(prompt=question, contexts=[], conversation=None)
    assert bind_native_request(event, request, scope=event.scope)
    run_context = NS(context=NS(event=event), messages=[])
    return event, reference, request, run_context


def test_saved_qa_survives_stale_snapshots_and_model_history_stays_bounded(native, native_messages):
    context, db, _ = native

    async def run():
        first = await _bound_history_event(context)
        second = await _bound_history_event(context)
        assert first[1].cid == second[1].cid and second[1].conversation.history == "[]"
        cases = [first, second]
        for index in range(4):
            if index == len(cases):
                cases.append(await _bound_history_event(context))
            event, reference, request, run_context = cases[index]
            run_context.messages = [native_messages.Message(role="assistant", content="current draft")]
            result = await prepare_native_history(
                context, event, run_context, scope=event.scope,
                question=f"问题{index + 1}", answer=f"答复{index + 1}",
            )
            assert result.status == "ready"
            assert all(isinstance(message, native_messages.Message) for message in run_context.messages)
            assert request.contexts == [], "archival history must not become model input"
            stored = native_messages.dump_messages_with_checkpoints(run_context.messages)
            assert len(stored) == (index + 1) * 2
            await context.conversation_manager.update_conversation(
                reference.owner_umo, reference.cid, history=stored,
            )
            prepared = run_context.messages
            assert (await prepare_native_history(
                context, event, run_context, scope=event.scope,
                question=f"问题{index + 1}", answer=f"答复{index + 1}",
            )).status == "ready"
            assert run_context.messages is prepared, "repeated done hooks must not duplicate a turn"
        assert len(db.rows[first[1].cid].content) == 8
        context.conversation_manager = type(context.conversation_manager)(db)
        following = Event()
        result = await read_existing_history(context, following, scope=following.scope)
        payload = json.loads(result.history.split("\n", 1)[1])
        assert [item["question"] for item in payload["items"]] == [
            "来源:群聊 问题2",
            "来源:群聊 问题3",
            "来源:群聊 问题4",
        ]
        assert len(result.history) <= MAX_HISTORY_CHARS and result.history_truncated

    asyncio.run(run())


def test_archival_history_removes_old_tools_systems_and_native_control_messages(native, native_messages):
    context, db, _ = native

    async def run():
        event, reference, _, run_context = await _bound_history_event(context)
        question, answer = "本人完整原问题\n" * 500, "完整公开答复\n" * 500
        db.rows[reference.cid].content = [
            {"role": "system", "content": "private system instructions"},
            {"role": "user", "content": [
                {"type": "text", "text": json.dumps({"untrustedQuestion": question}, ensure_ascii=False)},
                {"type": "text", "text": "old background and planner instructions"},
            ]},
            {"role": "assistant", "content": "private intermediate draft", "tool_calls": [{"id": "old-tool"}]},
            {"role": "tool", "tool_call_id": "old-tool", "content": "private tool data"},
            {"role": "user", "content": "internal native tool limit instruction"},
            {"role": "assistant", "content": [
                {"type": "think", "think": "private reasoning"},
                {"type": "text", "text": answer},
            ]},
            {"role": "_checkpoint", "content": {"id": "old-checkpoint"}},
        ]
        assert (await prepare_native_history(
            context, event, run_context, scope=event.scope, question="本轮问题", answer="本轮答复",
        )).status == "ready"
        stored = native_messages.dump_messages_with_checkpoints(run_context.messages)
        assert json.loads(stored[0]["content"])["untrustedQuestion"] == question
        assert stored[1]["content"] == answer, "storage must not apply prompt-length truncation"
        assert stored[2] == {"role": "_checkpoint", "content": {"id": "old-checkpoint"}}
        assert [message["role"] for message in stored] == ["user", "assistant", "_checkpoint", "user", "assistant"]
        assert all(marker not in json.dumps(stored) for marker in ("private", "internal native", "old background"))

    asyncio.run(run())


@pytest.mark.parametrize("failure, expected", [
    ("owner", "owner_mismatch"), ("storage", "storage_error"),
    ("history", "invalid_history"), ("scope", "scope_mismatch"),
    ("runner", "scope_mismatch"), ("request", "scope_mismatch"),
])
def test_history_preparation_fails_without_mutating_runtime_or_persisted_qa(native, native_messages, failure, expected):
    context, db, _ = native

    async def run():
        event, reference, _, run_context = await _bound_history_event(context)
        if failure == "owner":
            db.rows[reference.cid].user_id = "different-user"
        elif failure == "storage":
            db.fail = True
        elif failure == "history":
            db.rows[reference.cid].content = {"invalid": "history object"}
        elif failure == "scope":
            event.sender = "10002"
        elif failure == "runner":
            run_context.context.event = Event()
        elif failure == "request":
            event.set_extra("provider_request", NS())
        runtime = run_context.messages
        persisted = db.rows[reference.cid].content
        result = await prepare_native_history(
            context, event, run_context, scope=event.scope, question="本轮问题", answer="本轮答复",
        )
        assert result.status == expected
        assert run_context.messages is runtime and db.rows[reference.cid].content is persisted
        assert event.get_extra("_remail_history_prepared") is None

    asyncio.run(run())


def test_history_preparation_rechecks_scope_after_native_read_and_propagates_cancel(native, native_messages):
    context, _, _ = native

    async def run():
        event, _, _, run_context = await _bound_history_event(context)
        original = context.conversation_manager.get_conversation

        async def changed(*args, **kwargs):
            result = await original(*args, **kwargs)
            event.sender = "10002"
            return result

        context.conversation_manager.get_conversation = changed
        result = await prepare_native_history(
            context, event, run_context, scope=event.scope, question="本轮问题", answer="本轮答复",
        )
        assert result.status == "scope_mismatch" and run_context.messages == []
        event.sender = "10001"
        context.conversation_manager.get_conversation = AsyncMock(side_effect=asyncio.CancelledError)
        with pytest.raises(asyncio.CancelledError):
            await prepare_native_history(
                context, event, run_context, scope=event.scope, question="本轮问题", answer="本轮答复",
            )

    asyncio.run(run())


def _reset_target_for(event, reference):
    session_id, _ = _owner_umo(event.scope, event.unified_msg_origin)
    return NativeSessionResetTarget(
        session_id, event.scope, event.unified_msg_origin,
        reference.cid, reference.session_ref,
    )


def test_reset_one_history_preserves_identity_and_invalidates_old_turns(native, native_activity):
    context, db, prefs = native

    async def run():
        event, reference, request, runner = await _bound_history_event(context)
        other, other_ref, _, _ = await _bound_history_event(context, sender="10002")
        saved = [
            {"role": "user", "content": "原问题"},
            {"role": "assistant", "content": "原答复"},
        ]
        row = db.rows[reference.cid]
        row.content, row.title, row.persona_id, row.token_usage = saved, "保留标题", "persona", 42
        db.rows[other_ref.cid].content = saved
        intent_event = Event()
        assert (await read_existing_history(context, intent_event, scope=intent_event.scope)).history
        created = db.created
        result = await reset_native_sessions(context, (_reset_target_for(event, reference),))
        assert result.status == "ready" and result.reset_conversations == 1
        assert result.reset_scopes == (event.scope,)
        assert row.content == [] and row.token_usage == 0
        assert row.title == "保留标题" and row.persona_id == "persona"
        assert db.created == created and db.rows[other_ref.cid].content is saved
        assert prefs[reference.owner_umo, "sel_conv_id"] == reference.cid
        assert context.conversation_manager.session_conversations[reference.owner_umo] == reference.cid
        assert get_session_reference(event, scope=event.scope) is None
        assert not session_request_matches(event, request, scope=event.scope)
        assert get_session_reference(other, scope=other.scope) is not None
        stale = await prepare_native_history(
            context, event, runner, scope=event.scope,
            question="重置前问题", answer="迟到的结果",
        )
        assert stale.status == "scope_mismatch" and row.content == []
        assert (await ensure_native_session(context, intent_event, scope=intent_event.scope)).status == "scope_mismatch"
        fresh = Event()
        assert (await read_existing_history(context, fresh, scope=fresh.scope)).history == ""
        assert (await ensure_native_session(context, fresh, scope=fresh.scope)).status == "ready"
        assert get_session_reference(fresh, scope=fresh.scope).cid == reference.cid

    asyncio.run(run())


def test_reset_all_enumerates_unrecorded_and_old_remail_conversations_only(native, native_activity):
    context, db, _ = native

    async def run():
        _, reference, _, _ = await _bound_history_event(context)
        manager = context.conversation_manager
        legacy_owner = "qq-one:FriendMessage:remail-" + "a" * 64
        legacy = [
            await manager.new_conversation(legacy_owner, platform_id="qq-one")
            for _ in range(2)
        ]
        normal = await manager.new_conversation("qq-one:FriendMessage:12345", platform_id="qq-one")
        lookalike = await manager.new_conversation(
            "qq-one:FriendMessage:remail-" + "a" * 63, platform_id="qq-one"
        )
        saved = [{"role": "user", "content": "保留非插件历史"}]
        for row in db.rows.values():
            row.content = saved
        result = await reset_native_sessions(context, all_sessions=True)
        assert result.status == "ready" and result.reset_conversations == 3
        assert result.reset_scopes == (), "all reset cannot depend on retained trace scopes"
        assert all(db.rows[cid].content == [] for cid in [reference.cid, *legacy])
        assert db.rows[normal].content is saved and db.rows[lookalike].content is saved
        assert manager.session_conversations[legacy_owner] == legacy[-1]

    asyncio.run(run())


@pytest.mark.parametrize("field", ["scope", "session_id", "session_ref"])
def test_reset_refuses_untrusted_target_metadata(native, native_activity, field):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        target = _reset_target_for(event, reference)
        bad = (*target.scope[:-1], "other-user") if field == "scope" else "untrusted"
        original = db.rows[reference.cid].content
        result = await reset_native_sessions(context, (replace(target, **{field: bad}),))
        assert result.status == "invalid_targets" and result.reset_conversations == 0
        assert db.rows[reference.cid].content is original

    asyncio.run(run())


def test_reset_validates_every_cid_owner_before_writing_any_history(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        other, other_ref, _, _ = await _bound_history_event(context, sender="10002")
        foreign = await context.conversation_manager.new_conversation(
            "qq-one:FriendMessage:ordinary", platform_id="qq-one"
        )
        original = [{"role": "assistant", "content": "历史"}]
        for row in db.rows.values():
            row.content = original
        targets = (
            _reset_target_for(event, reference),
            replace(_reset_target_for(other, other_ref), conversation_id=foreign),
        )
        result = await reset_native_sessions(context, targets)
        assert result.status == "owner_mismatch" and result.reset_conversations == 0
        assert all(row.content is original for row in db.rows.values())

    asyncio.run(run())


@pytest.mark.parametrize("busy_at", ["intent", "history_prepared", "lock", "all_unrecorded"])
def test_busy_native_pipeline_or_resolution_refuses_the_whole_reset(native, native_activity, busy_at):
    context, db, _ = native

    async def run():
        event, reference, _, runner = await _bound_history_event(context)
        other, other_ref, _, _ = await _bound_history_event(context, sender="10002")
        original = [{"role": "assistant", "content": "不可丢失"}]
        for row in db.rows.values():
            row.content = original
        targets = (_reset_target_for(event, reference), _reset_target_for(other, other_ref))
        lock = None
        if busy_at == "lock":
            lock = asyncio.Lock()
            _LOCKS[id(context.conversation_manager), reference.owner_umo] = lock
            await lock.acquire()
        else:
            active = Event() if busy_at == "intent" else event
            if busy_at == "history_prepared":
                active.set_extra("_remail_history_prepared", (runner, reference.cid))
            native_activity.register(active)
        try:
            result = await reset_native_sessions(
                context, () if busy_at == "all_unrecorded" else targets,
                all_sessions=busy_at == "all_unrecorded",
            )
            assert result.status == "busy" and result.busy_session_ids
            assert result.reset_conversations == 0
            assert all(row.content is original for row in db.rows.values())
            assert get_session_reference(event, scope=event.scope) is not None
        finally:
            if lock is not None:
                lock.release()
            else:
                native_activity.unregister(active)

    asyncio.run(run())


def test_history_reader_queued_during_reset_cannot_reintroduce_old_context(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        entered, release = asyncio.Event(), asyncio.Event()
        update = db.update_conversation

        async def delayed(**kwargs):
            entered.set()
            await release.wait()
            await update(**kwargs)

        db.update_conversation = delayed
        resetting = asyncio.create_task(reset_native_sessions(context, (_reset_target_for(event, reference),)))
        await entered.wait()
        queued = Event()
        reading = asyncio.create_task(read_existing_history(context, queued, scope=queued.scope))
        await asyncio.sleep(0)
        assert not reading.done()
        release.set()
        assert (await resetting).status == "ready"
        assert (await reading).status == "scope_mismatch"
        assert (await ensure_native_session(context, queued, scope=queued.scope)).status == "scope_mismatch"
        assert db.rows[reference.cid].content == []

    asyncio.run(run())


def test_reset_rechecks_active_events_after_native_validation_read(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        read = context.conversation_manager.get_conversation
        original = db.rows[reference.cid].content

        async def became_busy(*args, **kwargs):
            conversation = await read(*args, **kwargs)
            native_activity.register(event)
            return conversation

        context.conversation_manager.get_conversation = became_busy
        result = await reset_native_sessions(context, (_reset_target_for(event, reference),))
        assert result.status == "busy" and db.rows[reference.cid].content is original

    asyncio.run(run())


def test_all_reset_fails_explicitly_when_native_enumeration_is_unavailable(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        context.conversation_manager.get_conversations = None
        original = db.rows[reference.cid].content
        result = await reset_native_sessions(context, (_reset_target_for(event, reference),), all_sessions=True)
        assert result.status == "unavailable" and result.reset_conversations == 0
        assert db.rows[reference.cid].content is original

    asyncio.run(run())


def test_reset_reports_partial_writes_and_invalidates_each_attempted_scope(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        other, other_ref, _, _ = await _bound_history_event(context, sender="10002")
        original = [{"role": "assistant", "content": "历史"}]
        for row in db.rows.values():
            row.content = original
        update = db.update_conversation

        async def second_fails(**kwargs):
            if kwargs["cid"] == other_ref.cid:
                raise RuntimeError("synthetic write failure")
            await update(**kwargs)

        db.update_conversation = second_fails
        result = await reset_native_sessions(context, (
            _reset_target_for(event, reference), _reset_target_for(other, other_ref),
        ))
        assert result.status == "partial" and result.reset_conversations == 1
        assert set(result.reset_scopes) == {event.scope, other.scope}
        assert db.rows[reference.cid].content == []
        assert db.rows[other_ref.cid].content is original
        assert get_session_reference(event, scope=event.scope) is None
        assert get_session_reference(other, scope=other.scope) is None

    asyncio.run(run())


def test_cancelled_reset_releases_locks_and_invalidates_late_references(native, native_activity):
    context, db, _ = native

    async def run():
        event, reference, _, _ = await _bound_history_event(context)
        target = _reset_target_for(event, reference)
        entered = asyncio.Event()
        update = db.update_conversation

        async def blocked(**kwargs):
            entered.set()
            await asyncio.Event().wait()

        db.update_conversation = blocked
        operation = asyncio.create_task(reset_native_sessions(context, (target,)))
        await entered.wait()
        operation.cancel()
        with pytest.raises(asyncio.CancelledError):
            await operation
        assert get_session_reference(event, scope=event.scope) is None
        db.update_conversation = update
        assert (await reset_native_sessions(context, (target,))).status == "ready"

    asyncio.run(run())
