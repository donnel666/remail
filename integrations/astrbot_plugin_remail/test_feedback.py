import ast
import asyncio
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .feedback import (
    MAX_INPUT_CHARS,
    MAX_ITEM_CHARS,
    MAX_PROMPT_CHARS,
    MAX_REPORT_CHARS,
    UNRESOLVED_ACK,
    DailyFeedback,
    build_summary_prompt,
    fallback_report,
    feedback_day,
    next_report_at,
    sanitize_feedback_text,
    sanitize_report,
)


PLUGIN_DIR = Path(__file__).parent


class _MessageType:
    GROUP_MESSAGE = "group"
    FRIEND_MESSAGE = "friend"


class _ReMailError(RuntimeError):
    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.message = message


def _main_feedback_functions():
    names = {
        "_feedback_authorized",
        "_record_feedback",
        "_send_due_feedback_reports",
        "_submit_feedback_command",
        "remail_record_unresolved",
    }
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    body = [
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name in names
    ]
    for node in body:
        node.decorator_list = []
    argument_patterns = [
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id in {"_DIAGNOSIS_ARGUMENTS", "_FEEDBACK_ARGUMENTS"}
            for target in node.targets
        )
    ]
    namespace = {
        "AstrMessageEvent": object,
        "DailyFeedback": DailyFeedback,
        "MessageChain": lambda items: items,
        "MessageType": _MessageType,
        "Plain": lambda text: text,
        "ReMailError": _ReMailError,
        "UNRESOLVED_ACK": UNRESOLVED_ACK,
        "logger": SimpleNamespace(warning=lambda *_args: None),
        "re": re,
        "sanitize_feedback_text": sanitize_feedback_text,
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*argument_patterns, *body], type_ignores=[])
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )
    return namespace


def test_redaction_and_limits() -> None:
    text = (
        "/诊断 user@example.com 一直没收到\n"
        "/绑定 other@example.com password\n"
        "X-System-Key: sk_abcdefgh mysql://root:pass@db/remail "
        "QQ: 123456789 password=hunter2 Bearer abcdefghijk "
        "Authorization: Bearer abc.def.ghi "
        "order_id=ORDER-2026-0001 API Key api-secret System Key system-secret "
        "密码 hunter3 验证码 1234 账号 test_user OTP 123456 "
        "username test_name code 654321"
    )
    clean = sanitize_feedback_text(text + "x" * (MAX_INPUT_CHARS + 10))
    assert "example.com" not in clean
    assert "sk_abcdefgh" not in clean
    assert "mysql://" not in clean
    assert "123456789" not in clean
    assert "hunter2" not in clean
    assert "abcdefghijk" not in clean
    assert "abc.def.ghi" not in clean
    assert "ORDER-2026-0001" not in clean
    for secret in (
        "api-secret",
        "system-secret",
        "hunter3",
        "1234",
        "test_user",
        "123456",
        "test_name",
        "654321",
    ):
        assert secret not in clean
    assert "/诊断 [参数已隐藏]" in clean
    assert "/绑定 [参数已隐藏]" in clean
    assert len(clean) <= MAX_ITEM_CHARS
    assert len(sanitize_report("x" * (MAX_REPORT_CHARS + 10))) == MAX_REPORT_CHARS

    normal_problem = "验证码 没收到；账号 无法登录；密码 无法重置；API Key 无法使用；System Key 无法使用"
    assert sanitize_feedback_text(normal_problem) == normal_problem


def test_report_day_and_next_report_use_shanghai_20_oclock() -> None:
    before = datetime(2026, 9, 1, 11, 59, tzinfo=timezone.utc)  # 19:59 Shanghai
    at_report = datetime(2026, 9, 1, 12, 0, tzinfo=timezone.utc)
    assert feedback_day(before) == "2026-09-01"
    assert feedback_day(at_report) == "2026-09-02"
    assert next_report_at(before).isoformat() == "2026-09-01T20:00:00+08:00"
    assert next_report_at(at_report).isoformat() == "2026-09-02T20:00:00+08:00"
    with pytest.raises(ValueError, match="timezone"):
        next_report_at(datetime(2026, 9, 1, 19, 0))


