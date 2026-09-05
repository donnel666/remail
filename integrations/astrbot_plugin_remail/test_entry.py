"""Run with ASTRBOT_SOURCE=/path/to/AstrBot to exercise its real waking stage."""

import ast
import asyncio
import enum
import inspect
import os
import re
import sys
import types
import typing
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import AsyncMock

import pytest

from .test_security import _load_welcome_functions
from .security import (
    contains_sensitive_command,
    normalize_security_text,
    redact_message_outline,
)


def _load(path, names, namespace):
    nodes = [
        node
        for node in ast.walk(ast.parse(path.read_text(encoding="utf-8")))
        if (
            isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef))
            and node.name in names
        )
        or (
            isinstance(node, ast.Assign)
            and any(
                isinstance(target, ast.Name) and target.id in names
                for target in node.targets
            )
        )
        or (
            isinstance(node, ast.AnnAssign)
            and isinstance(node.target, ast.Name)
            and node.target.id in names
        )
    ]
    for node in nodes:
        if not isinstance(node, (ast.Assign, ast.AnnAssign)):
            node.decorator_list = []
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


class Plain:
    def __init__(self, text):
        self.text = text


class At:
    def __init__(self, qq, name=""):
        self.qq, self.name = qq, name


class MessageType(enum.Enum):
    FRIEND_MESSAGE = "FriendMessage"
    GROUP_MESSAGE = "GroupMessage"


class Chain:
    def __init__(self, items=None):
        self.chain = items or []

    def message(self, text):
        self.chain.append(Plain(text))
        return self

    def use_markdown(self, _enabled):
        return self


class Event:
    def __init__(self, text, *, private=False, mention=None, sender="10001"):
        self.message_str, self.private, self.sender = text, private, sender
        self._extras, self.sent = {}, []
        self.stopped = self.is_at_or_wake_command = False
        self.role = "member"
        self.session_id = sender if private else "20001"
        segments = [{"type": "at", "data": {"qq": mention}}] if mention else []
        segments.append({"type": "text", "data": {"text": text}})
        self.message_obj = NS(
            type=MessageType.FRIEND_MESSAGE if private else MessageType.GROUP_MESSAGE,
            message_id="123",
            message=([At(mention)] if mention else []) + [Plain(text)],
            raw_message={"message": segments},
        )
        self.bot = NS(call_action=AsyncMock())

    @property
    def unified_msg_origin(self):
        return f"qq:{'FriendMessage' if self.private else 'GroupMessage'}:{self.session_id}"

    def get_message_type(self):
        return self.message_obj.type

    def get_platform_name(self):
        return "aiocqhttp"

    def get_platform_id(self):
        return "qq"

    def get_sender_id(self):
        return self.sender

    def get_self_id(self):
        return "90001"

    def get_group_id(self):
        return "" if self.private else "20001"

    def get_message_str(self):
        return self.message_str

    def get_message_outline(self):
        return self.message_str

    def get_messages(self):
        return self.message_obj.message

    def is_private_chat(self):
        return self.private

    def is_admin(self):
        return False

    def is_stopped(self):
        return self.stopped

    def stop_event(self):
        self.stopped = True

    def clear_result(self):
        pass

    def set_extra(self, key, value):
        self._extras[key] = value

    def get_extra(self, key=None, default=None):
        return self._extras if key is None else self._extras.get(key, default)

    async def send(self, chain):
        self.sent.append("".join(getattr(item, "text", "") for item in chain.chain))


