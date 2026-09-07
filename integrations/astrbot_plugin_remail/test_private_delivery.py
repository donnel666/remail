"""Regression check for QQ group-member temporary private delivery."""

import ast
import asyncio
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock


def test_group_member_delivery_uses_group_scoped_send_msg():
    source = Path(__file__).with_name("main.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    method = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "_send_private_text"
    )
    method.decorator_list = []
    namespace = {
        "Any": object,
        "AstrMessageEvent": object,
        "_event_is_private": lambda _event: False,
        "_positive_platform_id": lambda value: str(value) if str(value).isdigit() else "",
        "MessageChain": lambda items: items,
        "Plain": lambda value: value,
    }
    exec(compile(ast.Module(body=[method], type_ignores=[]), "main.py", "exec"), namespace)

    async def run():
        bot = SimpleNamespace(call_action=AsyncMock(return_value={"message_id": 1}))
        event = SimpleNamespace(
            bot=bot,
            get_platform_name=lambda: "aiocqhttp",
            get_group_id=lambda: "529642597",
            get_sender_id=lambda: "9845248",
            get_self_id=lambda: "577495930",
        )
        plugin = SimpleNamespace()
        assert await namespace["_send_private_text"](plugin, event, "请私聊绑定")
        bot.call_action.assert_awaited_once()
        action, kwargs = bot.call_action.await_args.args[0], bot.call_action.await_args.kwargs
        assert action == "send_msg"
        assert kwargs["message_type"] == "private"
        assert kwargs["user_id"] == "9845248"
        assert kwargs["group_id"] == "529642597"
        assert kwargs["self_id"] == "577495930"
        assert kwargs["message"] == [{"type": "text", "data": {"text": "请私聊绑定"}}]

    asyncio.run(run())


def test_private_entry_requires_membership_in_authorized_group():
    source = Path(__file__).with_name("main.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    methods = [
        node
        for node in ast.walk(tree)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name in {"_private_member_allowed", "_configured_private_group_ids"}
    ]
    for method in methods:
        method.decorator_list = []
    namespace = {
        "Any": object,
        "AstrMessageEvent": object,
        "_event_is_private": lambda _event: True,
        "_positive_platform_id": lambda value: str(value) if str(value).isdigit() else "",
    }
    exec(compile(ast.Module(body=methods, type_ignores=[]), "main.py", "exec"), namespace)

    async def run():
        bot = SimpleNamespace(call_action=AsyncMock(return_value={"user_id": "9845248"}))
        extras = {"_remail_allowed_group_ids": ["529642597"]}
        event = SimpleNamespace(
            bot=bot,
            get_platform_name=lambda: "aiocqhttp",
            get_sender_id=lambda: "9845248",
            get_self_id=lambda: "577495930",
            get_extra=lambda key, default=None: extras.get(key, default),
            set_extra=lambda key, value: extras.__setitem__(key, value),
        )
        plugin = SimpleNamespace(config={})
        assert await namespace["_private_member_allowed"](plugin, event)
        bot.call_action.assert_awaited_once_with(
            "get_group_member_info",
            group_id="529642597",
            user_id="9845248",
            no_cache=True,
            self_id="577495930",
        )
        bot.call_action.reset_mock()
        extras["_remail_allowed_group_ids"] = ["650384960"]
        extras.pop("_remail_private_member_allowed", None)
        bot.call_action.return_value = None
        assert not await namespace["_private_member_allowed"](plugin, event)

    asyncio.run(run())