def test_daily_buffer_is_bounded_serializable_and_tracks_unresolved() -> None:
    now = datetime(2026, 9, 1, 10, 0, tzinfo=timezone.utc)
    store = DailyFeedback(max_items=2, max_days=2)
    assert store.add("feedback", "接码速度慢", now)
    assert store.add("unresolved", "AI 无法解决这个问题", now)
    assert not store.add("suggestion", "增加筛选", now)
    snapshot = store.snapshot("2026-09-01")
    assert [item["kind"] for item in snapshot["items"]] == [
        "feedback",
        "unresolved",
    ]
    assert snapshot["dropped"] == 1
    assert DailyFeedback(store.dump(), max_items=2).dump() == store.dump()
    assert UNRESOLVED_ACK == "已记录该问题，并反馈给研发。"

    prioritized = DailyFeedback(max_items=2)
    assert prioritized.add("implicit", "线索一", now)
    assert prioritized.add("implicit", "线索二", now)
    assert prioritized.add("unresolved", "客服无法解决", now)
    assert [item["kind"] for item in prioritized.snapshot("2026-09-01")["items"]] == [
        "implicit",
        "unresolved",
    ]

    routed = DailyFeedback()
    assert routed.add("feedback", "旧群主的问题", now, owner_umo="bot:FriendMessage:1")
    assert not routed.add(
        "feedback", "新群主的问题", now, owner_umo="bot:FriendMessage:2"
    )
    assert routed.snapshot("2026-09-01")["ownerUmo"] == "bot:FriendMessage:1"


def test_due_days_survive_a_late_restart() -> None:
    store = DailyFeedback()
    store.add(
        "implicit",
        "库存显示可用但无法购买",
        datetime(2026, 9, 1, 11, 0, tzinfo=timezone.utc),
    )
    assert store.due_days(datetime(2026, 9, 1, 11, 59, tzinfo=timezone.utc)) == []
    assert store.due_days(datetime(2026, 9, 1, 13, 0, tzinfo=timezone.utc)) == [
        "2026-09-01"
    ]


def test_prompt_and_fallback_have_counts_without_identity_or_sensitive_data() -> None:
    dirty = {
        "day": "2026-09-01",
        "items": [
            {"kind": "suggestion", "text": "建议联系 user@example.com", "at": "10:00"},
            {"kind": "unresolved", "text": "TG ID: 123456789", "at": "11:00"},
            {"kind": "feedback", "text": "API Key prompt-secret", "at": "12:00"},
        ],
        "dropped": 3,
    }
    prompt = build_summary_prompt(dirty)
    report = fallback_report(dirty)
    for value, limit in ((prompt, MAX_PROMPT_CHARS), (report, MAX_REPORT_CHARS)):
        assert len(value) <= limit
        assert "user@example.com" not in value
        assert "123456789" not in value
        assert "prompt-secret" not in value
    assert "未解决 1 条" in prompt
    assert "未解决 1 条" in report
    assert "另有 3 条未纳入明细" in prompt

    large = DailyFeedback()
    now = datetime(2026, 9, 1, 10, 0, tzinfo=timezone.utc)
    for index in range(200):
        large.add("feedback", f"{index}:" + "x" * MAX_ITEM_CHARS, now)
    bounded_prompt = build_summary_prompt(large.snapshot("2026-09-01"))
    assert len(bounded_prompt) <= MAX_PROMPT_CHARS
    json.loads(bounded_prompt.split("记录（JSON）：\n", 1)[1])


def test_unresolved_tool_records_reason_and_returns_safe_llm_status() -> None:
    functions = _main_feedback_functions()
    record = AsyncMock(return_value=(True, ""))
    plugin = SimpleNamespace(_record_feedback=record)
    extra = {}
    event = SimpleNamespace(
        message_str="/诊断 user@example.com 始终收不到验证码",
        set_extra=lambda key, value: extra.__setitem__(key, value),
    )

    result = asyncio.run(functions["remail_record_unresolved"](plugin, event))
    assert result == f"{UNRESOLVED_ACK} 请用自然、简短的中文告知用户。"
    assert extra == {"_remail_unresolved_recorded": True}
    record.assert_awaited_once_with(event, "unresolved", "始终收不到验证码")


@pytest.mark.parametrize(
    ("record_result", "expected"),
    [
        ((False, ""), f"{UNRESOLVED_ACK} 请用自然、简短的中文告知用户。"),
        (
            (False, "记录失败"),
            "问题暂时未能记录；请告知用户稍后重试，不要声称已经记录。",
        ),
    ],
)
def test_unresolved_tool_treats_duplicates_as_recorded_without_leaking_errors(
    record_result: tuple[bool, str], expected: str
) -> None:
    tool = _main_feedback_functions()["remail_record_unresolved"]
    extra = {}
    event = SimpleNamespace(
        message_str="这个问题还是无法解决",
        set_extra=lambda key, value: extra.__setitem__(key, value),
    )
    plugin = SimpleNamespace(_record_feedback=AsyncMock(return_value=record_result))

    assert asyncio.run(tool(plugin, event)) == expected
    assert bool(extra) is (not record_result[1])


