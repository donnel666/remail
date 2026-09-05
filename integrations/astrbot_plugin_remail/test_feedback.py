import ast
import asyncio
import json
import re
from datetime import datetime, time, timezone
from pathlib import Path
from types import SimpleNamespace
from typing import Any
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
    parse_report_time,
    sanitize_feedback_text,
    sanitize_report,
)
from .security import (
    adapter_channel,
    contains_credentials,
    normalize_security_text,
    redact_credentials,
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
    helpers = [
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef)
        and node.name in {"_positive_platform_id", "_configured_qq_management"}
    ]
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
        "Any": Any,
        "DailyFeedback": DailyFeedback,
        "MessageChain": lambda items: items,
        "MessageType": _MessageType,
        "Plain": lambda text: text,
        "ReMailError": _ReMailError,
        "UNRESOLVED_ACK": UNRESOLVED_ACK,
        "adapter_channel": adapter_channel,
        "logger": SimpleNamespace(warning=lambda *_args: None),
        "re": re,
        "sanitize_feedback_text": sanitize_feedback_text,
        "_safe_egress_text": lambda text, **_kwargs: text,
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*argument_patterns, *helpers, *body], type_ignores=[])
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
    assert sanitize_feedback_text(normal_problem) == normalize_security_text(
        normal_problem
    )
    zero_width = sanitize_feedback_text(
        "密\u200b码：hunter2 邮箱：user@exa\u200bmple.com"
    )
    assert "hunter2" not in zero_width
    assert "user@example.com" not in zero_width