@pytest.fixture
def lifecycle(monkeypatch):
    source = os.environ.get("ASTRBOT_SOURCE")
    if not source:
        pytest.skip("Set ASTRBOT_SOURCE to test the installed AstrBot lifecycle")
    source = Path(source) / "astrbot"
    namespace, _ = _load_welcome_functions()
    namespace.update(
        re=re,
        inspect=inspect,
        types=types,
        typing=typing,
        enum=enum,
        Stage=object,
        HandlerFilter=object,
        At=At,
        Plain=Plain,
        MessageType=MessageType,
        AtAll=type("AtAll", (), {}),
        Reply=type("Reply", (), {}),
        MessageChain=Chain,
        MessageEventResult=Chain,
        CommandResult=type("CommandResult", (), {}),
        AstrMessageEvent=Event,
        contains_sensitive_command=contains_sensitive_command,
        redact_message_outline=redact_message_outline,
        CommandGroupFilter=type("CommandGroupFilter", (), {}),
        logger=NS(
            **{
                name: lambda *a, **kw: None
                for name in ("debug", "info", "warning", "error")
            }
        ),
    )
    for relative, names in (
        ("core/star/filter/permission.py", {"PermissionType", "PermissionTypeFilter"}),
        (
            "core/star/filter/command.py",
            {"GreedyStr", "unwrap_optional", "CommandFilter"},
        ),
        (
            "core/pipeline/waking_check/stage.py",
            {
                "WakingCheckStage",
                "build_unique_session_id",
                "UNIQUE_SESSION_ID_BUILDERS",
            },
        ),
        ("core/pipeline/context_utils.py", {"call_handler"}),
        ("core/pipeline/process_stage/method/star_request.py", {"StarRequestSubStage"}),
    ):
        _load(source / relative, names, namespace)
    main_path = Path(__file__).with_name("main.py")
    _load(
        main_path,
        {
            "ReMailError",
            "_safe_user_error",
            "_service_entry_requested",
            "_install_early_entry_guard",
            "bind",
            "binding_status",
            "unbind",
            "projects",
            "_private_target",
            "_result_text",
            "_binding_status_text",
            "_UNBOUND_TEXT",
            "_CHINESE_TEXT",
            "_install_binding_log_redaction",
            "_remove_binding_log_redaction",
        },
        namespace,
    )

    class Plugin:
        pass

    plugin = Plugin()
    plugin.config = {"qq_group_owner_id": "30001", "qq_group_admin_ids": ["30002"]}
    plugin.context = NS(
        send_message=AsyncMock(return_value=True), llm_generate=AsyncMock()
    )
    plugin.calls, plugin.bound, plugin.available = [], False, False

    async def request(method, path, **kwargs):
        plugin.calls.append((method, path, kwargs))
        if path == "/v1/bot/context":
            return {
                "authorized": True,
                "bound": plugin.bound,
                "accountAvailable": plugin.available,
            }
        return {"result": "unbound", "message": "操作成功", "items": []}

    plugin._request = request
    plugin._private = lambda event: event.private
    plugin._format_projects = lambda payload: "项目列表"
    for name in ("_reply", "_private_target", "_result_text", "_binding_status_text"):
        setattr(plugin, name, namespace[name])
        setattr(Plugin, name, staticmethod(namespace[name]))
    namespace["Main"] = Plugin
    for name in (
        "_authorize_event",
        "require_bound_service_user",
        "prepare_remail_llm_response",
        "moderate_qq_group_message",
        "handoff_group_manager_mentions",
        "welcome_new_members",
        "auto_approve_qq_join_request",
        "bind",
        "binding_status",
        "unbind",
        "projects",
    ):
        setattr(plugin, name, types.MethodType(namespace[name], plugin))

    metadata = NS(name="remail", activated=True, reserved=False, star_cls=plugin)
    star_map = {Plugin.__module__: metadata, "builtin": NS(name="builtin_commands")}
    session_enabled = AsyncMock(return_value=True)
    sessions = NS(is_plugin_enabled_for_session=session_enabled)

    async def filter_sessions(event, handlers):
        return (
            handlers
            if await session_enabled(event.unified_msg_origin, "remail")
            else [h for h in handlers if h.handler_module_path == "builtin"]
        )

    sessions.filter_handlers_by_session = filter_sessions
    namespace.update(
        star_map=star_map,
        SessionPluginManager=sessions,
        EventType=NS(AdapterMessageEvent="message"),
    )
    handlers = []
    namespace["star_handlers_registry"] = NS(
        get_handlers_by_event_type=lambda *a, **kw: handlers
    )
    waking = namespace["WakingCheckStage"]
    for name, attributes in (
        (
            "astrbot.core.pipeline.waking_check.stage",
            {
                "WakingCheckStage": waking,
                "build_unique_session_id": namespace["build_unique_session_id"],
            },
        ),
        (
            "astrbot.core.star.session_plugin_manager",
            {"SessionPluginManager": sessions},
        ),
        ("astrbot.core.star.star", {"star_map": star_map}),
    ):
        module = types.ModuleType(name)
        module.__dict__.update(attributes)
        monkeypatch.setitem(sys.modules, name, module)

    def register(name, command=None, *, builtin=False, permission=False):
        handler = getattr(plugin, name, None)
        if builtin:

            async def handler(event, **kwargs):
                pass

        md = NS(
            handler=handler,
            handler_module_path="builtin" if builtin else Plugin.__module__,
            handler_full_name=name,
            handler_name=name,
        )
        if command:
            filt = namespace["CommandFilter"](
                command, alias={"bind"} if command == "绑定" else None
            )
            filt.handler_params = {"key": str, "value": str} if command == "set" else {}
            md.event_filters = [filt]
        else:
            md.event_filters = [NS(filter=lambda *a: True)]
        if permission:
            md.event_filters.append(
                namespace["PermissionTypeFilter"](namespace["PermissionType"].ADMIN)
            )
        handlers.append(md)

    for name in ("moderate_qq_group_message", "require_bound_service_user"):
        register(name)
    register("bind", "绑定")
    register("prepare_remail_llm_response")
    for name, command in (
        ("binding_status", "绑定状态"),
        ("unbind", "解绑"),
        ("projects", "项目"),
    ):
        register(name, command)
    register("set", "set", builtin=True)
    register("provider", "provider", builtin=True, permission=True)
    config = {
        "platform_settings": {},
        "admins_id": [],
        "wake_prefix": ["/"],
        "provider_settings": {"prompt_prefix": "", "identifier": False},
    }
    stage = waking()
    asyncio.run(stage.initialize(NS(astrbot_config=config)))
    callbacks = namespace["StarRequestSubStage"]()
    original = waking.process
    namespace["_install_binding_log_redaction"]()
    remove = namespace["_install_early_entry_guard"](plugin)

    async def run(event, dispatch=True):
        await stage.process(event)
        if dispatch and not event.is_stopped():
            async for _ in callbacks.process(event):
                pass
        return event

    yield NS(
        plugin=plugin,
        stage=stage,
        namespace=namespace,
        run=run,
        metadata=metadata,
        session_enabled=session_enabled,
        original=original,
        remove=remove,
        waking=waking,
    )
    remove()
    namespace["_remove_binding_log_redaction"]()


