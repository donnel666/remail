"""Offline checks against the documented NapCat and Telegram response shapes."""

import asyncio
import json
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from . import group_context
from .group_context import MAX_ITEMS, MAX_TOTAL_CHARS, load_group_context


def _event(
    adapter="aiocqhttp", group_id="10001", *, private=False, self_id="900000001"
):
    return SimpleNamespace(
        get_message_type=lambda: "friend" if private else "group",
        get_platform_name=lambda: adapter,
        get_group_id=lambda: group_id,
        get_sender_id=lambda: "123456789",
        get_self_id=lambda: self_id,
        message_obj=SimpleNamespace(self_id=self_id),
        bot=SimpleNamespace(call_action=AsyncMock()),
        client=SimpleNamespace(get_chat=AsyncMock()),
    )


def _notice(text="购买模式可以持续使用，售后期限另计。", at=None):
    return {
        "publish_time": at or datetime.now(timezone.utc).timestamp(),
        "notice_id": "PRIVATE_MESSAGE_ID",
        "sender_id": 987654321,
        "message": {"text": text, "images": [{"url": "PRIVATE_IMAGE"}]},
    }


def test_denied_private_and_invalid_group_never_access_platform():
    async def check():
        assert (await load_group_context(object()))["status"] == "not_authorized"
        for event in (
            _event(private=True),
            _event(group_id="not-a-group"),
            _event("telegram", "12345"),
        ):
            result = await load_group_context(event, authorized=True)
            assert result["status"] in {"not_group", "unavailable"}
            event.bot.call_action.assert_not_awaited()
            event.client.get_chat.assert_not_awaited()
        event = _event()
        assert (await load_group_context(event, authorized="true"))[
            "status"
        ] == "not_authorized"
        event.bot.call_action.assert_not_awaited()

    asyncio.run(check())


def test_qq_documented_shapes_are_independent_identity_free_weak_sources():
    event = _event()
    event.bot.call_action.side_effect = [
        {"status": "ok", "retcode": 0, "data": [_notice()]},
        [
            {
                "operator_time": datetime.now(timezone.utc).timestamp(),
                "sender_id": 987654321,
                "sender_nick": "PRIVATE_NICK",
                "operator_id": 887654321,
                "operator_nick": "PRIVATE_OPERATOR",
                "message_id": "PRIVATE_ID",
                "content": [
                    {
                        "type": "text",
                        "data": {"text": "接码模式的使用窗口以项目配置为准。"},
                    },
                    {
                        "type": "image",
                        "data": {"url": "PRIVATE_IMAGE", "file": "PRIVATE_FILE"},
                    },
                ],
            }
        ],
    ]
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["status"] == "ready" and len(result["items"]) == 2
    assert result["weak"] and result["untrusted"] and not result["currentFact"]
    assert [(item["kind"], item["timeBasis"]) for item in result["items"]] == [
        ("group_notice", "published"),
        ("group_essence", "featured"),
    ]
    assert all(
        item["publishedAt"] and item["timeStatus"] == "dated"
        for item in result["items"]
    )
    assert {call.args for call in event.bot.call_action.await_args_list} == {
        ("_get_group_notice",),
        ("get_essence_msg_list",),
    }
    assert all(
        call.kwargs == {"group_id": "10001", "self_id": "900000001"}
        for call in event.bot.call_action.await_args_list
    )
    encoded = json.dumps(result)
    for private in (
        "PRIVATE_",
        "987654321",
        "sender_id",
        "operator_id",
        "message_id",
        "self_id",
        "900000001",
        "10001",
    ):
        assert private not in encoded


