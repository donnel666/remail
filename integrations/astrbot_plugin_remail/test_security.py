import ast
import asyncio
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from time import monotonic
from types import SimpleNamespace
from typing import Any
from unittest.mock import AsyncMock

import pytest

from .feedback import sanitize_feedback_text
from .security import (
    adapter_channel,
    channel_system_keys,
    has_disallowed_url,
    keyword_blacklist_match,
    normalize_adapter_identity,
    redact_message_outline,
    redact_message_text,
    validated_base_url,
    websocket_url,
)


PLUGIN_DIR = Path(__file__).parent


def _load_push_renderer():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    body = []
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id.startswith("_PUSH_")
            for target in node.targets
        ):
            body.append(node)
        elif isinstance(
            node, (ast.FunctionDef, ast.AsyncFunctionDef)
        ) and node.name in {
            "_safe_push_value",
            "_render_push_text",
        }:
            body.append(node)
    namespace = {"Any": Any, "re": re}
    exec(compile(ast.Module(body=body, type_ignores=[]), "main.py", "exec"), namespace)
    return namespace["_render_push_text"]


def _load_announcement_formatter():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    methods = [
        node
        for node in main_class.body
        if isinstance(node, ast.FunctionDef)
        and node.name in {"_clip", "_format_announcements"}
    ]
    for method in methods:
        method.decorator_list = []
    namespace = {"Any": Any, "re": re}
    exec(
        compile(
            ast.fix_missing_locations(ast.Module(body=methods, type_ignores=[])),
            "main.py",
            "exec",
        ),
        namespace,
    )
    main = type("Main", (), {"_clip": staticmethod(namespace["_clip"])})
    namespace["_format_announcements"].__globals__["Main"] = main
    return namespace["_format_announcements"]


def _load_user_error_helpers():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    body = [
        node
        for node in tree.body
        if (
            isinstance(node, ast.Assign)
            and any(
                isinstance(target, ast.Name)
                and target.id in {"_CHINESE_TEXT", "_UNBOUND_TEXT"}
                for target in node.targets
            )
        )
        or (isinstance(node, ast.ClassDef) and node.name == "ReMailError")
        or (isinstance(node, ast.FunctionDef) and node.name == "_safe_user_error")
    ]
    namespace = {"re": re}
    exec(compile(ast.Module(body=body, type_ignores=[]), "main.py", "exec"), namespace)
    return namespace


def _load_profile_formatter():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    formatter = next(
        node
        for node in main_class.body
        if isinstance(node, ast.FunctionDef) and node.name == "_format_profile"
    )
    formatter.decorator_list = []
    helpers = _load_user_error_helpers()
    push = _load_push_renderer()
    namespace = {
        "Any": Any,
        "_safe_push_value": push.__globals__["_safe_push_value"],
        "_UNBOUND_TEXT": helpers["_UNBOUND_TEXT"],
    }
    exec(
        compile(ast.Module(body=[formatter], type_ignores=[]), "main.py", "exec"),
        namespace,
    )
    main = type(
        "Main",
        (),
        {
            "_result_text": staticmethod(
                lambda payload, fallback: payload.get("message") or fallback
            )
        },
    )
    namespace["_format_profile"].__globals__["Main"] = main
    return namespace["_format_profile"]


def _load_welcome_functions():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    constant_names = {
        "_REMAIL_COMMAND_PREFIX",
        "_REMAIL_INTENT_SYSTEM_PROMPT",
        "_REMAIL_ONLY_TEXT",
        "_REMAIL_INTENT_UNAVAILABLE_TEXT",
        "_REMAIL_PUBLIC_SERVICE_SYSTEM_PROMPT",
        "_REMAIL_REACT_SYSTEM_PROMPT",
        "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT",
        "_REMAIL_OUTPUT_POLISH_SYSTEM_PROMPT",
        "_PRODUCT_TYPE_ALIASES",
        "_PROJECT_PRICE_SUBJECT",
        "_MONEY_PAYMENT_QUERY",
        "_PROJECT_YUAN_PRICE",
        "_PRODUCT_LABELS",
        "_CATFK_URL",
        "_PAY_LDXP_URL",
        "_REDEMPTION_CHANNEL_BLOCK",
        "_REDEMPTION_CHANNEL_SENTENCE",
        "_PRICE_STOCK_QUERY",
        "_PRICE_STOCK_SENTENCE",
        "_GROUP_PROMO_SENTENCE",
        "_DIAGNOSIS_QUERY",
        "_ORDER_DIAGNOSIS_PROBLEM",
        "_DIAGNOSIS_NOT_VERIFIED_RESPONSE",
        "_DIAGNOSIS_FOLLOWUP_SENTENCE",
        "_UNSUPPORTED_SPECULATION_SENTENCE",
        "_FACTUAL_LITERAL",
        "_FACT_TOKEN",
        "_REQUIRED_FACT_TERMS",
        "_POSITIVE_STATE",
        "_NEGATIVE_STATE",
        "_GROUP_ORDER_VALUE",
        "_GROUP_OTP_VALUE",
        "_GROUP_ACCOUNT_VALUE",
        "_GROUP_CREDENTIAL_VALUE",
        "_GROUP_EMAIL",
        "_GROUP_PLATFORM_ID_VALUE",
        "_GROUP_MANAGEMENT_CONTACT_SENTENCE",
        "_GROUP_PRIVATE_MAIL_DETAIL",
        "_GROUP_PRIVATE_MAIL_RESPONSE",
        "_HARD_INTERNAL_EXPOSURE",
        "_BLACK_BOX_RESPONSE",
        "_PRIVACY_CONFIG_ERROR_TEXT",
        "_KB_CONTEXT_PREFIX",
        "_POLISH_INTERNAL_DETAIL",
        "_PUSH_EMAIL",
        "_PUSH_DATABASE_URL",
        "_PUSH_CREDENTIAL",
        "_PUSH_SYSTEM_KEY",
        "_PUSH_AUTHORIZATION",
    }
    constants = [
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id in constant_names
            for target in node.targets
        )
    ]
    helpers = [
        node
        for node in tree.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name
        in {
            "_safe_push_value",
            "_joined_group_members",
            "_qq_group_join_request",
            "_structured_strings",
            "_qq_moderation_text",
            "_remail_intent_decision",
            "_is_remail_command",
            "_intent_context_key",
            "_is_safe_group_extra_part",
            "_tool_status_is_hidden",
            "_positive_platform_id",
            "_configured_qq_management",
            "_normalize_product_types",
            "_project_price_view",
            "_enforce_project_price_units",
            "_fact_token",
            "_protect_factual_literals",
            "_restore_factual_literals",
            "_polish_preserves_facts",
            "_enforce_group_privacy",
            "_enforce_black_box",
            "_needs_order_diagnosis",
            "_enforce_diagnosis_fact",
            "_replace_response_text",
            "_sync_final_agent_message",
            "_enforce_redemption_channel_priority",
            "_enforce_answer_scope",
            "_mentioned_qq_ids",
            "_mentions_bot",
        }
    ]
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    handlers = [
        node
        for node in main_class.body
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name
        in {
            "welcome_new_members",
            "auto_approve_qq_join_request",
            "moderate_qq_group_message",
            "handoff_group_manager_mentions",
            "classify_mentioned_group_question",
            "prepare_remail_llm_response",
            "enforce_redemption_channel_priority",
            "sync_polished_response_history",
        }
    ]
    for handler in handlers:
        handler.decorator_list = []

    class ReMailError(RuntimeError):
        pass

    namespace = {
        "AstrMessageEvent": object,
        "Any": Any,
        "At": lambda **values: ("at", values),
        "has_disallowed_url": has_disallowed_url,
        "json": json,
        "keyword_blacklist_match": keyword_blacklist_match,
        "LLMResponse": object,
        "MessageChain": lambda items: items,
        "MessageType": SimpleNamespace(FRIEND_MESSAGE="friend", GROUP_MESSAGE="group"),
        "normalize_adapter_identity": normalize_adapter_identity,
        "Plain": lambda text: ("plain", text),
        "re": re,
        "ReMailError": ReMailError,
        "redact_message_text": redact_message_text,
        "_safe_user_error": lambda _exc: "当前会话未获授权。",
        "sanitize_feedback_text": sanitize_feedback_text,
        "logger": SimpleNamespace(warning=lambda *_args: None),
        "monotonic": monotonic,
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*constants, *helpers, *handlers], type_ignores=[])
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )
    return namespace, ReMailError


def test_redact_message_outline() -> None:
    assert redact_message_outline("/绑定 user@example.com secret") == "/绑定 [REDACTED]"
    assert (
        redact_message_outline(" /bind user@example.com secret") == "/绑定 [REDACTED]"
    )
    assert redact_message_outline("!绑定 user@example.com secret") == "/绑定 [REDACTED]"
    assert (
        redact_message_outline("[At:123] /绑定 user@example.com secret")
        == "/绑定 [REDACTED]"
    )
    assert (
        redact_message_outline("[引用消息(foo: x)] /bind@mybot user@example.com secret")
        == "/绑定 [REDACTED]"
    )
    assert (
        redact_message_outline("bot /bind user@example.com secret")
        == "/绑定 [REDACTED]"
    )
    assert redact_message_outline("怎么绑定账号") == "怎么绑定账号"
    assert redact_message_text("绑定 user@example.com secret") == "绑定 [REDACTED]"
    assert redact_message_text("bind user@example.com secret") == "bind [REDACTED]"
    assert redact_message_text("/绑定 user@example.com secret") == "/绑定 [REDACTED]"
    assert (
        redact_message_outline("/诊断 order@example.com 一直没收到")
        == "/诊断 [REDACTED]"
    )
    assert redact_message_text("诊断 order@example.com 一直没收到") == "诊断 [REDACTED]"