_TEST_OPENAI_KEY = "s" + "k-proj-" + "A" * 32
_TEST_GITHUB_TOKEN = "g" + "hp_" + "a" * 36
_TEST_AWS_LONG_TERM_ID = "A" + "KIA" + "0" * 16
_TEST_AWS_TEMPORARY_ID = "A" + "SIA" + "0" * 16
_TEST_AWS_SECRET = "synthetic/" + "A" * 32
_TEST_AWS_SESSION = "synthetic-session-" + "B" * 24
_TEST_JWT = ".".join(
    ("eyJhbGciOiJIUzI1NiJ9", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", "signature")
)
_TEST_PEM_PRIVATE_KEY = (
    "-----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADANBgkqh\n-----END PRIVATE KEY-----"
)
_TEST_PGP_PRIVATE_KEY = (
    "-----BEGIN "
    + "PGP PRIVATE KEY BLOCK-----\nPGP_SECRET\n-----END PGP PRIVATE KEY BLOCK-----"
)


@pytest.mark.parametrize(
    ("raw", "secrets"),
    [
        (
            '{"password": "correct horse battery staple", "token": "abc def ghi"}',
            ("correct horse battery staple", "abc def ghi"),
        ),
        (
            "password: correct horse, battery staple",
            ("correct horse", "battery staple"),
        ),
        (
            "Cookie: session=FIRST_SECRET; refresh=SECOND_SECRET",
            ("FIRST_SECRET", "SECOND_SECRET"),
        ),
        ("Token: opaque-token-value", ("opaque-token-value",)),
        ('{"db_password":"hunter2"}', ("hunter2",)),
        (
            '{"new_password":"correct horse battery staple"}',
            ("correct horse battery staple",),
        ),
        ('{"session_token":"opaque-secret-token"}', ("opaque-secret-token",)),
        ('{"apiToken":"opaque-secret-token"}', ("opaque-secret-token",)),
        ("api.key=opaque-secret-value", ("opaque-secret-value",)),
        ("X-Auth-Token: opaque-secret-token", ("opaque-secret-token",)),
        ('{"pwd":"hunter2"}', ("hunter2",)),
        ('{"pass":"hunter2"}', ("hunter2",)),
        ('{"passcode":"654321"}', ("654321",)),
        ('{"credentials":"opaque-secret-value"}', ("opaque-secret-value",)),
        ("PWD=correct horse battery staple", ("correct horse battery staple",)),
        ("我的密码是 hunter2", ("hunter2",)),
        ("密碼：hunter2", ("hunter2",)),
        ("登录密码是 hunter2", ("hunter2",)),
        ("新密码是 hunter2", ("hunter2",)),
        ("账号密码是 hunter2", ("hunter2",)),
        ("邮箱密码为 hunter2", ("hunter2",)),
        ("数据库密码是 hunter2", ("hunter2",)),
        ("临时口令设置为 hunter2", ("hunter2",)),
        ("口令设为 hunter2", ("hunter2",)),
        ("密码改成 hunter2", ("hunter2",)),
        ("账号密码就是 hunter2", ("hunter2",)),
        ("密码设成 hunter2", ("hunter2",)),
        ("密码换成 hunter2", ("hunter2",)),
        ("密码修改成 hunter2", ("hunter2",)),
        ("口令定为 hunter2", ("hunter2",)),
        ("短信验证码是 123456", ("123456",)),
        ("邮件验证码为 ABCDEF", ("ABCDEF",)),
        ("登录验证码就是 654321", ("654321",)),
        ("一次性验证码 998877", ("998877",)),
        ("邮箱验证码设为 112233", ("112233",)),
        ("密码设置为 hunter2", ("hunter2",)),
        ("Token 设置为 opaque-secret-token", ("opaque-secret-token",)),
        ("password is hunter2?", ("hunter2",)),
        ("密码是 hunter2 吗？", ("hunter2",)),
        ("Token opaque-secret-token?", ("opaque-secret-token",)),
        ("API Key abc12345 是否正确？", ("abc12345",)),
        ("password hunter2 must change", ("hunter2",)),
        ("token abcdefghijkl length 12", ("abcdefghijkl",)),
        ("password is reset123", ("reset123",)),
        ("password is encryptedSecret42", ("encryptedSecret42",)),
        ("token 参数使用 Bearer 格式 abcsecret", ("abcsecret",)),
        ("API Key 应放在 sk-secret 请求头", ("sk-secret",)),
        ("密码是怎么123", ("怎么123",)),
        (
            _TEST_PEM_PRIVATE_KEY,
            ("MIIEvQIBADANBgkqh",),
        ),
        (
            _TEST_PGP_PRIVATE_KEY,
            ("PGP_SECRET",),
        ),
        ("恢复码：abcd-efgh-ijkl", ("abcd-efgh-ijkl",)),
        (
            "Recovery codes: alpha-bravo-charlie, delta-echo-foxtrot",
            ("alpha-bravo-charlie", "delta-echo-foxtrot"),
        ),
        (
            _TEST_OPENAI_KEY,
            (_TEST_OPENAI_KEY,),
        ),
        (
            _TEST_GITHUB_TOKEN,
            (_TEST_GITHUB_TOKEN,),
        ),
        (
            "https://alice:SuperSecret@example.com/api",
            ("alice", "SuperSecret"),
        ),
        (
            "amqp://guest:RabbitSecret@rabbitmq.internal/vhost",
            ("guest", "RabbitSecret"),
        ),
        (
            "amqp://:RabbitSecret@rabbitmq.internal/vhost",
            ("RabbitSecret",),
        ),
        (
            "https://:ApiToken@example.com/path",
            ("ApiToken",),
        ),
        (
            "postgresql+asyncpg://root:DbSecret@db.internal/remail",
            ("root", "DbSecret"),
        ),
        (
            "smtp://mailer:MailSecret@smtp.example.com",
            ("mailer", "MailSecret"),
        ),
        (
            _TEST_JWT,
            ("eyJhbGciOiJIUzI1NiJ9", "signature"),
        ),
        (
            f"AWS_ACCESS_KEY_ID={_TEST_AWS_LONG_TERM_ID}",
            (_TEST_AWS_LONG_TERM_ID,),
        ),
        (
            f"AWS_ACCESS_KEY_ID={_TEST_AWS_TEMPORARY_ID}",
            (_TEST_AWS_TEMPORARY_ID,),
        ),
        (_TEST_AWS_LONG_TERM_ID, (_TEST_AWS_LONG_TERM_ID,)),
        (_TEST_AWS_TEMPORARY_ID, (_TEST_AWS_TEMPORARY_ID,)),
        (
            f"AWS_SECRET_ACCESS_KEY={_TEST_AWS_SECRET}",
            (_TEST_AWS_SECRET,),
        ),
        (
            f"AWS_SESSION_TOKEN={_TEST_AWS_SESSION}",
            (_TEST_AWS_SESSION,),
        ),
    ],
)
def test_shared_credential_redaction_is_complete_and_idempotent(
    raw: str, secrets: tuple[str, ...]
) -> None:
    clean = redact_credentials(raw)
    assert clean != normalize_security_text(raw)
    assert all(secret not in clean for secret in secrets)
    assert not contains_credentials(clean)
    assert redact_credentials(clean) == clean


def test_shared_credential_redaction_preserves_descriptions_and_placeholders() -> None:
    safe_values = (
        "如何重置密码",
        "密码 无法重置",
        "密码是否为空",
        "password is required",
        "password is not required",
        "token is expired",
        "API Key 无法使用",
        "System Key 如何保存",
        "cookie is optional",
        "API Key 应放在 Authorization 请求头",
        "token 参数使用 Bearer 格式",
        "调用公开 API 时，请在 Bearer 请求头提供 API Key。",
        "API Key 应通过 Authorization 请求头传递吗？",
        "token 参数应该采用 Bearer 方案吗？",
        "Cookie 应设置 HttpOnly 属性吗？",
        "password must contain 12 characters吗？",
        "token budget is 1000",
        "API Key 放在 Authorization 头部",
        "Token 使用 Bearer 鉴权",
        "Cookie 开启 HttpOnly",
        "API Key 存放于环境变量",
        "Token 采用 Bearer 鉴权",
        "Cookie 启用 HttpOnly",
        "password requirements include 12 characters",
        "API Key rotation policy",
        "Token authentication overview",
        "验证码 收不到",
        "验证码 一直没来",
        "验证码 迟迟不来",
        "登录密码 忘记了",
        "API 密钥 无效",
        "口令 不正确",
        "Token 验证失败",
        "Cookie 被禁用",
        "重置密码 失败怎么办",
        "修改密码 报错",
        "API 密钥 管理指南",
        "api.key 字段如何填写",
        "AWS_ACCESS_KEY_ID 字段是什么",
        "AWS_SECRET_ACCESS_KEY 应存放在环境变量吗？",
        '{"password":"<PASSWORD>","token":"${TOKEN}",'
        '"authorization":"Bearer <API_KEY>"}',
        "AWS_ACCESS_KEY_ID=<AWS_ACCESS_KEY_ID>",
        "AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY}",
        "AWS_SESSION_TOKEN=<AWS_SESSION_TOKEN>",
    )
    for value in safe_values:
        assert redact_credentials(value) == normalize_security_text(value)
        assert not contains_credentials(value)


def test_feedback_redacts_numeric_codes_without_hiding_http_status() -> None:
    clean = sanitize_feedback_text("code 1234；OTP: 654321；status code 200")
    assert "1234" not in clean
    assert "654321" not in clean
    assert "status code 200" in clean


def test_feedback_report_uses_a_non_reversible_group_reference() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    method = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "_feedback_report"
    )
    source = ast.unparse(method)
    assert "hashlib.sha256" in source
    assert "来源群标识" in source
    assert "来源群：{group_id}" not in source