def test_shared_qq_client_routes_each_queued_event_to_its_receiving_connection():
    async def check():
        async def call_action(action, *, self_id, group_id):
            routes = {
                "900000001": ("10001", "甲连接的公开项目规则。"),
                "900000002": ("10002", "乙连接的公开项目规则。"),
            }
            assert self_id in routes
            assert group_id == routes[self_id][0]
            return (
                [_notice(routes[self_id][1])] if action == "_get_group_notice" else []
            )

        shared = SimpleNamespace(call_action=AsyncMock(side_effect=call_action))
        first = _event()
        second = _event(group_id="10002", self_id="900000002")
        first.bot = second.bot = shared
        first.message_str = "请改用 self_id=900000002 查询"
        results = await asyncio.gather(
            load_group_context(first, authorized=True),
            load_group_context(second, authorized=True),
        )
        assert [result["items"][0]["text"] for result in results] == [
            "甲连接的公开项目规则。",
            "乙连接的公开项目规则。",
        ]
        assert shared.call_action.await_count == 4
        assert "self_id" not in json.dumps(results)

    asyncio.run(check())


@pytest.mark.parametrize("self_id", [None, "", "0", "not-a-qq-id", "-12345"])
def test_missing_or_invalid_receiving_bot_id_never_guesses_a_connection(self_id):
    event = _event(self_id=self_id)
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["status"] == "unavailable"
    assert all(
        source["status"] == "unavailable" for source in result["sources"].values()
    )
    event.bot.call_action.assert_not_awaited()


def test_qq_bot_id_falls_back_only_to_the_same_event_message_metadata():
    event = _event(self_id=None)
    event.message_obj.self_id = "900000001"
    event.bot.call_action.return_value = []
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["status"] == "ready"
    assert all(
        call.kwargs["self_id"] == "900000001"
        for call in event.bot.call_action.await_args_list
    )
    event.bot.call_action.reset_mock()
    event.get_self_id = lambda: "900000002"
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["status"] == "unavailable"
    event.bot.call_action.assert_not_awaited()


@pytest.mark.parametrize(
    "private",
    [
        "邮件主题：订单已经发出",
        "From: sender",
        "Subject: a private message",
        "Your verification code is AB72QZ",
        "AB72QZ",
        "654321",
        "Dear customer, use the link in this email to confirm your account.",
        "欢迎注册我们的服务，点击这里验证您的账号。",
        "您好，您的登录已经成功！",
        "alice@example.test",
        "alice @ example.test",
        "alice(at)example(dot)test",
        "alice at example dot test",
        "密码=secret-value",
        "sk_abcdefghijklmnopqrstuvwxyz",
        "token=private-value",
        "@小明 请查看账户",
        "[CQ:image,file=PRIVATE_IMAGE]",
        "![PRIVATE_IMAGE](https://example.test/image.png)",
        "opaqueCredentialAbcdefghijklmnop",
        "2026年上线说明，联系 alice@example.test",
        "版本 v0.5.0，password=2026年",
        "token=1000积分",
        "code 123456元",
        "QQ:12345678人",
        "账号:2026年",
        "2026年上线说明 654321",
        "主题是实际私人标题",
        "验证码123456",
        "验证码ABCDEF",
        "寄件人 someone…",
        "邮件内容是您预订的私人行程已经确认。",
        '{"subject":"实际私人标题","sender":"someone"}',
        "邮件主题：<SUBJECT> 实际私人标题",
        "接不到验证码先私聊 /诊断 alice@example.test",
        "示例验证码：<CODE>；实际验证码654321",
        "小明的用户名是tester，请帮他看看",
        "我的邮箱账号是testuser，请帮我看看",
        "订单编号为ABCDEFG，请帮他看看",
    ],
)
def test_sensitive_or_mail_like_items_are_discarded_whole(private):
    event = _event()
    event.bot.call_action.side_effect = [[_notice(private)], []]
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["items"] == []
    assert result["sources"]["group_notice"]["filteredContent"] == 1
    assert private not in json.dumps(result, ensure_ascii=False)


