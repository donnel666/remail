import ast
import asyncio
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import pytest

from .security import redact_message_outline, validated_base_url, websocket_url


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


def test_event_key_requires_exact_platform_id_and_binding_stops_after_direct_send() -> (
    None
):
    source = (PLUGIN_DIR / "main.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    functions = {
        node.name: node
        for node in ast.walk(tree)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    }
    key_source = ast.unparse(functions["_system_key"])
    assert "get_platform_id" in key_source
    assert "get_platform_name" not in key_source
    assert "_service_key" not in key_source
    assert "REMAIL_BOT_[A-Z0-9_]+" in source
    assert "logger=_WEBSOCKET_LOGGER" in source
    assert "priority=sys.maxsize - 1" in source
    assert "CoreMessageEvent.get_messages = redacted_messages" in source

    headers_source = ast.unparse(functions["_bot_headers"])
    assert "event.get_group_id()" in headers_source
    assert "X-Bot-Group" in headers_source
    assert "scene == 'group'" in headers_source
    group_block = headers_source.split("if scene == 'group':", 1)[1].split(
        "if require_subject:", 1
    )[0]
    assert "X-Bot-Group" in group_block
    assert headers_source.count("X-Bot-Group") == 1

    request_source = ast.unparse(functions["_websocket_request"])
    assert "groupId" in request_source
    assert "if group_id" in request_source

    authorize_source = ast.unparse(functions["_authorize_event"])
    assert "/v1/bot/context" in authorize_source
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
    assert bind_source.index("event.send") < bind_source.index("event.stop_event")
    assert "_authorize_event" in bind_source
    assert bind_source.index("_authorize_event") < bind_source.index(
        "_BIND_ARGUMENTS"
    ), bind_source
    assert bind_source.index("_authorize_event") < bind_source.index(
        "绑定只允许在私聊中执行"
    ), bind_source


def test_system_keys_are_read_from_environment() -> None:
    schema = json.loads((PLUGIN_DIR / "_conf_schema.json").read_text(encoding="utf-8"))
    assert "default_system_key" not in schema
    fields = schema["platform_system_keys"]["templates"]["system_key"]["items"]
    assert "system_key" not in fields
    assert fields["system_key_env"]["default"] == ""
    assert "launch_poll_seconds" not in schema


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
    assert "'topic': 'project.launched'" in run_source
    assert "'topics': list(_PUSH_TOPICS)" in run_source
    assert "/v1/bot/projects/launches" not in source
    handler_source = ast.unparse(functions["_handle_websocket_message"])
    assert "payload.get('topic') or payload.get('event')" in handler_source
    event_source = ast.unparse(functions["_deliver_push_event"])
    assert "raw_after_id.isdecimal()" in event_source
    assert "after_id = int(raw_after_id)" in event_source
    delivery_source = ast.unparse(functions["_deliver_push_to_destinations"])
    assert delivery_source.index("send_message") < delivery_source.index("put_kv_data")

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
    assert "json.dumps" not in ast.unparse(functions["_render_push_text"])


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