def test_group_moderation_keyword_and_url_rules() -> None:
    assert keyword_blacklist_match("ＳＰＡ\u200bＭ message", ["spam"])
    assert keyword_blacklist_match("这是黑名单内容", ["黑名单"])
    assert not keyword_blacklist_match("ordinary message", ["", 123, "spam"])

    allowed = ["example.com", "例子.公司"]
    assert not has_disallowed_url("https://example.com/path", allowed)
    assert not has_disallowed_url("https://docs.example.com/path", allowed)
    assert not has_disallowed_url("https://例子.公司/帮助", allowed)
    assert not has_disallowed_url("联系 user@example.com", [])
    assert has_disallowed_url("https://evil.example/path", allowed)
    assert has_disallowed_url("https://example.com.evil.test/path", allowed)
    assert has_disallowed_url("https://example.com@evil.test/path", allowed)
    assert has_disallowed_url("www.evil.test/path", allowed)
    assert has_disallowed_url("https://example.com", [])
    assert not has_disallowed_url("malformed http://[", allowed)


def test_remail_intent_decision_requires_one_unambiguous_label() -> None:
    functions, _ = _load_welcome_functions()
    classify = functions["_remail_intent_decision"]
    for value in ("REMAIL", "  remail\n", "Classification: REMAIL"):
        assert classify(value) is True
    assert classify("IGNORE") is False
    for value in ("REMAIL or IGNORE", "remailing", "", None):
        assert classify(value) is None

    is_command = functions["_is_remail_command"]
    for value in ("/项目 github", "!help", "！诊断 foo bar", "/绑定状态"):
        assert is_command(value)
    for value in ("/weather", "!今天天气", "普通聊天", ""):
        assert not is_command(value)


def test_redemption_channel_priority_is_enforced_before_response() -> None:
    functions, _ = _load_welcome_functions()
    enforce = functions["_enforce_redemption_channel_priority"]
    enforce_scope = functions["_enforce_answer_scope"]
    original = (
        "推荐去 https://pay.ldxp.cn/shop/aishop6 购买兑换码。\n\n"
        "同时加入 ReMail 官方群反馈更准：\n"
        "TG：t.me/remail6\nQQ：529642597\n\n"
        "群里还能看到最新项目和库存。"
    )
    result = enforce(original)
    assert result.index("https://catfk.com/shop/aishop6") < result.index(
        "https://pay.ldxp.cn/shop/aishop6"
    )
    assert "手续费更低" in result
    assert "推荐去 https://pay.ldxp.cn" not in result
    assert "TG：t.me/remail6" in result
    assert "QQ：529642597" in result
    assert "回到 ReMail 完成兑换" in result
    assert enforce(result) == result
    assert enforce("普通 ReMail 使用说明") == "普通 ReMail 使用说明"

    scoped = enforce_scope("怎么买积分兑换码？", result)
    assert "t.me/remail6" not in scoped
    assert "529642597" not in scoped
    assert "群里" not in scoped

    response = SimpleNamespace(role="assistant", completion_text=original)
    response_event = SimpleNamespace(
        message_str="怎么买积分兑换码？",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
    )

    async def polish_echo(**kwargs):
        payload = json.loads(kwargs["prompt"])
        return SimpleNamespace(
            role="assistant", completion_text=f"嗯，{payload['factualDraft']}"
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=polish_echo),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), response_event, response
        )
    )
    assert response.completion_text == scoped
    context.get_current_chat_provider_id.assert_awaited_once_with(
        response_event.unified_msg_origin
    )
    polish_call = context.llm_generate.await_args.kwargs
    assert polish_call["chat_provider_id"] == "provider"
    assert polish_call["tools"] is None
    assert polish_call["contexts"] is None
    assert "最终答复编辑器" in polish_call["system_prompt"]
    polish_input = json.loads(polish_call["prompt"])
    assert polish_input["userQuestion"] == "怎么买积分兑换码？"
    assert polish_input["factualDraft"] != original
    assert "[[REMAIL_FACT_" in polish_input["factualDraft"]
    assert "https://pay.ldxp.cn" not in polish_input["factualDraft"]

    run_context = SimpleNamespace(
        messages=[
            SimpleNamespace(role="user", content="question", tool_calls=None),
            SimpleNamespace(role="assistant", content=original, tool_calls=None),
        ]
    )
    asyncio.run(
        functions["sync_polished_response_history"](
            object(), object(), run_context, response
        )
    )
    assert run_context.messages[-1].content == scoped

    failed_response = SimpleNamespace(role="assistant", completion_text=original)
    failed_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=RuntimeError("provider unavailable")),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=failed_context),
            response_event,
            failed_response,
        )
    )
    assert failed_response.completion_text == scoped

    rejected_response = SimpleNamespace(role="assistant", completion_text=original)
    rejected_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="err", completion_text="provider internal error"
            )
        ),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=rejected_context),
            response_event,
            rejected_response,
        )
    )
    assert rejected_response.completion_text == scoped

    extras = {}
    event = SimpleNamespace(
        is_at_or_wake_command=True,
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    asyncio.run(functions["prepare_remail_llm_response"](object(), event))
    assert extras == {"enable_streaming": False}

    private_extras = {}
    private_event = SimpleNamespace(
        is_at_or_wake_command=False,
        get_message_type=lambda: "friend",
        set_extra=lambda key, value: private_extras.__setitem__(key, value),
    )
    asyncio.run(functions["prepare_remail_llm_response"](object(), private_event))
    assert private_extras == {"enable_streaming": False}

    leaked_response = SimpleNamespace(
        role="assistant",
        completion_text=(
            "邮件主题是 Genspark account email verification，发件人来自 Microsoft，"
            "验证码是 768071。"
        ),
    )
    leaked_event = SimpleNamespace(
        message_str="帮我看看这封邮件",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
    )
    blocked_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(),
        llm_generate=AsyncMock(),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=blocked_context), leaked_event, leaked_response
        )
    )
    assert leaked_response.completion_text == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    blocked_context.get_current_chat_provider_id.assert_not_awaited()
    blocked_context.llm_generate.assert_not_awaited()

    unverified_response = SimpleNamespace(
        role="assistant", completion_text="这是 iCloud 项目，换 Outlook 邮箱即可。"
    )
    unverified_event = SimpleNamespace(
        message_str="peptide_tech.3k@icloud.com 接不到码",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
    )
    unverified_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(),
        llm_generate=AsyncMock(),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=unverified_context),
            unverified_event,
            unverified_response,
        )
    )
    assert (
        unverified_response.completion_text
        == functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
    )
    unverified_context.llm_generate.assert_not_awaited()

    locked_response = SimpleNamespace(
        role="assistant",
        completion_text=(
            "该订单对应 ChatGPT 项目，但真正应该使用 Genspark 项目，验证码是 768071。"
        ),
    )
    locked_event = SimpleNamespace(
        message_str="peptide_tech.3k@icloud.com 接不到码",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: (
            {"projectName": "ChatGPT", "message": "暂未发现明确异常。"}
            if key == "_remail_code_diagnosis_fact"
            else default
        ),
    )
    locked_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(), llm_generate=AsyncMock()
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=locked_context), locked_event, locked_response
        )
    )
    assert locked_response.completion_text == (
        "该订单对应的是 ChatGPT 项目。 暂未发现明确异常。 "
        "请核对 ChatGPT 项目是否与目标业务一致。"
    )
    assert "Genspark" not in locked_response.completion_text
    assert "768071" not in locked_response.completion_text
    assert locked_response.result_chain == [("plain", locked_response.completion_text)]
    locked_context.llm_generate.assert_not_awaited()


def test_answer_scope_removes_group_promotions_and_unasked_price_stock() -> None:
    functions, _ = _load_welcome_functions()
    enforce = functions["_enforce_answer_scope"]
    original = (
        "购买邮箱是长效邮箱，可持续收件和接码；标准质保 24 小时，质保不是使用期限。\n\n"
        "当前 iCloud 邮箱价格 0.03 一个，库存约 1.4K 个。\n\n"
        "Gmail 注册风控严格，不支持通过 ReMail 完成。\n"
        "iCloud 贵是因为需求大、资源非常稀缺，抢着买是正常现象。\n\n"
        "TG群：t.me/remail6\nQQ群：529642597（加群可参与抽奖）\n\n"
        "需要我帮你查具体订单吗？请发送订单邮箱或截图。"
    )
    result = enforce("iCloud 购买邮箱有效期多久？", original)
    assert "长效邮箱" in result
    assert "24 小时" in result
    for forbidden in (
        "0.03",
        "库存",
        "t.me/remail6",
        "529642597",
        "TG群",
        "QQ群",
        "加群",
        "抽奖",
        "截图",
        "订单邮箱",
        "风控严格",
        "需求大",
        "资源非常稀缺",
        "正常现象",
    ):
        assert forbidden not in result
    assert enforce("iCloud 购买邮箱有效期多久？", result) == result

    price_result = enforce("iCloud 现在价格和库存是多少？", original)
    assert "0.03" in price_result
    assert "库存" in price_result
    assert "t.me/remail6" not in price_result
    assert "529642597" not in price_result

    future_result = enforce("iCloud 什么时候降价或补货？", original)
    assert "0.03" in future_result
    assert "库存" in future_result
    assert "需求大" not in future_result

    api_result = enforce(
        "API 如何批量下单？", "调用成功后会返回 10 个邮箱地址。然后逐个处理。"
    )
    assert api_result == "调用成功后会返回 10 个邮箱地址。然后逐个处理。"
    uncertainty = "目前没有公开说明是否因为资源稀缺。当前没有已公布的补货时间。"
    assert enforce("为什么还没补货？", uncertainty) == uncertainty