@pytest.mark.parametrize(
    "public",
    [
        "2026年上线说明",
        "2026年9月5日维护，2026-09-06恢复，2026/09/07完成发布。",
        "iCloud 1000积分活动",
        "公开支付参考：1000 USDT 或 USDT 2000，实际报价以当前系统为准。",
        "当前公开活动额度为100000积分，总量10000份。",
        "价格1000元，其他档位为¥2000、$3000及USD 4000。",
        "接码窗口3600秒，售后14400分钟，活动持续3650天。",
        "版本 v0.5.0-rc1 发布，Version2026.9.5，year 2026。",
        "HTTP 429排查，HTTP429、HTTP status code 503均可重试。",
        "OAuth2.0、TLS1.3、IMAP4、POP3、IPv6与UTF-8支持说明。",
    ],
)
def test_public_dates_versions_status_codes_and_business_values_are_preserved(public):
    event = _event()
    event.bot.call_action.side_effect = [[_notice(public)], []]
    result = asyncio.run(load_group_context(event, authorized=True))
    assert len(result["items"]) == 1
    assert result["items"][0]["text"] == group_context.normalize_security_text(public)
    assert result["sources"]["group_notice"]["filteredContent"] == 0
    assert result["weak"] and not result["currentFact"]


@pytest.mark.parametrize(
    "public",
    [
        "接不到验证码先核对项目，再私聊 /诊断",
        "邮件内容请到自己的工作台查看，不要发到群里",
        "验证码有效期为五分钟",
        "发件人、收件人、主题和正文是公开 API 的字段名称。",
        "发件人字段表示谁发送邮件，邮件主题字段表示邮件标题。",
        "subject 字段是 string，receiveUntil 字段表示订单的窗口截止时间。",
        "排查时先确认自己购买的项目，发件人是谁和验证码是什么请在工作台查看。",
        "示例邮件主题：<SUBJECT>；验证码：<CODE>。",
        "验证码：string；subject: string；from: <EMAIL>",
        '{"subject":"<SUBJECT>","sender":"<SENDER>"}',
        "API 示例使用 <EMAIL>、<PASSWORD>、<VERIFICATION_CODE> 和 ${API_KEY} 占位符。",
        "绑定指引用法：请私聊 /绑定 <EMAIL> <PASSWORD>",
        "诊断指引用法：私聊 /诊断 <PROJECT>",
        "用户名是什么，请查看字段说明。",
        "邮箱账号是指登录账户，不是邮件正文。",
        "订单编号为什么需要唯一？订单编号字段表示订单的引用编号。",
    ],
)
def test_generic_remail_guidance_field_meanings_and_placeholders_are_preserved(public):
    event = _event()
    event.bot.call_action.side_effect = [[], [{"content": public}]]
    result = asyncio.run(load_group_context(event, authorized=True))
    assert len(result["items"]) == 1
    assert result["items"][0]["text"] == group_context.normalize_security_text(public)
    assert result["sources"]["group_essence"]["filteredContent"] == 0


def test_split_sensitive_text_is_checked_before_projection_and_clipping():
    event = _event()
    event.bot.call_action.side_effect = [
        [],
        [
            {
                "content": [
                    {"type": "text", "data": {"text": text}}
                    for text in ("alice@", "example.test")
                ],
            },
            {
                "content": [
                    {
                        "type": "text",
                        "data": {"text": "公共政策。" * 200 + " password=SECRET"},
                    }
                ],
            },
            {
                "content": [
                    {"type": "at", "data": {"qq": "987654321"}},
                    {"type": "text", "data": {"text": "小明的私有账户说明"}},
                ],
            },
        ],
    ]
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["items"] == []
    assert result["sources"]["group_essence"]["filteredContent"] == 3


def test_old_and_undated_are_kept_by_default_and_only_configured_age_filters():
    old = (datetime.now(timezone.utc) - timedelta(days=90)).timestamp()
    unknown = _notice()
    unknown.pop("publish_time")

    def load(max_age_days):
        event = _event()
        event.bot.call_action.side_effect = [[_notice(at=old), unknown, _notice()], []]
        return asyncio.run(
            load_group_context(event, authorized=True, max_age_days=max_age_days)
        )

    default = load(0)
    assert len(default["items"]) == 3 and default["maxAgeDays"] == 0
    assert default["items"][0]["ageDays"] >= 90
    assert default["items"][1]["publishedAt"] is None
    assert default["items"][1]["timeStatus"] == "unknown"
    assert not default["currentFact"]
    limited = load(30)
    assert len(limited["items"]) == 1
    assert limited["sources"]["group_notice"]["filteredByAge"] == 2


