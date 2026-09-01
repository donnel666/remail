import ast
import asyncio
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from unittest.mock import AsyncMock

import pytest

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


def _load_fae_filter():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    body = []
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name)
            and (target.id.startswith("_PUSH_") or target.id == "_INTERNAL_DETAIL")
            for target in node.targets
        ):
            body.append(node)
        elif isinstance(node, ast.FunctionDef) and node.name in {
            "_safe_push_value",
            "_safe_fae_completion",
        }:
            body.append(node)
    namespace = {"Any": Any, "re": re}
    exec(compile(ast.Module(body=body, type_ignores=[]), "main.py", "exec"), namespace)
    return namespace["_safe_fae_completion"]


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
    helpers = [
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef)
        and node.name
        in {
            "_joined_group_members",
            "_qq_group_join_request",
            "_structured_strings",
            "_qq_moderation_text",
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
        "MessageChain": lambda items: items,
        "Plain": lambda text: ("plain", text),
        "ReMailError": ReMailError,
        "logger": SimpleNamespace(warning=lambda *_args: None),
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*helpers, *handlers], type_ignores=[])
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

    safe_completion = _load_fae_filter()
    assert safe_completion("该邮箱对应 GitHub 项目，请核对。", "fallback") == (
        "该邮箱对应 GitHub 项目，请核对。"
    )
    for unsafe in ("内部别名 route_a", "代理节点 provider_x", "源站渠道 vendor_x"):
        assert safe_completion(unsafe, "fallback") == "fallback"


def test_llm_request_requires_remail_event_authorization() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "authorize_llm"
    )
    handler.decorator_list = []
    helpers = _load_user_error_helpers()
    namespace = {
        "AstrMessageEvent": object,
        "ProviderRequest": object,
        "MessageChain": lambda items: items,
        "Plain": lambda text: text,
        "ReMailError": helpers["ReMailError"],
        "_safe_user_error": helpers["_safe_user_error"],
    }
    exec(
        compile(ast.Module(body=[handler], type_ignores=[]), "main.py", "exec"),
        namespace,
    )

    class Plugin:
        async def _authorize_event(self, _event):
            raise helpers["ReMailError"](401, "Authentication is required.")

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    event = SimpleNamespace(send=send, stop_event=lambda: stopped.append(True))
    asyncio.run(namespace["authorize_llm"](Plugin(), event, object()))
    assert sent == [["当前会话未获授权。"]]
    assert stopped == [True]


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
    assert diagnosis_source.index("bindingRequired") < diagnosis_source.index(
        "context.llm_generate"
    )
    assert "event.request_llm" not in diagnosis_source
    assert "context.llm_generate" in diagnosis_source
    assert "get_current_chat_provider_id" in diagnosis_source
    assert "sanitize_feedback_text(description)" in diagnosis_source
    assert "event.send" in diagnosis_source
    assert "event.stop_event" in diagnosis_source
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom))
        for node in ast.walk(functions["diagnose_code"])
    )
    assert "该邮箱对应的是" in diagnosis_source
    assert "请核对" in diagnosis_source
    prompt_assignment = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_DIAGNOSIS_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    diagnosis_prompt = ast.literal_eval(prompt_assignment.value)
    for rule in (
        "购买邮箱",
        "接码",
        "绝不能把订单类型本身当作失败原因",
        "不得擅自断定买错项目",
        "不得输出邮箱",
    ):
        assert rule in diagnosis_prompt
    tool_source = ast.unparse(functions["remail_code_diagnosis"])
    assert "description.strip()" in tool_source
    assert "body={'email': email}" in tool_source
    assert "accountUnavailable" in tool_source
    assert "_reply" in tool_source
    assert "project_id" not in tool_source


def test_diagnosis_binding_required_returns_message_without_llm() -> None:
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
        "Plain": lambda text: text,
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
        context = SimpleNamespace(
            get_current_chat_provider_id=lambda *_args: pytest.fail(
                "unbound diagnosis must not call LLM"
            ),
            llm_generate=lambda **_kwargs: pytest.fail(
                "unbound diagnosis must not call LLM"
            ),
        )

        async def _request(self, *_args, **_kwargs):
            return {
                "bindingRequired": True,
                "message": (
                    "当前账号尚未绑定 ReMail。\n"
                    "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
                ),
            }

        @staticmethod
        def _result_text(payload, fallback):
            return payload.get("message") or fallback

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    event = SimpleNamespace(
        message_str="/诊断 order@example.com 一直没收到",
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
    for payload in (
        {"bindingRequired": True, "message": "请绑定"},
        {"accountUnavailable": True, "message": "账号不可用"},
    ):
        plugin.payload = payload
        result = asyncio.run(
            namespace["remail_code_diagnosis"](
                plugin, object(), "order@example.com", "接不到码"
            )
        )
        assert result == ""
    assert sent == ["请绑定", "账号不可用"]


def test_system_keys_are_read_from_plugin_config() -> None:
    schema = json.loads((PLUGIN_DIR / "_conf_schema.json").read_text(encoding="utf-8"))
    assert "launch_system_key" not in schema
    assert "platform_system_keys" not in schema
    for field in ("qq_system_key", "telegram_system_key"):
        assert schema[field]["default"] == ""
        assert schema[field]["obvious_hint"] is True
        assert schema[field]["secret"] is True
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

    denied = qq_event(notice)
    denied.send = AsyncMock()
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