def test_polish_fact_guard_and_group_privacy_are_deterministic() -> None:
    functions, _ = _load_welcome_functions()
    protect = functions["_protect_factual_literals"]
    restore = functions["_restore_factual_literals"]
    preserves = functions["_polish_preserves_facts"]
    privacy = functions["_enforce_group_privacy"]

    draft = "当前不可用，价格 0.03 元。调用 `POST /v1/open/orders`，等待 10 分钟。"
    protected, literals = protect(draft)
    assert "0.03" not in protected
    assert "/v1/open/orders" not in protected
    candidate = restore(f"先说清楚：{protected}", literals)
    assert preserves(draft, candidate)
    assert not preserves(draft, candidate.replace("不可用", "可用"))
    assert not preserves(draft, f"{candidate} 新价格 0.06 元。")
    assert not preserves(draft, candidate.replace("/v1/open/orders", "/v1/orders"))
    assert not preserves("Gmail 当前不可用。", "iCloud 当前不可用。")
    assert not preserves("当前没有查询到可用的 Gmail 项目。", "Gmail 项目当前可用。")
    assert restore("标记被删除了", literals) == ""

    exposed = (
        "联系邮箱：user@example.com\n订单号 ORD_12345\n验证码：654321\n"
        "API Key: sk_example_secret"
    )
    hidden = privacy(exposed)
    for secret in ("user@example.com", "ORD_12345", "654321", "sk_example_secret"):
        assert secret not in hidden
    assert "[邮箱已隐藏]" in hidden
    assert "[订单信息已隐藏]" in hidden
    assert "[验证码已隐藏]" in hidden
    placeholders = privacy(
        "Authorization: Bearer <API_KEY>\napi_key: ${API_KEY}\npassword: <PASSWORD>"
    )
    assert "Bearer <API_KEY>" in placeholders
    assert "${API_KEY}" in placeholders
    assert "<PASSWORD>" in placeholders

    management = functions["_enforce_answer_scope"](
        "这个问题怎么办？",
        "你可以私聊群主 1362626064 继续跟进。管理员QQ号：9845248。",
    )
    assert "私聊群主" not in management
    assert "1362626064" not in management
    assert "9845248" not in privacy("管理员QQ号：9845248")

    leaked_mail = (
        "你好，邮件已经收到。主题是 Genspark account email verification，"
        "发件人来自 Microsoft on behalf of Genspark，验证码是 768071。"
        "邮箱 peptide_tech.3k@icloud.com。"
    )
    assert privacy(leaked_mail) == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    for secret in (
        "Genspark account email verification",
        "Microsoft on behalf of Genspark",
        "768071",
        "peptide_tech.3k@icloud.com",
    ):
        assert secret not in privacy(leaked_mail)

    black_box = functions["_enforce_black_box"]
    assert (
        black_box("数据库显示上游返回了供应商字段。")
        == functions["_BLACK_BOX_RESPONSE"]
    )
    assert black_box("相关内部实现不对外提供。") == functions["_BLACK_BOX_RESPONSE"]
    assert (
        black_box("内部实现不对外，但数据库和上游信息如下。")
        == functions["_BLACK_BOX_RESPONSE"]
    )
    assert (
        privacy("邮件标题叫 Secret，发送方 Example，代码 ABC123。")
        == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    )

    diagnosis = functions["_enforce_diagnosis_fact"]
    fact = {"projectName": "ChatGPT", "message": "暂未发现明确异常。"}
    corrected = diagnosis("这是 iCloud 项目，截图属于 Genspark 项目。", fact)
    assert "ChatGPT 项目" in corrected
    assert "iCloud 项目" not in corrected
    assert "Genspark 项目" not in corrected
    assert (
        diagnosis("该订单对应 ChatGPT 项目，但真正应该使用 Genspark 项目。", fact)
        == corrected
    )


def test_project_price_tool_supports_multiple_types_and_uses_point_units() -> None:
    functions, _ = _load_welcome_functions()
    normalize = functions["_normalize_product_types"]
    project_view = functions["_project_price_view"]
    enforce_units = functions["_enforce_project_price_units"]
    requested = normalize("iCloud / 微软邮箱 / 域名邮箱")
    assert set(requested) == {"icloud", "microsoft", "domain"}

    payload = {
        "total": 2,
        "items": [
            {
                "id": 7,
                "name": "OpenAI",
                "targetPlatform": "openai",
                "products": [
                    {
                        "type": "icloud",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": True,
                        "codePrice": "12",
                        "purchasePrice": "15",
                        "effectiveCodePrice": "10",
                        "effectivePurchasePrice": "13",
                        "publicAvailable": 43000,
                    },
                    {
                        "type": "microsoft",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": False,
                        "effectiveCodePrice": "8",
                        "publicAvailable": 1000,
                    },
                ],
            },
            {
                "id": 8,
                "name": "Cloudflare",
                "targetPlatform": "cloudflare",
                "products": [
                    {
                        "type": "domain",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": True,
                        "effectiveCodePrice": "4",
                        "effectivePurchasePrice": "9",
                        "publicAvailable": 2000,
                    }
                ],
            },
        ],
    }
    view = project_view(payload, requested)
    assert view["unit"] == "ReMail积分"
    assert view["matched"] is True
    assert {item["productType"] for item in view["prices"]} == {
        "icloud",
        "microsoft",
        "domain",
    }
    icloud = next(item for item in view["prices"] if item["productType"] == "icloud")
    assert icloud["codePricePoints"] == "10"
    assert icloud["purchasePricePoints"] == "13"
    assert (
        enforce_units(
            "iCloud 邮箱目前价格多少？", "iCloud 接码价格 10元/个，购买价格 13 元。"
        )
        == "iCloud 接码价格 10 积分，购买价格 13 积分。"
    )
    assert (
        enforce_units("充值 100 元怎么买兑换码？", "支付 100 元。") == "支付 100 元。"
    )

    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    tool = next(
        node
        for node in main_class.body
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_project_prices"
    )
    tool.decorator_list = []
    namespace = {
        "Any": Any,
        "AstrMessageEvent": object,
        "json": json,
        "_normalize_product_types": normalize,
        "_project_price_view": project_view,
    }
    exec(
        compile(ast.Module(body=[tool], type_ignores=[]), "main.py", "exec"), namespace
    )
    plugin = SimpleNamespace(_request=AsyncMock(return_value=payload))
    result = asyncio.run(
        namespace["remail_project_prices"](plugin, object(), "icloud,microsoft,domain")
    )
    assert json.loads(result)["unit"] == "ReMail积分"
    assert plugin._request.await_args.kwargs["params"] == {
        "scope": "visible",
        "limit": 100,
    }
    doc = ast.get_docstring(tool) or ""
    assert "任何实时价格问题都必须调用" in doc
    assert "codePricePoints" in doc


def test_runtime_system_prompt_documents_every_remail_tool_contract() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    assignment = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    prompt = ast.literal_eval(assignment.value)
    tools = (
        "remail_project_prices",
        "remail_projects",
        "remail_project_inventory",
        "remail_faqs",
        "remail_announcements",
        "remail_api_documentation",
        "remail_code_diagnosis",
        "remail_order_rankings",
        "remail_latest_ranking_rewards",
        "remail_binding_status",
        "remail_record_unresolved",
    )
    for index, tool in enumerate(tools, start=1):
        start = prompt.index(f"【{index}. {tool}】")
        end = (
            prompt.index(f"【{index + 1}. {tools[index]}】", start)
            if index < len(tools)
            else prompt.index("remail_projects 返回空列表", start)
        )
        section = prompt[start:end]
        for required in ("用途：", "参数：", "返回", "典型场景："):
            assert required in section, f"{tool} missing {required}"


def test_configured_qq_management_and_telegram_bot_mentions() -> None:
    functions, _ = _load_welcome_functions()
    configured = functions["_configured_qq_management"]
    assert configured(
        {
            "qq_group_owner_id": "9845248",
            "qq_group_admin_ids": ["12345678", 87654321, "9845248", "bad", "0"],
        }
    ) == ("9845248", {"12345678", "87654321"})
    assert configured({"qq_group_owner_id": "not-a-qq"}) == ("", set())
    group_config = {
        "qq_group_owner_id": "9845248",
        "qq_group_admin_ids": ["12345678"],
        "qq_group_management": [
            "529642597|11111111|22222222,33333333",
            "650384960|44444444|55555555",
        ],
    }
    assert configured(group_config, "529642597") == (
        "11111111",
        {"22222222", "33333333"},
    )
    assert configured(group_config, "999999999") == (
        "9845248",
        {"12345678"},
    )

    mention = SimpleNamespace(qq="@HongYeBot", name="HongYeBot")
    telegram_event = SimpleNamespace(
        get_platform_name=lambda: "telegram",
        get_self_id=lambda: "hongyebot",
        get_messages=lambda: [mention],
        message_obj=SimpleNamespace(raw_message=None),
    )
    assert functions["_mentions_bot"](telegram_event)
    assert functions["_normalize_product_types"]("Gmail 变种") == ("gmail_variant",)


def test_adapter_identity_uses_real_qq_and_telegram_ids() -> None:
    assert normalize_adapter_identity("aiocqhttp", "123456789", "987654321") == (
        "123456789",
        "987654321",
    )
    with pytest.raises(ValueError, match="真实 QQ 号"):
        normalize_adapter_identity("aiocqhttp", "openid-user", "987654321")
    with pytest.raises(ValueError, match="真实QQ群号"):
        normalize_adapter_identity("aiocqhttp", "123456789", "group-openid")

    assert normalize_adapter_identity("telegram", "123456789", "-1001234567890#42") == (
        "123456789",
        "-1001234567890",
    )
    assert normalize_adapter_identity("telegram", "123456789", "") == (
        "123456789",
        "",
    )
    with pytest.raises(ValueError, match="用户 ID"):
        normalize_adapter_identity("telegram", "telegram-user", "-1001234567890")