def test_unresolved_tool_hides_unexpected_storage_errors() -> None:
    tool = _main_feedback_functions()["remail_record_unresolved"]
    event = SimpleNamespace(
        message_str="无法解决",
        set_extra=lambda *_args: pytest.fail("failed records must not be marked"),
    )
    plugin = SimpleNamespace(
        _record_feedback=AsyncMock(side_effect=RuntimeError("private traceback"))
    )

    result = asyncio.run(tool(plugin, event))
    assert result == "问题暂时未能记录；请告知用户稍后重试，不要声称已经记录。"
    assert "traceback" not in result


def test_same_message_id_is_recorded_once() -> None:
    record_feedback = _main_feedback_functions()["_record_feedback"]
    storage = {}

    async def get_value(key, default):
        return storage.get(key, default)

    async def put_value(key, value):
        storage[key] = value

    plugin = SimpleNamespace(
        config={"feedback_enabled": True},
        feedback_lock=asyncio.Lock(),
        feedback_seen=set(),
        _feedback_enabled=lambda: True,
        _feedback_authorized=AsyncMock(return_value=(True, "")),
        _feedback_group_metadata=AsyncMock(
            return_value=(
                "bot:group",
                {
                    "platformId": "bot",
                    "ownerUmo": "bot:FriendMessage:12345",
                },
            )
        ),
        _valid_feedback_umo=lambda *_args: True,
        _feedback_store_key=lambda _group: "feedback:test",
        get_kv_data=AsyncMock(side_effect=get_value),
        put_kv_data=AsyncMock(side_effect=put_value),
    )
    event = SimpleNamespace(message_obj=SimpleNamespace(message_id="message-42"))

    async def run_twice():
        first = await record_feedback(plugin, event, "unresolved", "还是无法处理")
        second = await record_feedback(plugin, event, "unresolved", "还是无法处理")
        return first, second

    assert asyncio.run(run_twice()) == ((True, ""), (False, ""))
    items = next(iter(storage["feedback:test"]["days"].values()))["items"]
    assert len(items) == 1
    assert plugin.put_kv_data.await_count == 1


def test_full_day_does_not_mark_an_unstored_message_as_duplicate_success() -> None:
    record_feedback = _main_feedback_functions()["_record_feedback"]
    owner_umo = "bot:FriendMessage:12345"
    full = DailyFeedback()
    for index in range(200):
        assert full.add("feedback", f"已存反馈 {index}", owner_umo=owner_umo)
    storage = {"feedback:test": full.dump()}

    async def get_value(key, default):
        return storage.get(key, default)

    async def put_value(key, value):
        storage[key] = value

    plugin = SimpleNamespace(
        feedback_lock=asyncio.Lock(),
        feedback_seen=set(),
        _feedback_enabled=lambda: True,
        _feedback_authorized=AsyncMock(return_value=(True, "")),
        _feedback_group_metadata=AsyncMock(
            return_value=(
                "bot:group",
                {"platformId": "bot", "ownerUmo": owner_umo},
            )
        ),
        _valid_feedback_umo=lambda *_args: True,
        _feedback_store_key=lambda _group: "feedback:test",
        get_kv_data=AsyncMock(side_effect=get_value),
        put_kv_data=AsyncMock(side_effect=put_value),
    )
    event = SimpleNamespace(message_obj=SimpleNamespace(message_id="full-message"))

    async def run_twice():
        return (
            await record_feedback(plugin, event, "unresolved", "无法解决"),
            await record_feedback(plugin, event, "unresolved", "无法解决"),
        )

    assert asyncio.run(run_twice()) == (
        (False, "暂时无法记录，请稍后再试。"),
        (False, "暂时无法记录，请稍后再试。"),
    )
    assert not plugin.feedback_seen