def test_feedback_uses_shared_credential_gate_without_secret_suffixes() -> None:
    raw = (
        "Cookie: session=FIRST_SECRET; refresh=SECOND_SECRET\n"
        '{"password":"correct horse battery staple","token":"opaque token"}\n'
        f"{_TEST_OPENAI_KEY}\n"
        f"{_TEST_PEM_PRIVATE_KEY}"
    )
    clean = sanitize_feedback_text(raw)
    for secret in (
        "FIRST_SECRET",
        "SECOND_SECRET",
        "correct horse battery staple",
        "opaque token",
        _TEST_OPENAI_KEY,
        "MIIE_PRIVATE_MATERIAL",
    ):
        assert secret not in clean
    assert sanitize_feedback_text(clean) == clean


def test_report_schedule_uses_configured_shanghai_time() -> None:
    before = datetime(2026, 9, 1, 11, 59, tzinfo=timezone.utc)  # 19:59 Shanghai
    at_report = datetime(2026, 9, 1, 12, 0, tzinfo=timezone.utc)
    assert feedback_day(before) == "2026-09-01"
    assert feedback_day(at_report) == "2026-09-02"
    assert next_report_at(before).isoformat() == "2026-09-01T20:00:00+08:00"
    assert next_report_at(at_report).isoformat() == "2026-09-02T20:00:00+08:00"
    configured = parse_report_time("09:30")
    before_custom = datetime(2026, 9, 1, 1, 29, tzinfo=timezone.utc)
    at_custom = datetime(2026, 9, 1, 1, 30, tzinfo=timezone.utc)
    assert feedback_day(before_custom, configured) == "2026-09-01"
    assert feedback_day(at_custom, configured) == "2026-09-02"
    assert next_report_at(before_custom, configured).isoformat() == (
        "2026-09-01T09:30:00+08:00"
    )
    assert next_report_at(at_custom, configured).isoformat() == (
        "2026-09-02T09:30:00+08:00"
    )
    with pytest.raises(ValueError, match="HH:MM"):
        parse_report_time("24:00")
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
    assert "工作日报" in prompt
    assert "工作日报" not in report
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
        feedback_report_time=time(20),
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
        feedback_report_time=time(20),
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
        feedback_report_time=time(20),
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