def test_channel_keys_are_optional_but_cannot_be_shared() -> None:
    assert adapter_channel("aiocqhttp") == "qq"
    assert adapter_channel("telegram") == "telegram"
    with pytest.raises(ValueError, match="没有配置"):
        adapter_channel("qq_official")
    assert channel_system_keys(" sk_qq ", "") == {"qq": "sk_qq"}
    assert channel_system_keys("", " sk_tg ") == {"telegram": "sk_tg"}
    assert channel_system_keys("sk_qq", "sk_tg") == {
        "qq": "sk_qq",
        "telegram": "sk_tg",
    }
    with pytest.raises(ValueError, match="不能使用同一把"):
        channel_system_keys("sk_shared", "sk_shared")


def test_command_errors_are_mapped_to_safe_chinese() -> None:
    helpers = _load_user_error_helpers()
    error = helpers["ReMailError"]
    safe = helpers["_safe_user_error"]

    assert helpers["_UNBOUND_TEXT"] == (
        "当前账号尚未绑定 ReMail。\n"
        "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
    )
    assert safe(error(401, "Authentication is required.")) == "当前会话未获授权。"
    assert str(error(503, "ReMail WebSocket response lost")) == "ReMail 请求失败。"
    assert safe(error(429, "rate limit exceeded")) == "请求过于频繁，请稍后再试。"
    assert (
        safe(error(503, "ReMail WebSocket response lost"))
        == "服务暂时不可用，请稍后重试。"
    )
    assert safe(error(422, "Account or password is incorrect."), binding=True) == (
        "ReMail 账号或密码错误。"
    )
    assert safe(error(422, "账号或密码不正确。"), binding=True) == "账号或密码不正确。"


def test_llm_request_requires_remail_event_authorization() -> None:
    runtime, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "authorize_llm"
    )
    handler.decorator_list = []
    billing_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_PUBLIC_BILLING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    service_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_PUBLIC_SERVICE_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    react_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_REACT_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    routing_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    command_pattern = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_COMMAND_PREFIX"
            for target in node.targets
        )
    )
    is_command = next(
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef) and node.name == "_is_remail_command"
    )
    helpers = _load_user_error_helpers()

    class TextPart:
        def __init__(self, text: str) -> None:
            self.text = text

    namespace = {
        "AstrMessageEvent": object,
        "Any": Any,
        "ProviderRequest": object,
        "MessageChain": lambda items: items,
        "MessageType": SimpleNamespace(GROUP_MESSAGE="group"),
        "Plain": lambda text: text,
        "ReMailError": helpers["ReMailError"],
        "TextPart": TextPart,
        "_safe_user_error": helpers["_safe_user_error"],
        "_tool_status_is_hidden": runtime["_tool_status_is_hidden"],
        "_is_safe_group_extra_part": runtime["_is_safe_group_extra_part"],
        "_PRIVACY_CONFIG_ERROR_TEXT": runtime["_PRIVACY_CONFIG_ERROR_TEXT"],
        "re": re,
    }
    exec(
        compile(
            ast.Module(
                body=[
                    billing_prompt,
                    service_prompt,
                    react_prompt,
                    routing_prompt,
                    command_pattern,
                    is_command,
                    handler,
                ],
                type_ignores=[],
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )

    class Plugin:
        async def _authorize_event(self, _event):
            raise helpers["ReMailError"](401, "Authentication is required.")

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    event = SimpleNamespace(
        get_extra=lambda _key, default="": default,
        send=send,
        stop_event=lambda: stopped.append(True),
    )
    request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](Plugin(), event, request))
    assert sent == [["当前会话未获授权。"]]
    assert stopped == [True]
    assert not request.extra_user_content_parts
    assert not hasattr(request, "system_prompt")

    authorized = SimpleNamespace(_authorize_event=AsyncMock())
    handoff_event = SimpleNamespace(
        get_extra=lambda key, default="": (
            "群主" if key == "_remail_admin_handoff_role" else default
        ),
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized handoff must continue"),
    )
    handoff_request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](authorized, handoff_event, handoff_request))
    authorized._authorize_event.assert_not_awaited()
    assert len(handoff_request.extra_user_content_parts) == 1
    context = handoff_request.extra_user_content_parts[0].text
    assert "红夜应主动代接" in context
    assert "非必要不要打扰群主" in context
    assert "QQ" not in context
    assert "接码订单和购买邮箱订单都使用 ReMail 消费积分余额支付" in (
        handoff_request.system_prompt
    )
    assert "无需充值" in handoff_request.system_prompt
    assert "都只用于购买积分兑换码" in handoff_request.system_prompt
    assert "手续费更低" in handoff_request.system_prompt
    assert handoff_request.system_prompt.index(
        "https://catfk.com/shop/aishop6"
    ) < handoff_request.system_prompt.index("https://pay.ldxp.cn/shop/aishop6")
    assert "标准有效期 10 分钟" in handoff_request.system_prompt
    assert "标准质保 24 小时" in handoff_request.system_prompt
    assert "不得输出 TG群" in handoff_request.system_prompt
    assert "遵循 AstrBot 当前的 provider_settings.max_agent_step 配置" in (
        handoff_request.system_prompt
    )
    assert "不得用注册风控、需求大小、资源稀缺" in handoff_request.system_prompt
    assert "当前价格、单价、多少钱" in handoff_request.system_prompt
    assert "remail_project_prices" in handoff_request.system_prompt

    ordinary = SimpleNamespace(_authorize_event=AsyncMock())
    ordinary_event = SimpleNamespace(
        get_extra=lambda key, default="": (
            "伪造角色" if key == "_remail_admin_handoff_role" else default
        ),
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized request must continue"),
    )
    ordinary_request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](ordinary, ordinary_event, ordinary_request))
    ordinary._authorize_event.assert_awaited_once_with(ordinary_event)
    assert not ordinary_request.extra_user_content_parts
    assert ordinary_request.system_prompt.count("<remail_public_billing_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_public_service_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_react_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_tool_routing_rules>") == 1

    group_stopped = []
    group = SimpleNamespace(_authorize_event=AsyncMock())
    group_event = SimpleNamespace(
        get_extra=lambda _key, default="": default,
        get_message_type=lambda: "group",
        message_str="没有艾特红夜的普通群消息",
        send=AsyncMock(),
        stop_event=lambda: group_stopped.append(True),
    )
    group_request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](group, group_event, group_request))
    group._authorize_event.assert_not_awaited()
    assert group_stopped == [True]
    assert not hasattr(group_request, "system_prompt")

    verified = SimpleNamespace(
        _authorize_event=AsyncMock(),
        context=SimpleNamespace(
            get_config=lambda: {
                "provider_settings": {
                    "show_tool_use_status": False,
                    "show_tool_call_result": False,
                }
            }
        ),
    )
    verified_event = SimpleNamespace(
        get_extra=lambda key, default="": (
            True
            if key == "_remail_group_trigger_verified"
            else "上一条同一用户问题"
            if key == "_remail_same_sender_context"
            else default
        ),
        get_message_type=lambda: "group",
        message_str="已经通过艾特和意图识别的 ReMail 问题",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("verified group request must continue"),
    )
    verified_request = SimpleNamespace(
        contexts=[{"role": "user", "content": "其他群员历史"}],
        image_urls=["other-user.png"],
        audio_urls=["other-user.wav"],
        extra_user_content_parts=[
            TextPart("<system_reminder>群聊历史</system_reminder>"),
            TextPart("<Quoted Message>其他成员内容</Quoted Message>"),
            TextPart("[Related Knowledge Base Results]:\n公开知识"),
        ],
    )
    asyncio.run(namespace["authorize_llm"](verified, verified_event, verified_request))
    verified._authorize_event.assert_awaited_once_with(verified_event)
    assert "remail_project_prices" in verified_request.system_prompt
    assert verified_request.contexts == []
    assert verified_request.image_urls == []
    assert verified_request.audio_urls == []
    extra_texts = [part.text for part in verified_request.extra_user_content_parts]
    assert not any("其他群员历史" in text for text in extra_texts)
    assert not any("其他成员内容" in text for text in extra_texts)
    assert any("公开知识" in text for text in extra_texts)
    assert any("上一条同一用户问题" in text for text in extra_texts)

    unsafe = SimpleNamespace(
        _authorize_event=AsyncMock(),
        context=SimpleNamespace(
            get_config=lambda: {"provider_settings": {"show_tool_use_status": True}}
        ),
    )
    unsafe_stopped = []
    unsafe_event = SimpleNamespace(
        get_extra=lambda _key, default="": default,
        get_message_type=lambda: "friend",
        send=AsyncMock(),
        stop_event=lambda: unsafe_stopped.append(True),
    )
    unsafe_request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](unsafe, unsafe_event, unsafe_request))
    unsafe_event.send.assert_awaited_once_with([runtime["_PRIVACY_CONFIG_ERROR_TEXT"]])
    assert unsafe_stopped == [True]


def test_personal_info_formatter_handles_binding_states() -> None:
    render = _load_profile_formatter()
    assert render({"bound": False}) == (
        "当前账号尚未绑定 ReMail。\n"
        "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
    )
    assert render(
        {
            "bound": True,
            "available": True,
            "balance": "12.50",
            "totalRecharged": "200.00",
            "groupName": "VIP 1",
            "roleDisplay": "普通用户",
            "nextGroupName": "VIP 2",
            "upgradeRemaining": "300.00",
        }
    ) == (
        "ReMail 个人信息\n"
        "余额：12.50 积分\n"
        "账号分组：VIP 1\n"
        "角色：普通用户\n"
        "累计充值：200.00 积分\n"
        "升级进度：距离 VIP 2 还差 300.00 积分"
    )
    assert render({"bound": True, "message": "账号不可用"}) == "账号不可用"