def test_feedback_without_a_valid_owner_is_not_stored() -> None:
    record_feedback = _main_feedback_functions()["_record_feedback"]
    get_value = AsyncMock()
    put_value = AsyncMock()
    plugin = SimpleNamespace(
        feedback_lock=asyncio.Lock(),
        feedback_seen=set(),
        _feedback_enabled=lambda: True,
        _feedback_authorized=AsyncMock(return_value=(True, "")),
        _feedback_group_metadata=AsyncMock(
            return_value=(
                "bot:group",
                {"platformId": "bot", "ownerUmo": ""},
            )
        ),
        _valid_feedback_umo=lambda *_args: False,
        get_kv_data=get_value,
        put_kv_data=put_value,
    )
    event = SimpleNamespace(message_obj=SimpleNamespace(message_id="message-43"))

    result = asyncio.run(
        record_feedback(plugin, event, "feedback", "这条消息没有接收人")
    )
    assert result == (False, "暂时无法记录，请稍后再试。")
    get_value.assert_not_awaited()
    put_value.assert_not_awaited()


def test_non_whitelisted_group_is_not_recorded() -> None:
    functions = _main_feedback_functions()
    event = SimpleNamespace(
        message_obj=SimpleNamespace(message_id="denied-message"),
        get_message_type=lambda: _MessageType.GROUP_MESSAGE,
    )
    get_value = AsyncMock()
    put_value = AsyncMock()
    group_metadata = AsyncMock()
    plugin = SimpleNamespace(
        feedback_lock=asyncio.Lock(),
        feedback_seen=set(),
        _feedback_enabled=lambda: True,
        _authorize_event=AsyncMock(side_effect=_ReMailError(401, "来源群鉴权失败。")),
        _feedback_group_metadata=group_metadata,
        get_kv_data=get_value,
        put_kv_data=put_value,
    )

    async def authorized(current_event):
        return await functions["_feedback_authorized"](plugin, current_event)

    plugin._feedback_authorized = authorized
    result = asyncio.run(
        functions["_record_feedback"](plugin, event, "feedback", "这是一条反馈")
    )
    assert result == (False, "当前群未获授权。")
    group_metadata.assert_not_awaited()
    get_value.assert_not_awaited()
    put_value.assert_not_awaited()


def test_empty_feedback_command_checks_group_authorization_before_format_help() -> None:
    submit = _main_feedback_functions()["_submit_feedback_command"]
    stopped = []
    event = SimpleNamespace(
        message_str="/反馈",
        send=AsyncMock(),
        stop_event=lambda: stopped.append(True),
    )
    authorize = AsyncMock(return_value=(False, "当前群未获授权。"))
    plugin = SimpleNamespace(_feedback_authorized=authorize)

    asyncio.run(submit(plugin, event, "feedback", "反馈"))
    authorize.assert_awaited_once_with(event)
    event.send.assert_awaited_once_with(["当前群未获授权。"])
    assert stopped == [True]


@pytest.mark.parametrize("sent", [True, False])
def test_daily_report_discards_only_after_success(sent: bool) -> None:
    send_reports = _main_feedback_functions()["_send_due_feedback_reports"]
    store = DailyFeedback()
    store.add(
        "feedback",
        "历史反馈",
        datetime(2020, 1, 1, 1, 0, tzinfo=timezone.utc),
        owner_umo="bot:FriendMessage:12345",
    )
    storage = {"feedback:test": store.dump()}

    async def get_value(key, default):
        return storage.get(key, default)

    async def put_value(key, value):
        storage[key] = value

    send_message = AsyncMock(return_value=sent)
    plugin = SimpleNamespace(
        config={},
        context=SimpleNamespace(send_message=send_message),
        feedback_groups={
            "bot:group": {
                "platformId": "bot",
                "groupId": "group",
                "groupUmo": "bot:GroupMessage:group",
                "ownerUmo": "bot:FriendMessage:99999",
            }
        },
        feedback_lock=asyncio.Lock(),
        _feedback_target_overrides=lambda: {},
        _feedback_store_key=lambda _group: "feedback:test",
        _feedback_report=AsyncMock(return_value="日报"),
        _valid_feedback_umo=lambda *_args: True,
        get_kv_data=AsyncMock(side_effect=get_value),
        put_kv_data=AsyncMock(side_effect=put_value),
    )

    assert asyncio.run(send_reports(plugin)) is (not sent)
    send_message.assert_awaited_once()
    assert send_message.await_args.args[0] == "bot:FriendMessage:12345"
    remaining = DailyFeedback(storage["feedback:test"]).snapshot("2020-01-01")["items"]
    assert bool(remaining) is (not sent)
    assert plugin.put_kv_data.await_count == int(sent)