def test_real_waking_entry_and_bootstrap(lifecycle):
    case = lifecycle
    for text in ("/绑定", "/bind", "/绑定状态", "/解绑"):
        event = asyncio.run(case.run(Event(text, private=True)))
        assert event.message_str == text[1:]
        assert event.sent and event.is_stopped()
    assert not any(path == "/v1/bot/context" for _, path, _ in case.plugin.calls)
    event = Event("/绑定 user@example.test sentinel-password", private=True)
    assert "sentinel-password" not in event.get_message_outline()
    event = asyncio.run(case.run(event))
    assert case.plugin.calls[-1][2]["body"] == {
        "email": "user@example.test",
        "password": "sentinel-password",
    }
    assert "sentinel-password" not in "".join(event.sent)
    case.stage.ctx.astrbot_config["wake_prefix"] = ["bot "]
    event = asyncio.run(case.run(Event("bot 绑定", private=True)))
    assert event.message_str == "绑定" and event.sent
    case.stage.ctx.astrbot_config["wake_prefix"] = ["/"]
    case.plugin.context.llm_generate.assert_not_awaited()
    for bound, available in ((False, False), (True, False)):
        case.plugin.bound, case.plugin.available = bound, available
        for text, mention in (
            ("/项目", None),
            ("多少钱", "90001"),
            ("/set", "90001"),
            ("/provider", "90001"),
            ("/绑定 user@example.test sentinel-password", None),
        ):
            case.plugin.calls.clear()
            event = asyncio.run(case.run(Event(text, mention=mention)))
            assert not event.sent and event.is_stopped()
            assert [path for _, path, _ in case.plugin.calls] == ["/v1/bot/context"]
            assert (
                case.plugin.context.send_message.await_args.args[0]
                == "qq:FriendMessage:10001"
            )