def test_commands_and_llm_tools_cannot_accept_platform_identity() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    forbidden_parts = {"qq", "subject", "sender", "user_id", "platform", "group"}
    for node in ast.walk(tree):
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        decorators = [
            decorator.func if isinstance(decorator, ast.Call) else decorator
            for decorator in node.decorator_list
        ]
        if not any(
            isinstance(decorator, ast.Attribute)
            and decorator.attr in {"command", "llm_tool"}
            for decorator in decorators
        ):
            continue
        arguments = {
            argument.arg for argument in [*node.args.args, *node.args.kwonlyargs]
        } - {"self", "event"}
        forbidden = {
            argument
            for argument in arguments
            if any(part in argument.casefold() for part in forbidden_parts)
        }
        assert not forbidden, (node.name, forbidden)


def test_help_is_chinese_authorized_and_stops_builtin_help() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    help_assignment = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_HELP_TEXT"
            for target in node.targets
        )
    )
    help_text = ast.literal_eval(help_assignment.value)
    for command in (
        "/help",
        "/公告",
        "/常见问题",
        "/接口文档",
        "/项目",
        "/库存",
        "/排行榜",
        "/排行榜奖励",
        "/绑定",
        "/绑定状态",
        "/个人信息",
        "/解绑",
        "/诊断",
        "/反馈",
        "/建议",
    ):
        assert command in help_text
    for internal in ("WebSocket", "System Key", "数据库", "上游", "供应商"):
        assert internal not in help_text

    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "remail_help"
    )
    decorator = ast.unparse(handler.decorator_list[0])
    assert "command('help'" in decorator
    assert "'帮助'" in decorator
    assert "'remail帮助'" in decorator
    assert "priority=sys.maxsize" in decorator
    source = ast.unparse(handler)
    assert "_private_target" in source
    assert source.index("_authorize_event") < source.index("context.send_message")
    assert "event.send" not in source
    assert "event.stop_event()" in source
    assert "exc.message" not in source
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom)) for node in ast.walk(handler)
    )

    handler.decorator_list = []
    namespace = {
        "AstrMessageEvent": object,
        "MessageChain": lambda items: items,
        "Plain": lambda text: text,
        "ReMailError": RuntimeError,
        "logger": SimpleNamespace(warning=lambda *_args: None),
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[help_assignment, handler], type_ignores=[])
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )
    sent = []

    class Context:
        async def send_message(self, target, message):
            sent.append((target, message))
            return True

    class Plugin:
        context = Context()

        @staticmethod
        def _private_target(_event):
            return "qq-main:FriendMessage:123456789"

        async def _authorize_event(self, _event):
            return None

    stopped = []
    event = SimpleNamespace(
        stop_event=lambda: stopped.append(True),
    )
    asyncio.run(namespace["remail_help"](Plugin(), event))
    assert sent == [("qq-main:FriendMessage:123456789", [help_text])]
    assert stopped == [True]


def test_event_key_uses_channel_key_and_binding_stops_after_direct_send() -> None:
    source = (PLUGIN_DIR / "main.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    functions = {
        node.name: node
        for node in ast.walk(tree)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    }
    assert "os.getenv" not in source
    assert "logger=_WEBSOCKET_LOGGER" in source
    assert "priority=sys.maxsize - 1" in source
    assert "AstrMessageEvent.get_message_str = redacted_text" in source
    assert "AstrMessageEvent.get_messages = redacted_messages" in source
    assert "_REMAIL_IGNORE_MARKER" not in source
    assert not (PLUGIN_DIR / "PERSONA.md").exists()

    headers_source = ast.unparse(functions["_bot_headers"])
    assert "event.get_platform_name()" in headers_source
    assert "event.get_platform_id()" not in headers_source
    assert "event.get_sender_id()" in headers_source
    assert "event.get_group_id()" in headers_source
    assert "normalize_adapter_identity" in headers_source
    assert "_channel_system_keys" in headers_source
    assert "X-Bot-Group" in headers_source
    assert "X-Bot-Channel" in headers_source
    assert "scene == 'group'" in headers_source
    group_block = headers_source.split("if scene == 'group':", 1)[1]
    assert "X-Bot-Group" in group_block
    assert headers_source.count("X-Bot-Group") == 1
    assert "X-Bot-Subject" in headers_source
    assert "require_subject" not in headers_source

    request_source = ast.unparse(functions["_websocket_request"])
    assert "groupId" in request_source
    assert "if group_id" in request_source

    authorize_source = ast.unparse(functions["_authorize_event"])
    assert "/v1/bot/context" in authorize_source
    llm_authorize_source = ast.unparse(functions["authorize_llm"])
    assert "priority=-sys.maxsize" in llm_authorize_source
    assert "_authorize_event" in llm_authorize_source
    assert "event.send" in llm_authorize_source
    assert "event.stop_event" in llm_authorize_source
    for name in (
        "docs",
        "announcements",
        "faqs",
        "remail_faqs",
        "remail_announcements",
        "remail_api_documentation",
    ):
        handler_source = ast.unparse(functions[name])
        assert "_authorize_event" in handler_source, name
        content_marker = (
            "_public_request"
            if name not in {"docs", "remail_api_documentation"}
            else ("openapi_spec" if name == "remail_api_documentation" else "docs_url")
        )
        assert handler_source.index("_authorize_event") < handler_source.index(
            content_marker
        ), name

    bind = functions["bind"]
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom)) for node in ast.walk(bind)
    )
    bind_source = ast.unparse(bind)
    assert "event.send" in bind_source
    assert "event.message_str" in bind_source
    assert "event.get_message_str" not in bind_source
    assert bind_source.index("event.send") < bind_source.index("event.stop_event")
    assert "_authorize_event" in bind_source
    assert bind_source.index("_authorize_event") < bind_source.index(
        "_BIND_ARGUMENTS"
    ), bind_source
    assert bind_source.index("_authorize_event") < bind_source.index(
        "绑定只允许在私聊中执行"
    ), bind_source

    result_source = ast.unparse(functions["_result_text"])
    assert "payload.get('message')" in result_source
    assert "requestId" not in result_source
    assert "_CHINESE_TEXT" in result_source

    for name in (
        "remail_help",
        "bind",
        "binding_status",
        "personal_info",
        "unbind",
        "diagnose_code",
        "projects",
        "inventory",
        "rankings",
        "ranking_rewards",
        "docs",
        "announcements",
        "faqs",
    ):
        assert "exc.message" not in ast.unparse(functions[name]), name

    for name in ("binding_status", "unbind"):
        handler = functions[name]
        handler_source = ast.unparse(handler)
        assert not any(
            isinstance(node, (ast.Yield, ast.YieldFrom)) for node in ast.walk(handler)
        )
        assert "event.send" in handler_source
        assert handler_source.index("event.send") < handler_source.index(
            "event.stop_event"
        )
        assert "_authorize_event" in handler_source

    for name in (
        "projects",
        "inventory",
        "rankings",
        "ranking_rewards",
        "docs",
        "announcements",
        "faqs",
    ):
        handler = functions[name]
        handler_source = ast.unparse(handler)
        assert not any(
            isinstance(node, (ast.Yield, ast.YieldFrom)) for node in ast.walk(handler)
        )
        assert "_reply" in handler_source

    private_target_source = ast.unparse(functions["_private_target"])
    assert "event.get_platform_id()" in private_target_source
    assert "event.get_platform_name()" in private_target_source
    assert "event.get_sender_id()" in private_target_source
    assert "MessageType.FRIEND_MESSAGE.value" in private_target_source

    profile_source = ast.unparse(functions["personal_info"])
    assert "/v1/bot/profile" in profile_source
    assert "_private_target" in profile_source
    assert "context.send_message" in profile_source
    assert "event.send" not in profile_source
    assert profile_source.index("context.send_message") < profile_source.rindex(
        "event.stop_event"
    )

    inventory_source = ast.unparse(functions["inventory"])
    assert "格式：/库存 <项目ID>" in inventory_source
    assert "project_id: str=''" in inventory_source
    assert "_authorize_event" in inventory_source
    assert "_PRODUCT_LABELS" in ast.unparse(functions["_format_projects"])
    assert "_PRODUCT_LABELS" in ast.unparse(functions["_format_inventory"])

    binding_status_source = ast.unparse(functions["_binding_status_text"])
    assert "result == 'unbound'" in binding_status_source
    assert "_UNBOUND_TEXT" in binding_status_source
    diagnosis_source = ast.unparse(functions["diagnose_code"])
    assert "格式：/诊断 邮箱 原因" in diagnosis_source
    assert "_DIAGNOSIS_ARGUMENTS" in diagnosis_source
    assert "body={'email': email}" in diagnosis_source
    assert "bindingRequired" in diagnosis_source
    assert "event.request_llm" not in diagnosis_source
    assert "context.llm_generate" not in diagnosis_source
    assert "_enforce_diagnosis_fact" in diagnosis_source
    assert "_enforce_black_box" in diagnosis_source
    assert "_enforce_group_privacy" in diagnosis_source
    assert "event.send" in diagnosis_source
    assert "event.stop_event" in diagnosis_source
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom))
        for node in ast.walk(functions["diagnose_code"])
    )
    tool_source = ast.unparse(functions["remail_code_diagnosis"])
    assert "description.strip()" in tool_source
    assert "body={'email': email}" in tool_source
    assert "accountUnavailable" in tool_source
    assert "_reply" in tool_source
    assert "project_id" not in tool_source