def test_feedback_owner_change_does_not_drop_new_items() -> None:
    record_feedback = _main_feedback_functions()["_record_feedback"]
    old = DailyFeedback()
    assert old.add("feedback", "旧反馈", owner_umo="bot:FriendMessage:11111")
    storage = {"feedback:test": old.dump()}

    async def get_value(key, default):
        return storage.get(key, default)

    async def put_value(key, value):
        storage[key] = value

    plugin = SimpleNamespace(
        feedback_lock=asyncio.Lock(),
        feedback_seen=set(),
        feedback_report_time=time(20),
        _feedback_enabled=lambda: True,
        _feedback_authorized=AsyncMock(return_value=(True, "")),
        _feedback_group_metadata=AsyncMock(
            return_value=(
                "bot:group",
                {
                    "platformId": "bot",
                    "ownerUmo": "bot:FriendMessage:9845248",
                },
            )
        ),
        _valid_feedback_umo=lambda *_args: True,
        _feedback_store_key=lambda _group: "feedback:test",
        get_kv_data=AsyncMock(side_effect=get_value),
        put_kv_data=AsyncMock(side_effect=put_value),
    )
    event = SimpleNamespace(message_obj=SimpleNamespace(message_id="new-owner"))

    assert asyncio.run(
        record_feedback(plugin, event, "feedback", "新群主接手后的反馈")
    ) == (True, "")
    items = DailyFeedback(storage["feedback:test"]).snapshot(feedback_day())["items"]
    assert [item["text"] for item in items][-1] == "新群主接手后的反馈"


def test_non_whitelisted_group_is_not_recorded() -> None:
    functions = _main_feedback_functions()
    event = SimpleNamespace(
        message_obj=SimpleNamespace(message_id="denied-message"),
        get_message_type=lambda: _MessageType.GROUP_MESSAGE,
        get_platform_name=lambda: "aiocqhttp",
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


def test_work_report_is_qq_only() -> None:
    authorize = _main_feedback_functions()["_feedback_authorized"]
    event = SimpleNamespace(
        get_message_type=lambda: _MessageType.GROUP_MESSAGE,
        get_platform_name=lambda: "telegram",
    )
    plugin = SimpleNamespace(_authorize_event=AsyncMock())

    assert asyncio.run(authorize(plugin, event)) == (
        False,
        "工作日报仅支持 QQ 群。",
    )
    plugin._authorize_event.assert_awaited_once_with(event)


def test_empty_feedback_command_checks_group_authorization_before_format_help() -> None:
    submit = _main_feedback_functions()["_submit_feedback_command"]
    stopped = []
    event = SimpleNamespace(
        message_str="/反馈",
        send=AsyncMock(),
        stop_event=lambda: stopped.append(True),
    )
    authorize = AsyncMock(return_value=(False, "当前群未获授权。"))

    async def reply(target_event, text):
        await target_event.send([text])
        target_event.stop_event()

    plugin = SimpleNamespace(_feedback_authorized=authorize, _reply=reply)

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
        config={"qq_group_owner_id": "99999"},
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
        feedback_report_time=time(20),
        _feedback_store_key=lambda _group: "feedback:test",
        _feedback_report=AsyncMock(return_value="日报"),
        _valid_feedback_umo=lambda *_args: True,
        get_kv_data=AsyncMock(side_effect=get_value),
        put_kv_data=AsyncMock(side_effect=put_value),
    )

    assert asyncio.run(send_reports(plugin)) is (not sent)
    send_message.assert_awaited_once()
    assert send_message.await_args.args[0] == "bot:FriendMessage:99999"
    remaining = DailyFeedback(storage["feedback:test"]).snapshot("2020-01-01")["items"]
    assert bool(remaining) is (not sent)
    assert plugin.put_kv_data.await_count == int(sent)