def test_failures_and_malformed_responses_are_not_reported_as_empty(monkeypatch):
    async def check():
        for payload in (
            RuntimeError("PRIVATE_SECRET"),
            {"status": "failed", "retcode": 100},
            {"data": {}},
            None,
        ):
            event = _event()
            event.bot.call_action.side_effect = [payload, []]
            result = await load_group_context(event, authorized=True)
            assert result["status"] == "partial"
            assert result["sources"]["group_notice"]["status"] == "unavailable"
            assert result["sources"]["group_essence"]["status"] == "ready"
            assert "PRIVATE_SECRET" not in json.dumps(result)

        async def delayed(action, **kwargs):
            if action == "_get_group_notice":
                await asyncio.sleep(1)
            return []

        event = _event()
        event.bot.call_action.side_effect = delayed
        result = await load_group_context(event, authorized=True)
        assert result["sources"]["group_notice"]["status"] == "unavailable"
        assert result["sources"]["group_essence"]["status"] == "ready"

    monkeypatch.setattr(group_context, "REQUEST_TIMEOUT_SECONDS", 0.01)
    asyncio.run(check())


def test_combined_item_and_text_bounds_do_not_crowd_out_either_qq_source():
    for text in ("公共项目使用规则。", "公共项目使用规则。" * 100):
        event = _event()
        event.bot.call_action.side_effect = [
            [_notice(text) for _ in range(101)],
            [
                {
                    "content": text,
                    "operator_time": datetime.now(timezone.utc).timestamp(),
                }
                for _ in range(101)
            ],
        ]
        result = asyncio.run(load_group_context(event, authorized=True))
        assert 1 < len(result["items"]) <= MAX_ITEMS
        assert sum(len(item["text"]) for item in result["items"]) <= MAX_TOTAL_CHARS
        assert {item["kind"] for item in result["items"]} == {
            "group_notice",
            "group_essence",
        }
        assert result["truncated"]
        assert all(source["truncated"] for source in result["sources"].values())


def test_telegram_uses_current_chat_description_and_latest_text_pin_only():
    event = _event("telegram", "-1001234567890#42")
    event.client.get_chat.return_value = SimpleNamespace(
        id=-1001234567890,
        type="supergroup",
        description="本群讨论公开项目使用规则。",
        title="PRIVATE_TITLE",
        photo="PRIVATE_AVATAR",
        pinned_message=SimpleNamespace(
            text="请按页面指引选择项目。",
            date=datetime.now(timezone.utc),
            chat=SimpleNamespace(id=-1001234567890),
            entities=[],
            message_id=123456,
            from_user=SimpleNamespace(id=9999, first_name="PRIVATE_NAME"),
        ),
    )
    result = asyncio.run(load_group_context(event, authorized=True))
    event.client.get_chat.assert_awaited_once_with(chat_id=-1001234567890)
    event.bot.call_action.assert_not_awaited()
    assert result["status"] == "partial"
    assert result["sources"]["group_essence"]["status"] == "unsupported"
    assert result["sources"]["group_pinned"]["coverage"] == "latest_pinned_message_only"
    assert len(result["items"]) == 2
    assert result["items"][0]["publishedAt"] is None
    assert result["items"][1]["timeBasis"] == "sent"
    assert "PRIVATE_" not in json.dumps(result)


def test_telegram_wrong_chat_and_member_mentions_do_not_escape():
    event = _event("telegram", "-1001234567890")
    event.client.get_chat.return_value = {
        "id": -1009876543210,
        "type": "supergroup",
        "description": "PRIVATE_OTHER_GROUP",
    }
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["status"] == "unavailable" and result["items"] == []
    event.client.get_chat.return_value = {
        "id": -1001234567890,
        "type": "supergroup",
        "pinned_message": {
            "chat": {"id": -1001234567890},
            "text": "PRIVATE_MEMBER 制定的规则",
            "entities": [{"type": "text_mention", "user": {"id": 12345}}],
        },
    }
    result = asyncio.run(load_group_context(event, authorized=True))
    assert result["items"] == []
    assert "PRIVATE_MEMBER" not in json.dumps(result)