def test_diagnosis_binding_required_returns_message_without_llm() -> None:
    functions, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    assignments = [
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id in {"_DIAGNOSIS_ARGUMENTS", "_UNBOUND_TEXT"}
            for target in node.targets
        )
    ]
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "diagnose_code"
    )
    handler.decorator_list = []
    namespace = {
        "AstrMessageEvent": object,
        "MessageChain": lambda items: items,
        "MessageType": SimpleNamespace(GROUP_MESSAGE="group"),
        "Plain": lambda text: text,
        "_enforce_diagnosis_fact": functions["_enforce_diagnosis_fact"],
        "_enforce_black_box": functions["_enforce_black_box"],
        "_enforce_answer_scope": functions["_enforce_answer_scope"],
        "_enforce_group_privacy": functions["_enforce_group_privacy"],
        "_safe_user_error": lambda _exc: "失败",
        "json": json,
        "re": re,
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*assignments, handler], type_ignores=[])
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )

    class Plugin:
        payload = {
            "bindingRequired": True,
            "message": (
                "当前账号尚未绑定 ReMail。\n"
                "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
            ),
        }

        async def _request(self, *_args, **_kwargs):
            return self.payload

        @staticmethod
        def _result_text(payload, fallback):
            return payload.get("message") or fallback

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    event = SimpleNamespace(
        message_str="/诊断 order@example.com 一直没收到",
        get_message_type=lambda: "friend",
        send=send,
        stop_event=lambda: stopped.append(True),
    )
    asyncio.run(namespace["diagnose_code"](Plugin(), event))
    assert sent == [
        [
            "当前账号尚未绑定 ReMail。\n"
            "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
        ]
    ]
    assert stopped == [True]

    plugin = Plugin()
    plugin.payload = {
        "projectName": "ChatGPT",
        "message": "暂未发现明确异常。",
    }
    group_sent = []
    group_stopped = []

    async def send_group(message):
        group_sent.append(message)

    group_event = SimpleNamespace(
        message_str="/诊断 peptide_tech.3k@icloud.com 接不到码",
        get_message_type=lambda: "group",
        send=send_group,
        stop_event=lambda: group_stopped.append(True),
    )
    asyncio.run(namespace["diagnose_code"](plugin, group_event))
    assert group_sent == [
        [
            "该订单对应的是 ChatGPT 项目。 暂未发现明确异常。 "
            "请核对 ChatGPT 项目是否与目标业务一致。"
        ]
    ]
    assert "peptide_tech.3k@icloud.com" not in group_sent[0][0]
    assert group_stopped == [True]


def test_diagnosis_tool_directly_sends_binding_state() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    unbound = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_UNBOUND_TEXT"
            for target in node.targets
        )
    )
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_code_diagnosis"
    )
    handler.decorator_list = []
    namespace = {"AstrMessageEvent": object, "json": json}
    exec(
        compile(
            ast.Module(body=[unbound, handler], type_ignores=[]), "main.py", "exec"
        ),
        namespace,
    )

    class Plugin:
        payload = {}

        async def _request(self, *_args, **_kwargs):
            return self.payload

        async def _reply(self, _event, text):
            sent.append(text)

        @staticmethod
        def _result_text(payload, fallback):
            return payload.get("message") or fallback

    plugin = Plugin()
    sent = []
    extras = {}
    event = SimpleNamespace(set_extra=lambda key, value: extras.__setitem__(key, value))
    for payload in (
        {"bindingRequired": True, "message": "请绑定"},
        {"accountUnavailable": True, "message": "账号不可用"},
    ):
        plugin.payload = payload
        result = asyncio.run(
            namespace["remail_code_diagnosis"](
                plugin, event, "order@example.com", "接不到码"
            )
        )
        assert result == ""
    assert sent == ["请绑定", "账号不可用"]

    plugin.payload = {"projectName": "ChatGPT", "message": "暂未发现明确异常。"}
    result = asyncio.run(
        namespace["remail_code_diagnosis"](
            plugin, event, "order@example.com", "接不到码"
        )
    )
    assert json.loads(result)["projectName"] == "ChatGPT"
    assert extras["_remail_code_diagnosis_fact"] == {
        "projectName": "ChatGPT",
        "message": "暂未发现明确异常。",
    }


def test_system_keys_are_read_from_plugin_config() -> None:
    schema = json.loads((PLUGIN_DIR / "_conf_schema.json").read_text(encoding="utf-8"))
    assert "launch_system_key" not in schema
    assert "platform_system_keys" not in schema
    for field in ("qq_system_key", "telegram_system_key"):
        assert schema[field]["default"] == ""
        assert schema[field]["obvious_hint"] is True
        assert schema[field]["secret"] is True
    assert schema["qq_group_owner_id"]["default"] == ""
    assert "手工填写" in schema["qq_group_owner_id"]["hint"]
    assert schema["qq_group_admin_ids"]["default"] == []
    assert schema["qq_group_management"]["default"] == []
    assert "群号|群主QQ号" in schema["qq_group_management"]["hint"]
    assert "launch_poll_seconds" not in schema
    assert schema["auto_approve_join_requests"]["description"] == "自动批准加群"
    assert schema["auto_approve_join_requests"]["default"] is False
    assert schema["minimum_qq_level"]["default"] == 16
    assert schema["keyword_blacklist_enabled"]["default"] is False
    assert schema["keyword_blacklist"]["default"] == []
    assert schema["url_whitelist_enabled"]["default"] is False
    assert schema["url_whitelist_domains"]["default"] == []
    assert schema["welcome_enabled"]["description"] == "新人欢迎"
    assert schema["welcome_enabled"]["default"] is False
    assert schema["welcome_text"]["type"] == "text"
    welcome = schema["welcome_text"]["default"]
    assert "https://remail.aishop6.com" in welcome
    assert "https://catfk.com/shop/aishop6" in welcome
    assert "Outlook、iCloud 和域名邮箱" in welcome
    assert "@红夜" in welcome
    assert schema["feedback_enabled"]["description"] == "工作日报"
    assert schema["feedback_report_time"]["default"] == "20:00"
    assert "feedback_report_targets" not in schema


def test_group_join_welcome_uses_trusted_event_and_whitelist() -> None:
    functions, remail_error = _load_welcome_functions()
    joined = functions["_joined_group_members"]
    handler = functions["welcome_new_members"]

    def qq_event(raw, self_id="999"):
        return SimpleNamespace(
            get_platform_name=lambda: "aiocqhttp",
            get_self_id=lambda: self_id,
            message_obj=SimpleNamespace(raw_message=raw),
        )

    notice = {
        "post_type": "notice",
        "notice_type": "group_increase",
        "user_id": 123456789,
    }
    assert joined(qq_event(notice)) == [("123456789", "")]
    assert joined(qq_event(notice, self_id="123456789")) == []
    assert joined(qq_event({"post_type": "message"})) == []

    telegram_member = SimpleNamespace(id=456789, username="new_user", is_bot=False)
    telegram_event = SimpleNamespace(
        get_platform_name=lambda: "telegram",
        message_obj=SimpleNamespace(
            raw_message=SimpleNamespace(
                message=SimpleNamespace(new_chat_members=[telegram_member])
            )
        ),
    )
    assert joined(telegram_event) == [("456789", "new_user")]

    sent = AsyncMock()
    event = qq_event(notice)
    event.send = sent
    authorize = AsyncMock()
    plugin = SimpleNamespace(
        config={"welcome_enabled": True, "welcome_text": "欢迎加入"},
        _authorize_event=authorize,
    )
    asyncio.run(handler(plugin, event))
    authorize.assert_awaited_once_with(event)
    sent.assert_awaited_once_with(
        [("at", {"qq": "123456789", "name": ""}), ("plain", "欢迎加入")]
    )

    sent.reset_mock()
    authorize.reset_mock()
    plugin.config = {
        "welcome_enabled": False,
        "auto_approve_join_requests": True,
        "welcome_text": "欢迎加入",
    }
    asyncio.run(handler(plugin, event))
    authorize.assert_not_awaited()
    sent.assert_not_awaited()

    denied = qq_event(notice)
    denied.send = AsyncMock()
    plugin.config = {"welcome_enabled": True, "welcome_text": "欢迎加入"}
    plugin._authorize_event = AsyncMock(side_effect=remail_error("denied"))
    asyncio.run(handler(plugin, denied))
    denied.send.assert_not_awaited()


@pytest.mark.parametrize(
    ("level", "returned_user_id", "approved"),
    [
        (15, 123456789, False),
        (16, 123456789, True),
        (80, 987654321, False),
        (None, 123456789, False),
    ],
)
def test_qq_join_request_approval_enforces_level_and_identity(
    level: int | None, returned_user_id: int, approved: bool
) -> None:
    functions, _ = _load_welcome_functions()
    parse_request = functions["_qq_group_join_request"]
    handler = functions["auto_approve_qq_join_request"]
    raw = {
        "post_type": "request",
        "request_type": "group",
        "sub_type": "add",
        "group_id": 529642597,
        "user_id": 123456789,
        "flag": "request-flag",
    }
    bot = SimpleNamespace(call_action=AsyncMock())
    bot.call_action.side_effect = [
        {"user_id": returned_user_id, "qqLevel": level},
        None,
    ]
    event = SimpleNamespace(
        bot=bot,
        get_platform_name=lambda: "aiocqhttp",
        get_group_id=lambda: "529642597",
        get_sender_id=lambda: "123456789",
        message_obj=SimpleNamespace(raw_message=raw),
    )
    authorize = AsyncMock()
    plugin = SimpleNamespace(
        config={
            "auto_approve_join_requests": True,
            "minimum_qq_level": 16,
        },
        _authorize_event=authorize,
    )

    assert parse_request(event) == ("123456789", "request-flag")
    asyncio.run(handler(plugin, event))
    authorize.assert_awaited_once_with(event)
    assert bot.call_action.await_args_list[0].args == ("get_stranger_info",)
    assert bot.call_action.await_args_list[0].kwargs == {
        "user_id": 123456789,
        "no_cache": True,
    }
    assert bot.call_action.await_count == (2 if approved else 1)
    if approved:
        assert bot.call_action.await_args_list[1].args == ("set_group_add_request",)
        assert bot.call_action.await_args_list[1].kwargs == {
            "flag": "request-flag",
            "approve": True,
        }