def test_bound_filter_errors_are_safe_and_ownership_is_restored(lifecycle):
    case = lifecycle
    case.plugin.bound = case.plugin.available = True
    for text in ("/set", "/provider"):
        event = asyncio.run(case.run(Event(text, mention="90001")))
        assert event.sent == [
            normalize_security_text(case.namespace["_REMAIL_SAFE_ERROR_TEXT"])
        ]
        assert event.is_stopped()
    event = asyncio.run(case.run(Event("咨询问题", private=True), dispatch=False))
    assert not case.namespace["_event_is_owned"](event)
    assert event.message_str == "咨询问题"
    event = asyncio.run(case.run(Event("/项目")))
    assert event.sent == ["项目列表"]


def test_entry_scope_and_uninstall(lifecycle):
    case = lifecycle
    for text in ("普通聊天", "项目 管理"):
        event = asyncio.run(case.run(Event(text)))
        assert not event.sent and not event.is_stopped()
        assert not hasattr(event, "_remail_send_guard_installed")
    assert not case.plugin.calls
    for attribute, value in (("activated", False), ("star_cls", object())):
        original = getattr(case.metadata, attribute)
        setattr(case.metadata, attribute, value)
        event = asyncio.run(case.run(Event("问题", mention="90001"), dispatch=False))
        assert not event.is_stopped() and not hasattr(
            event, "_remail_send_guard_installed"
        )
        setattr(case.metadata, attribute, original)
    case.stage.ctx.astrbot_config["plugin_set"] = ["other"]
    event = asyncio.run(case.run(Event("问题", mention="90001"), dispatch=False))
    assert not event.is_stopped() and not case.plugin.calls
    case.stage.ctx.astrbot_config["plugin_set"] = ["*"]
    case.session_enabled.return_value = False
    case.stage.unique_session = True
    event = asyncio.run(case.run(Event("问题", mention="90001"), dispatch=False))
    assert not event.is_stopped() and not case.plugin.calls
    assert event.unified_msg_origin == "qq:GroupMessage:10001_20001"
    case.remove()
    assert case.waking.process is case.original


def test_management_and_moderation_do_not_require_binding(lifecycle):
    case = lifecycle
    event = Event("联系群主", mention="30001", sender="30002")
    asyncio.run(case.run(event))
    asyncio.run(case.plugin.handoff_group_manager_mentions(event))
    assert not case.plugin.calls and not event.sent
    case.plugin.config.update(
        keyword_blacklist_enabled=True, keyword_blacklist=["违规"]
    )
    for mention in (None, "90001"):
        event = asyncio.run(case.run(Event("违规", mention=mention)))
        event.bot.call_action.assert_awaited_once_with("delete_msg", message_id=123)
        assert not event.sent
    case.plugin.context.send_message.assert_not_awaited()
    case.plugin.config.update(welcome_enabled=True, welcome_text="欢迎加入")
    event = Event("")
    event.message_obj.raw_message = {
        "post_type": "notice",
        "notice_type": "group_increase",
        "user_id": "10001",
    }
    asyncio.run(case.run(event))
    asyncio.run(case.plugin.welcome_new_members(event))
    assert event.sent == ["欢迎加入"]
    case.plugin.config["auto_approve_join_requests"] = True
    event = Event("")
    event.message_obj.raw_message = {
        "post_type": "request",
        "request_type": "group",
        "sub_type": "add",
        "user_id": "10001",
        "group_id": "20001",
        "flag": "join-request",
    }
    event.bot.call_action.return_value = {"qqLevel": 20, "user_id": 10001}
    asyncio.run(case.run(event))
    asyncio.run(case.plugin.auto_approve_qq_join_request(event))
    assert event.bot.call_action.await_args.args == ("set_group_add_request",)
    assert event.bot.call_action.await_args.kwargs == {
        "flag": "join-request",
        "approve": True,
    }