def test_qq_join_request_ignores_invites_and_unauthorized_groups() -> None:
    functions, remail_error = _load_welcome_functions()
    parse_request = functions["_qq_group_join_request"]
    handler = functions["auto_approve_qq_join_request"]
    raw = {
        "post_type": "request",
        "request_type": "group",
        "sub_type": "invite",
        "group_id": 529642597,
        "user_id": 123456789,
        "flag": "request-flag",
    }
    event = SimpleNamespace(
        bot=SimpleNamespace(call_action=AsyncMock()),
        get_platform_name=lambda: "aiocqhttp",
        get_group_id=lambda: "529642597",
        get_sender_id=lambda: "123456789",
        message_obj=SimpleNamespace(raw_message=raw),
    )
    assert parse_request(event) is None

    raw["sub_type"] = "add"
    plugin = SimpleNamespace(
        config={"auto_approve_join_requests": True, "minimum_qq_level": 16},
        _authorize_event=AsyncMock(side_effect=remail_error("denied")),
    )
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_awaited_once_with(event)
    event.bot.call_action.assert_not_awaited()


def test_qq_group_moderation_extracts_cards_and_recalls_violations() -> None:
    functions, remail_error = _load_welcome_functions()
    extract = functions["_qq_moderation_text"]
    handler = functions["moderate_qq_group_message"]

    def make_event(segments):
        stopped = []
        event = SimpleNamespace(
            bot=SimpleNamespace(call_action=AsyncMock()),
            get_platform_name=lambda: "aiocqhttp",
            message_obj=SimpleNamespace(
                message_id="42",
                raw_message={"message": segments},
            ),
            stop_event=lambda: stopped.append(True),
        )
        return event, stopped

    segments = [
        {"type": "reply", "data": {"text": "旧消息 spam https://evil.test"}},
        {"type": "image", "data": {"url": "https://cdn.evil.test/a.jpg"}},
        {"type": "text", "data": {"text": "普通正文"}},
        {
            "type": "share",
            "data": {
                "url": "https://docs.example.com/help",
                "title": "产品说明",
                "content": "查看文档",
            },
        },
        {
            "type": "json",
            "data": {
                "data": json.dumps({"meta": {"jumpUrl": "https://outside.test/path"}})
            },
        },
    ]
    event, _ = make_event(segments)
    text = extract(event)
    assert "普通正文" in text
    assert "https://docs.example.com/help" in text
    assert "https://outside.test/path" in text
    assert "旧消息 spam" not in text
    assert "cdn.evil.test" not in text

    authorize = AsyncMock()
    plugin = SimpleNamespace(
        config={
            "keyword_blacklist_enabled": False,
            "url_whitelist_enabled": True,
            "url_whitelist_domains": ["example.com"],
        },
        _authorize_event=authorize,
    )
    event, stopped = make_event(segments)
    asyncio.run(handler(plugin, event))
    authorize.assert_awaited_once_with(event)
    event.bot.call_action.assert_awaited_once_with("delete_msg", message_id=42)
    assert stopped == [True]

    safe_event, safe_stopped = make_event(
        [{"type": "text", "data": {"text": "https://sub.example.com/path"}}]
    )
    authorize.reset_mock()
    asyncio.run(handler(plugin, safe_event))
    authorize.assert_not_awaited()
    safe_event.bot.call_action.assert_not_awaited()
    assert not safe_stopped

    denied_event, denied_stopped = make_event(
        [{"type": "text", "data": {"text": "spam"}}]
    )
    plugin.config = {
        "keyword_blacklist_enabled": True,
        "keyword_blacklist": ["spam"],
    }
    plugin._authorize_event = AsyncMock(side_effect=remail_error("denied"))
    asyncio.run(handler(plugin, denied_event))
    denied_event.bot.call_action.assert_not_awaited()
    assert not denied_stopped


@pytest.mark.parametrize(
    ("target_role", "expected_label"),
    [("owner", "群主"), ("admin", "管理员")],
)
def test_member_mentions_group_management_wake_remail_fae(
    target_role: str, expected_label: str
) -> None:
    functions, _ = _load_welcome_functions()
    mentioned_ids = functions["_mentioned_qq_ids"]
    handler = functions["handoff_group_manager_mentions"]
    extras = {}
    event = SimpleNamespace(
        bot=SimpleNamespace(call_action=AsyncMock()),
        get_platform_name=lambda: "aiocqhttp",
        get_sender_id=lambda: "123456789",
        get_group_id=lambda: "529642597",
        get_self_id=lambda: "999999999",
        message_obj=SimpleNamespace(
            raw_message={
                "message": [
                    {"type": "at", "data": {"qq": "888888888"}},
                    {"type": "at", "data": {"qq": "999999999"}},
                    {"type": "text", "data": {"text": "接码怎么使用？"}},
                ]
            }
        ),
        message_str="@管理成员(888888888) 接码怎么使用？",
        set_extra=lambda key, value: extras.__setitem__(key, value),
        is_wake=False,
        is_at_or_wake_command=False,
    )
    authorize = AsyncMock()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "777777777",
            "qq_group_admin_ids": [],
            "qq_group_management": [
                "529642597|888888888|"
                if target_role == "owner"
                else "529642597|777777777|888888888"
            ],
        },
        _authorize_event=authorize,
    )

    assert mentioned_ids(event) == ["888888888"]
    asyncio.run(handler(plugin, event))

    authorize.assert_awaited_once_with(event)
    assert event.message_str == "@管理成员(888888888) 接码怎么使用？"
    assert extras == {
        "_remail_admin_handoff_role": expected_label,
        "_remail_admin_handoff_text": "接码怎么使用？",
    }
    assert event.is_wake is True
    assert event.is_at_or_wake_command is True
    event.bot.call_action.assert_not_awaited()


def test_group_management_handoff_ignores_privileged_senders_and_unauthorized_groups() -> (
    None
):
    functions, remail_error = _load_welcome_functions()
    handler = functions["handoff_group_manager_mentions"]

    def make_event():
        extras = {}
        event = SimpleNamespace(
            bot=SimpleNamespace(call_action=AsyncMock()),
            get_platform_name=lambda: "aiocqhttp",
            get_sender_id=lambda: "123456789",
            get_group_id=lambda: "529642597",
            get_self_id=lambda: "999999999",
            message_obj=SimpleNamespace(
                raw_message={"message": [{"type": "at", "data": {"qq": "888888888"}}]}
            ),
            message_str="@群主(888888888)",
            set_extra=lambda key, value: extras.__setitem__(key, value),
            is_wake=False,
            is_at_or_wake_command=False,
        )
        return event, extras

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "888888888",
            "qq_group_admin_ids": ["123456789"],
        },
        _authorize_event=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    assert not extras
    assert event.is_at_or_wake_command is False
    plugin._authorize_event.assert_awaited_once_with(event)
    event.bot.call_action.assert_not_awaited()

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "777777777",
            "qq_group_admin_ids": [],
        },
        _authorize_event=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    assert not extras
    assert event.is_at_or_wake_command is False
    plugin._authorize_event.assert_not_awaited()

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={"qq_group_owner_id": "888888888", "qq_group_admin_ids": []},
        _authorize_event=AsyncMock(side_effect=remail_error("denied")),
        _reply=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    plugin._reply.assert_awaited_once()
    event.bot.call_action.assert_not_awaited()
    assert not extras
    assert event.is_at_or_wake_command is False


def test_only_explicit_mentions_reach_group_intent_classification() -> None:
    functions, remail_error = _load_welcome_functions()
    handler = functions["classify_mentioned_group_question"]

    async def reply(event, text):
        try:
            await event.send([("plain", text)])
        finally:
            event.stop_event()

    def make_event(
        text: str,
        *,
        mention_bot: bool = False,
        handoff_role: str = "",
        sender_id: str = "123456789",
    ):
        sent = []
        stopped = []
        extras = {}
        if handoff_role:
            extras = {
                "_remail_admin_handoff_role": handoff_role,
                "_remail_admin_handoff_text": text,
            }
        self_id = "999999999"
        segments = []
        if mention_bot:
            segments.append({"type": "at", "data": {"qq": self_id}})
        segments.append({"type": "text", "data": {"text": text}})

        async def send(message):
            sent.append(message)

        event = SimpleNamespace(
            get_extra=lambda key, default="": extras.get(key, default),
            get_platform_name=lambda: "aiocqhttp",
            get_sender_id=lambda: sender_id,
            get_self_id=lambda: self_id,
            get_messages=lambda: [],
            message_obj=SimpleNamespace(raw_message={"message": segments}),
            message_str=f"@{handoff_role} {text}" if handoff_role else text,
            unified_msg_origin="bot:GroupMessage:529642597",
            set_extra=lambda key, value: extras.__setitem__(key, value),
            is_wake=mention_bot or bool(handoff_role),
            is_at_or_wake_command=mention_bot or bool(handoff_role),
            send=send,
            stop_event=lambda: stopped.append(True),
        )
        return event, sent, stopped

    def make_plugin(decision: str = "REMAIL"):
        return SimpleNamespace(
            _authorize_event=AsyncMock(),
            _reply=reply,
            collect_group_feedback=AsyncMock(),
            remail_intent_contexts={},
            context=SimpleNamespace(
                get_current_chat_provider_id=AsyncMock(return_value="provider"),
                llm_generate=AsyncMock(
                    return_value=SimpleNamespace(completion_text=decision)
                ),
            ),
        )

    event, sent, stopped = make_event("接码怎么使用？")
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_awaited_once_with(event)
    assert not sent
    assert stopped == [True]

    event, sent, stopped = make_event("红夜，今天天气如何？")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_awaited_once_with(event)
    assert not sent
    assert stopped == [True]

    event, sent, stopped = make_event("/项目 github")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_not_awaited()
    assert not sent and not stopped

    event, sent, stopped = make_event("!今天天气如何？")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    assert not sent
    assert stopped == [True]

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_awaited_once_with(event)
    plugin.context.llm_generate.assert_awaited_once()
    classifier = json.loads(plugin.context.llm_generate.await_args.kwargs["prompt"])
    assert classifier == {"untrustedMessage": "接码怎么使用？"}
    assert not sent and not stopped

    plugin.context.llm_generate.reset_mock()
    follow_up, sent, stopped = make_event("那多久？", mention_bot=True)
    asyncio.run(handler(plugin, follow_up))
    follow_up_payload = json.loads(
        plugin.context.llm_generate.await_args.kwargs["prompt"]
    )
    assert follow_up_payload == {
        "untrustedMessage": "那多久？",
        "recentReMailMessage": "接码怎么使用？",
    }
    assert not sent and not stopped

    plugin.context.llm_generate.reset_mock()
    other_sender, sent, stopped = make_event(
        "那多久？", mention_bot=True, sender_id="987654321"
    )
    asyncio.run(handler(plugin, other_sender))
    other_payload = json.loads(plugin.context.llm_generate.await_args.kwargs["prompt"])
    assert other_payload == {"untrustedMessage": "那多久？"}
    assert not sent and not stopped

    event, sent, stopped = make_event("今天天气如何？", mention_bot=True)
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", functions["_REMAIL_ONLY_TEXT"])]]
    assert stopped == [True]

    event, sent, stopped = make_event("今天天气如何？", handoff_role="群主")
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_awaited_once()
    assert not sent
    assert not stopped
    assert event.is_wake is False
    assert event.is_at_or_wake_command is False
    assert event.message_str == "@群主 今天天气如何？"

    event, sent, stopped = make_event("API 怎么调用？", handoff_role="管理员")
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_awaited_once()
    assert event.is_wake is True
    assert event.is_at_or_wake_command is True
    assert event.message_str == "API 怎么调用？"
    assert not sent and not stopped

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin("")
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", functions["_REMAIL_INTENT_UNAVAILABLE_TEXT"])]]
    assert stopped == [True]

    event, sent, stopped = make_event("/weather", mention_bot=True)
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_awaited_once()
    assert sent == [[("plain", functions["_REMAIL_ONLY_TEXT"])]]

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin()
    plugin._authorize_event = AsyncMock(side_effect=remail_error("denied"))
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", "当前会话未获授权。")]]
    assert stopped == [True]

    source = (PLUGIN_DIR / "main.py").read_text(encoding="utf-8")
    assert "proactively_answer_remail_questions" not in source
    assert "_remail_proactive_support" not in source


def test_remote_base_url_requires_tls() -> None:
    assert (
        validated_base_url("https://remail.example.com/")
        == "https://remail.example.com"
    )
    assert validated_base_url("http://127.0.0.1:8080") == "http://127.0.0.1:8080"
    assert (
        websocket_url("https://remail.example.com")
        == "wss://remail.example.com/v1/bot/ws"
    )
    with pytest.raises(ValueError):
        validated_base_url("http://remail.example.com")


def test_push_subscription_topics_cursor_order_and_safe_renderer() -> None:
    source = (PLUGIN_DIR / "main.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    functions = {
        node.name: node
        for node in ast.walk(tree)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    }
    assignments = {
        target.id: ast.literal_eval(node.value)
        for node in tree.body
        if isinstance(node, ast.Assign)
        for target in node.targets
        if isinstance(target, ast.Name) and target.id == "_PUSH_TOPICS"
    }
    assert assignments["_PUSH_TOPICS"] == (
        "project.launched",
        "leaderboard.settled",
        "system.notice.updated",
        "system.announcement.updated",
        "email.discount.updated",
        "project.price.updated",
    )

    run_source = ast.unparse(functions["_run_websocket"])
    assert "X-Bot-Channel" in run_source
    assert "'topic': 'project.launched'" in run_source
    assert "'topics': list(_PUSH_TOPICS)" in run_source
    assert (
        run_source.count("contextlib.suppress(asyncio.CancelledError, Exception)") == 2
    )
    assert "/v1/bot/projects/launches" not in source
    handler_source = ast.unparse(functions["_handle_websocket_message"])
    assert "payload.get('topic') or payload.get('event')" in handler_source
    event_source = ast.unparse(functions["_deliver_push_event"])
    assert "raw_after_id.isdecimal()" in event_source
    assert "after_id = int(raw_after_id)" in event_source
    delivery_source = ast.unparse(functions["_deliver_push_to_destinations"])
    assert delivery_source.index("send_message") < delivery_source.index("put_kv_data")

    oldest = functions["_oldest_launch_cursor"]
    namespace = {"datetime": datetime, "timezone": timezone}
    exec(
        compile(ast.Module(body=[oldest], type_ignores=[]), "main.py", "exec"),
        namespace,
    )

    class CursorPlugin:
        launch_cursors = {
            "existing": (
                datetime(2026, 8, 31, tzinfo=timezone.utc),
                7,
                "2026-08-31T00:00:00Z",
            ),
            "new": (datetime.min.replace(tzinfo=timezone.utc), 0, ""),
        }

        async def _load_launch_cursors(self):
            return None

    assert asyncio.run(namespace["_oldest_launch_cursor"](CursorPlugin())) == (
        "2026-08-31T00:00:00Z",
        7,
    )

    render = _load_push_renderer()
    unsafe = "user@example.com\npassword=hunter2\npostgresql://root:pw@db/remail\nsk_1234567890\nBearer abcdefghijk"
    cases = {
        "project.launched": {
            "project": {"id": 7, "name": "GitHub", "description": unsafe},
            "databaseRows": "raw-db-row",
        },
        "leaderboard.settled": {
            "businessDate": "2026-08-31",
            "settledAt": "2026-08-31T00:00:00Z",
            "items": [
                {
                    "rank": 1,
                    "name": "user@example.com",
                    "successCount": 9,
                    "rewardAmount": "5.00",
                }
            ],
            "credentials": "raw-credential",
        },
        "system.notice.updated": {"notice": unsafe, "databaseRows": "raw-db-row"},
        "system.announcement.updated": {
            "announcements": [{"title": "Notice", "content": unsafe}],
            "credentials": "raw-credential",
        },
        "email.discount.updated": {"message": unsafe, "databaseRows": "raw-db-row"},
        "project.price.updated": {
            "projectId": 7,
            "name": "GitHub",
            "message": unsafe,
            "credentials": "raw-credential",
        },
    }
    for topic, payload in cases.items():
        rendered = render(topic, payload)
        assert rendered
        for secret in (
            "user@example.com",
            "hunter2",
            "root:pw",
            "sk_1234567890",
            "abcdefghijk",
            "raw-db-row",
            "raw-credential",
        ):
            assert secret not in rendered, (topic, rendered)
        assert len(rendered) <= 4000
    assert render("database.dumped", {"message": "raw-db-row"}) == ""
    renderer_source = ast.unparse(functions["_render_push_text"])
    assert "json.dumps" not in renderer_source
    assert "filter(" not in renderer_source
    assert "filter(" not in ast.unparse(functions["_format_announcements"])


def test_announcements_are_numbered_and_visually_separated() -> None:
    render = _load_announcement_formatter()
    result = render(
        {"notice": "系统正在试运营"},
        {
            "announcements": [
                {
                    "title": "公告：第一条",
                    "content": "第一段\n\n\n第二段",
                },
                {"title": "公告：公告：第二条", "content": "第二条正文"},
                {"title": "", "content": "无标题正文"},
            ]
        },
    )
    assert result == (
        "系统通知\n系统正在试运营\n\n"
        "公告（3 条）\n\n"
        "1. 第一条\n第一段\n\n第二段\n\n"
        "2. 第二条\n第二条正文\n\n"
        "3. 未命名公告\n无标题正文"
    )
    assert "公告：公告：" not in result
    assert render({}, {"announcements": []}) == "暂无系统通知或公告。"


def test_push_cursor_advances_only_for_successful_destination() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    delivery = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "_deliver_push_to_destinations"
    )

    class TestReMailError(RuntimeError):
        pass

    namespace = {
        "Any": Any,
        "datetime": datetime,
        "timezone": timezone,
        "ReMailError": TestReMailError,
        "_render_push_text": _load_push_renderer(),
        "MessageChain": lambda items: items,
        "Plain": lambda value: value,
    }
    exec(
        compile(ast.Module(body=[delivery], type_ignores=[]), "main.py", "exec"),
        namespace,
    )

    class Context:
        async def send_message(self, destination, _message):
            if destination == "failed":
                raise RuntimeError("adapter unavailable")
            return True

    class Plugin:
        config = {"launch_destinations": ["sent", "failed"]}
        context = Context()
        launch_cursors = {}

        def __init__(self):
            self.saved = []

        async def _load_launch_cursors(self):
            return None

        @staticmethod
        def _launch_cursor_key(destination):
            return f"cursor:{destination}"

        async def put_kv_data(self, key, value):
            self.saved.append((key, value))

    plugin = Plugin()
    after_id = 2**63 + 123
    with pytest.raises(TestReMailError):
        asyncio.run(
            namespace["_deliver_push_to_destinations"](
                plugin,
                "project.price.updated",
                {"projectId": 7, "name": "GitHub", "message": "接码价格已更新"},
                "2026-08-31T01:02:03.123456Z",
                after_id,
            )
        )
    assert plugin.saved == [
        (
            "cursor:sent",
            {"after": "2026-08-31T01:02:03.123456Z", "afterId": after_id},
        )
    ]
    assert "sent" in plugin.launch_cursors
    assert "failed" not in plugin.launch_cursors
