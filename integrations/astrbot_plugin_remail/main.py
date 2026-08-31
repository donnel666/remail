from __future__ import annotations

import asyncio
import contextlib
import hashlib
import json
import logging
import os
import re
import sys
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from time import monotonic
from typing import Any

import httpx
import websockets

from astrbot.api import AstrBotConfig, logger
from astrbot.api.event import AstrMessageEvent, MessageChain, filter
from astrbot.api.message_components import Plain
from astrbot.api.star import Context, Star
from astrbot.core.platform.astr_message_event import (
    AstrMessageEvent as CoreMessageEvent,
)
from astrbot.core.platform.message_type import MessageType

from .security import (
    contains_binding_command,
    redact_message_outline,
    validated_base_url,
    websocket_url,
)


_WEBSOCKET_LOGGER = logging.getLogger("remail.websocket.transport")
_WEBSOCKET_LOGGER.addHandler(logging.NullHandler())
_WEBSOCKET_LOGGER.propagate = False
_WEBSOCKET_LOGGER.setLevel(logging.WARNING)


def _install_binding_log_redaction() -> None:
    """Patch the shared outline method because EventBus logs before handlers run."""
    if not getattr(CoreMessageEvent, "_remail_redaction_installed", False):
        original_outline = CoreMessageEvent.get_message_outline

        def redacted(event: CoreMessageEvent) -> str:
            return redact_message_outline(
                event.get_message_str(), original_outline(event)
            )

        CoreMessageEvent.get_message_outline = redacted
        CoreMessageEvent._remail_redaction_installed = True
    if not getattr(CoreMessageEvent, "_remail_history_redaction_installed", False):
        original_messages = CoreMessageEvent.get_messages

        def redacted_messages(event: CoreMessageEvent):
            messages = original_messages(event)
            if contains_binding_command(
                event.get_message_str(), event.get_message_outline()
            ):
                return [Plain("/绑定 [REDACTED]")]
            return messages

        CoreMessageEvent.get_messages = redacted_messages
        CoreMessageEvent._remail_history_redaction_installed = True


_BIND_ARGUMENTS = re.compile(
    r"(?:^|\s)/?(?:绑定|bind)(?:@[a-z0-9_]+)?\s+(\S+)\s+(.+)$",
    re.IGNORECASE,
)
_SYSTEM_KEY_ENV = re.compile(r"^REMAIL_BOT_[A-Z0-9_]+$")
_PUSH_TOPICS = (
    "project.launched",
    "leaderboard.settled",
    "system.notice.updated",
    "system.announcement.updated",
    "email.discount.updated",
    "project.price.updated",
)
_PUSH_EMAIL = re.compile(r"[^\s@]+@[^\s@]+")
_PUSH_DATABASE_URL = re.compile(
    r"\b(?:(?:mysql|postgres(?:ql)?|redis|mongodb(?:\+srv)?)://\S+|[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/@\s]+@\S+)",
    re.IGNORECASE,
)
_PUSH_CREDENTIAL = re.compile(
    r"(?im)^.*(?:\b(?:password|passwd|secret|authorization|cookie|dsn|access[_-]?token|refresh[_-]?token|api[_-]?key)|密码|密钥|令牌)\s*[:=：]\s*\S.*$"
)
_PUSH_SYSTEM_KEY = re.compile(r"\bsk_[a-z0-9_-]{8,}\b", re.IGNORECASE)
_PUSH_AUTHORIZATION = re.compile(
    r"\b(?:basic|bearer)\s+[a-z0-9._~+/=-]{8,}\b", re.IGNORECASE
)


def _safe_push_value(value: Any, limit: int = 1000) -> str:
    if isinstance(value, bool) or not isinstance(value, (str, int, float)):
        return ""
    text = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]", "", str(value)).strip()
    text = _PUSH_DATABASE_URL.sub("[敏感信息已隐藏]", text)
    text = _PUSH_CREDENTIAL.sub("[敏感信息已隐藏]", text)
    text = _PUSH_SYSTEM_KEY.sub("[敏感信息已隐藏]", text)
    text = _PUSH_AUTHORIZATION.sub("[敏感信息已隐藏]", text)
    text = _PUSH_EMAIL.sub("[邮箱已隐藏]", text)
    return text if len(text) <= limit else text[: limit - 1] + "…"


def _render_push_text(topic: str, payload: Any) -> str:
    """Render only the documented public DTO fields; never stringify a payload."""
    if not isinstance(payload, dict):
        payload = {}
    lines: list[str] = []
    if topic == "project.launched":
        project = (
            payload.get("project") if isinstance(payload.get("project"), dict) else {}
        )
        label = " ".join(
            filter(
                None,
                (
                    f"#{_safe_push_value(project.get('id'))}"
                    if _safe_push_value(project.get("id"))
                    else "",
                    _safe_push_value(project.get("name")),
                ),
            )
        )
        lines = [f"新项目上线：{label}" if label else "新项目上线"]
        if description := _safe_push_value(project.get("description")):
            lines.append(description)
    elif topic == "leaderboard.settled":
        business_date = _safe_push_value(payload.get("businessDate"))
        lines = [f"{business_date} 排行榜结算" if business_date else "排行榜结算"]
        if settled_at := _safe_push_value(payload.get("settledAt")):
            lines.append(f"结算时间：{settled_at}")
        items = payload.get("items") if isinstance(payload.get("items"), list) else []
        for item in items[:20]:
            if not isinstance(item, dict):
                continue
            rank = _safe_push_value(item.get("rank"))
            name = _safe_push_value(item.get("name"))
            count = _safe_push_value(item.get("successCount"))
            reward = _safe_push_value(item.get("rewardAmount"))
            lines.append(f"{rank}. {name} — {count} 单，奖励 {reward}".strip())
    elif topic == "system.notice.updated":
        lines = ["系统通知更新", _safe_push_value(payload.get("notice"))]
    elif topic == "system.announcement.updated":
        lines = ["系统公告更新"]
        announcements = (
            payload.get("announcements")
            if isinstance(payload.get("announcements"), list)
            else []
        )
        for item in announcements[:20]:
            if not isinstance(item, dict):
                continue
            title = _safe_push_value(item.get("title"), 300)
            content = _safe_push_value(item.get("content"))
            if title or content:
                lines.extend((f"公告：{title}" if title else "公告", content))
    elif topic == "email.discount.updated":
        lines = ["邮箱折扣更新", _safe_push_value(payload.get("message"))]
    elif topic == "project.price.updated":
        project_id = _safe_push_value(payload.get("projectId"))
        name = _safe_push_value(payload.get("name"))
        label = " ".join(filter(None, (f"#{project_id}" if project_id else "", name)))
        lines = [
            f"项目价格更新：{label}" if label else "项目价格更新",
            _safe_push_value(payload.get("message")),
        ]
    else:
        return ""
    rendered = "\n".join(line for line in lines if line)
    return rendered if len(rendered) <= 4000 else rendered[:3994] + "\n（已截断）"


class ReMailError(RuntimeError):
    def __init__(self, status: int, message: str, request_id: str = "") -> None:
        super().__init__(message)
        self.status = status
        self.message = message
        self.request_id = request_id


class _WebSocketUnavailable(RuntimeError):
    def __init__(self, *, sent: bool = False) -> None:
        super().__init__("WebSocket is unavailable")
        self.sent = sent


@dataclass
class _PendingRequest:
    key: str
    future: asyncio.Future
    state: str = "queued"


class Main(Star):
    def __init__(self, context: Context, config: AstrBotConfig) -> None:
        super().__init__(context)
        _install_binding_log_redaction()
        self.config = config
        self.request_timeout = max(
            1, min(int(config.get("request_timeout_seconds", 10)), 60)
        )
        base_url = validated_base_url(str(config.get("base_url", "")))
        self.client = httpx.AsyncClient(
            base_url=base_url,
            timeout=self.request_timeout,
            headers={"Accept": "application/json"},
        )
        self.websocket_tasks: list[asyncio.Task] = []
        self.websocket_connections: dict[str, Any] = {}
        self.websocket_ready: dict[str, asyncio.Event] = {}
        self.websocket_send_locks: dict[str, asyncio.Lock] = {}
        self.websocket_pending: dict[str, _PendingRequest] = {}
        self.websocket_pongs: dict[str, asyncio.Future] = {}
        self.launch_queue: asyncio.Queue = asyncio.Queue(maxsize=100)
        self.launch_worker: asyncio.Task | None = None
        self.launch_cursors: dict[str, tuple[datetime, int, str]] = {}
        self.launch_cursor_lock = asyncio.Lock()
        self.openapi_spec: dict[str, Any] | None = None
        self.openapi_cached_at = 0.0
        self.public_cache: dict[str, tuple[float, Any]] = {}

    async def initialize(self) -> None:
        destinations = self.config.get("launch_destinations", []) or []
        if self._websocket_enabled():
            if destinations:
                self.launch_worker = asyncio.create_task(self._project_launch_worker())
            self._start_websocket_connections(bool(destinations))

    def _websocket_enabled(self) -> bool:
        return (
            str(self.config.get("transport_mode", "websocket")).strip().lower()
            == "websocket"
        )

    @staticmethod
    def _environment_key(name: Any) -> str:
        variable = str(name or "").strip()
        return (
            str(os.getenv(variable, "")).strip()
            if _SYSTEM_KEY_ENV.fullmatch(variable)
            else ""
        )

    def _platform_key_environments(self) -> dict[str, str]:
        return {
            str(item.get("platform_id", "")).strip(): str(
                item.get("system_key_env", "")
            ).strip()
            for item in self.config.get("platform_system_keys", []) or []
            if isinstance(item, dict) and str(item.get("platform_id", "")).strip()
        }

    def _system_key(self, event: AstrMessageEvent) -> str:
        variable = self._platform_key_environments().get(
            str(event.get_platform_id()).strip(), ""
        )
        return self._environment_key(variable)

    def _service_key(self) -> str:
        key = self._environment_key(self.config.get("launch_system_key_env", ""))
        if key:
            return key
        for variable in self._platform_key_environments().values():
            if key := self._environment_key(variable):
                return key
        return ""

    def _all_system_keys(self) -> list[str]:
        values = [self._service_key()]
        values.extend(
            self._environment_key(variable)
            for variable in self._platform_key_environments().values()
        )
        return list(dict.fromkeys(key for key in values if key))

    @staticmethod
    def _scene(event: AstrMessageEvent) -> str:
        return (
            "private"
            if event.get_message_type() == MessageType.FRIEND_MESSAGE
            else "group"
        )

    def _bot_headers(
        self, event: AstrMessageEvent, *, require_subject: bool = True
    ) -> dict[str, str]:
        key = self._system_key(event)
        if not key:
            raise ReMailError(503, "机器人尚未配置 ReMail System Key。")
        scene = self._scene(event)
        headers = {
            "X-System-Key": key,
            "X-Bot-Scene": scene,
        }
        if scene == "group":
            group_id = str(event.get_group_id()).strip()
            if not group_id:
                raise ReMailError(401, "群聊来源鉴权失败。")
            headers["X-Bot-Group"] = group_id
        if require_subject:
            subject = str(event.get_sender_id()).strip()
            if not subject:
                raise ReMailError(400, "消息平台没有提供有效用户身份。")
            headers["X-Bot-Subject"] = subject
        return headers

    async def _request(
        self,
        method: str,
        path: str,
        *,
        event: AstrMessageEvent | None = None,
        body: dict[str, Any] | None = None,
        params: dict[str, Any] | None = None,
    ) -> Any:
        headers: dict[str, str] = {}
        key = ""
        subject = ""
        scene = ""
        group_id = ""
        if event is not None:
            headers.update(self._bot_headers(event))
            key = headers["X-System-Key"]
            subject = headers.get("X-Bot-Subject", "")
            scene = headers.get("X-Bot-Scene", "")
            group_id = headers.get("X-Bot-Group", "")
        if self._websocket_enabled() and key and path.startswith("/v1/bot/"):
            try:
                return await self._websocket_request(
                    key, method, path, subject, scene, group_id, body, params
                )
            except _WebSocketUnavailable as exc:
                if exc.sent:
                    raise ReMailError(
                        503, "ReMail WebSocket 响应丢失，请先查询状态再重试。"
                    ) from exc
        try:
            response = await self.client.request(
                method, path, json=body, params=params, headers=headers
            )
        except httpx.HTTPError as exc:
            raise ReMailError(503, "ReMail 服务暂时不可用。") from exc
        if response.status_code == 204:
            return None
        try:
            payload = response.json()
        except ValueError:
            payload = {}
        if response.is_error:
            message = str(
                payload.get("reason") or payload.get("message") or "ReMail 请求失败。"
            )
            raise ReMailError(
                response.status_code, message, str(payload.get("requestId") or "")
            )
        return payload

    async def _authorize_event(self, event: AstrMessageEvent) -> None:
        await self._request("GET", "/v1/bot/context", event=event)

    async def _public_request(self, path: str, ttl: int = 30) -> Any:
        cached = self.public_cache.get(path)
        if cached and cached[0] > monotonic():
            return cached[1]
        payload = await self._request("GET", path)
        self.public_cache[path] = (monotonic() + ttl, payload)
        return payload

    def _start_websocket_connections(self, subscribe_launches: bool) -> None:
        service_key = self._service_key()
        for key in self._all_system_keys():
            self.websocket_ready.setdefault(key, asyncio.Event())
            self.websocket_send_locks.setdefault(key, asyncio.Lock())
            self.websocket_tasks.append(
                asyncio.create_task(
                    self._run_websocket(key, subscribe_launches and key == service_key),
                )
            )

    async def _run_websocket(self, key: str, subscribe_launches: bool) -> None:
        reconnect_delay = 1
        while True:
            heartbeat: asyncio.Task | None = None
            reader: asyncio.Task | None = None
            connection: Any = None
            try:
                async with websockets.connect(
                    websocket_url(str(self.client.base_url)),
                    additional_headers={"X-System-Key": key},
                    open_timeout=self.request_timeout,
                    close_timeout=5,
                    max_size=4 << 20,
                    ping_interval=None,
                    logger=_WEBSOCKET_LOGGER,
                ) as connection:
                    self.websocket_connections[key] = connection
                    reconnect_delay = 1
                    reader = asyncio.create_task(self._read_websocket(key, connection))
                    heartbeat = asyncio.create_task(
                        self._websocket_heartbeat(key, connection)
                    )
                    self.websocket_ready[key].set()
                    if subscribe_launches:
                        after, after_id = await self._oldest_launch_cursor()
                        subscription = {
                            "type": "subscribe",
                            "id": uuid.uuid4().hex,
                            # Old servers read topic; new servers prefer topics.
                            "topic": "project.launched",
                            "topics": list(_PUSH_TOPICS),
                        }
                        if after:
                            subscription.update({"after": after, "afterId": after_id})
                        await self._send_websocket(key, subscription)
                    done, pending = await asyncio.wait(
                        {reader, heartbeat},
                        return_when=asyncio.FIRST_COMPLETED,
                    )
                    for task in pending:
                        task.cancel()
                    for task in done:
                        await task
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("ReMail WebSocket disconnected: %s", type(exc).__name__)
            finally:
                if heartbeat:
                    heartbeat.cancel()
                    with contextlib.suppress(asyncio.CancelledError):
                        await heartbeat
                if reader:
                    reader.cancel()
                    with contextlib.suppress(asyncio.CancelledError):
                        await reader
                if self.websocket_connections.get(key) is connection:
                    self.websocket_connections.pop(key, None)
                self.websocket_ready[key].clear()
                self._fail_websocket_waiters(key)
            await asyncio.sleep(reconnect_delay)
            reconnect_delay = min(reconnect_delay * 2, 30)

    async def _read_websocket(self, key: str, connection: Any) -> None:
        async for raw in connection:
            await self._handle_websocket_message(key, connection, raw)

    async def _websocket_heartbeat(self, key: str, connection: Any) -> None:
        while True:
            await asyncio.sleep(20)
            heartbeat_id = uuid.uuid4().hex
            future = asyncio.get_running_loop().create_future()
            self.websocket_pongs[heartbeat_id] = future
            try:
                await self._send_websocket(key, {"type": "ping", "id": heartbeat_id})
                await asyncio.wait_for(future, timeout=10)
            finally:
                self.websocket_pongs.pop(heartbeat_id, None)
            if self.websocket_connections.get(key) is not connection:
                return

    async def _send_websocket(
        self, key: str, payload: dict[str, Any], on_sending=None
    ) -> None:
        connection = self.websocket_connections.get(key)
        if connection is None:
            raise _WebSocketUnavailable()
        async with self.websocket_send_locks[key]:
            if self.websocket_connections.get(key) is not connection:
                raise _WebSocketUnavailable()
            if on_sending:
                on_sending()
            await connection.send(
                json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
            )

    async def _handle_websocket_message(
        self, key: str, connection: Any, raw: Any
    ) -> None:
        payload = json.loads(raw)
        if not isinstance(payload, dict):
            raise ReMailError(503, "ReMail WebSocket 协议错误。")
        frame_type = str(payload.get("type") or "")
        frame_id = str(payload.get("id") or "")
        if frame_type == "response":
            pending = self.websocket_pending.get(frame_id)
            if pending and pending.key == key and not pending.future.done():
                pending.future.set_result(payload)
            return
        if frame_type == "pong":
            future = self.websocket_pongs.get(frame_id)
            if future and not future.done():
                future.set_result(True)
            return
        topic = str(payload.get("topic") or payload.get("event") or "")
        if frame_type == "event" and topic in _PUSH_TOPICS:
            try:
                self.launch_queue.put_nowait((key, connection, payload))
            except asyncio.QueueFull as exc:
                raise ReMailError(503, "ReMail 主动推送队列已满。") from exc
            return
        if frame_type == "subscribed" and (
            topic == "project.launched"
            or "project.launched" in (payload.get("topics") or [])
        ):
            cursor = payload.get("cursor")
            if isinstance(cursor, dict):
                await self._initialize_launch_cursors(
                    str(cursor.get("after") or ""),
                    int(cursor.get("afterId") or 0),
                )
            return
        if frame_type == "error":
            error = ReMailError(
                int(payload.get("status") or 503),
                str(payload.get("message") or "ReMail WebSocket 请求失败。"),
            )
            pending = self.websocket_pending.get(frame_id)
            if pending and pending.key == key and not pending.future.done():
                pending.future.set_exception(error)
                return
            raise error
        if frame_type != "hello":
            raise ReMailError(503, "ReMail WebSocket 协议错误。")

    async def _project_launch_worker(self) -> None:
        while True:
            key, connection, payload = await self.launch_queue.get()
            try:
                if self.websocket_connections.get(key) is not connection:
                    continue
                await self._deliver_push_event(payload)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("ReMail push delivery failed: %s", type(exc).__name__)
                with contextlib.suppress(Exception):
                    await connection.close(code=1011, reason="push delivery failed")
                if self.websocket_connections.get(key) is connection:
                    self.websocket_connections.pop(key, None)
                    self.websocket_ready[key].clear()
            finally:
                self.launch_queue.task_done()

    def _fail_websocket_waiters(self, key: str) -> None:
        for frame_id, pending in list(self.websocket_pending.items()):
            if pending.key == key and not pending.future.done():
                pending.future.set_exception(
                    _WebSocketUnavailable(sent=pending.state != "queued")
                )
                self.websocket_pending.pop(frame_id, None)

    async def _websocket_request(
        self,
        key: str,
        method: str,
        path: str,
        subject: str,
        scene: str,
        group_id: str,
        body: dict[str, Any] | None,
        params: dict[str, Any] | None,
    ) -> Any:
        ready = self.websocket_ready.get(key)
        if ready is None:
            raise _WebSocketUnavailable()
        if not ready.is_set():
            try:
                await asyncio.wait_for(
                    ready.wait(), timeout=min(self.request_timeout, 2)
                )
            except asyncio.TimeoutError as exc:
                raise _WebSocketUnavailable() from exc
        frame_id = uuid.uuid4().hex
        future = asyncio.get_running_loop().create_future()
        pending = _PendingRequest(key=key, future=future)
        self.websocket_pending[frame_id] = pending
        frame = {
            "type": "request",
            "id": frame_id,
            "method": method.upper(),
            "path": path,
            "subject": subject,
            "scene": scene,
            "query": {name: str(value) for name, value in (params or {}).items()},
        }
        if group_id:
            frame["groupId"] = group_id
        if body is not None:
            frame["body"] = body
        try:
            await self._send_websocket(
                key, frame, lambda: setattr(pending, "state", "sending")
            )
            pending.state = "sent"
        except Exception as exc:
            self.websocket_pending.pop(frame_id, None)
            raise _WebSocketUnavailable(sent=pending.state != "queued") from exc
        try:
            response = await asyncio.wait_for(future, timeout=self.request_timeout)
        except asyncio.TimeoutError as exc:
            raise ReMailError(503, "ReMail WebSocket 请求超时。") from exc
        finally:
            self.websocket_pending.pop(frame_id, None)
        status = int(response.get("status") or 500)
        payload = response.get("body")
        if status == 204:
            return None
        if status >= 400:
            safe = payload if isinstance(payload, dict) else {}
            message = str(
                safe.get("reason") or safe.get("message") or "ReMail 请求失败。"
            )
            raise ReMailError(status, message, str(safe.get("requestId") or ""))
        return payload

    @staticmethod
    def _launch_cursor_key(destination: str) -> str:
        digest = hashlib.sha256(destination.encode("utf-8")).hexdigest()[:20]
        return f"launch_cursor_{digest}"

    async def _load_launch_cursors(self) -> None:
        async with self.launch_cursor_lock:
            for raw_destination in self.config.get("launch_destinations", []) or []:
                destination = str(raw_destination)
                if destination in self.launch_cursors:
                    continue
                stored = await self.get_kv_data(
                    self._launch_cursor_key(destination), {}
                )
                after = (
                    str(stored.get("after") or "") if isinstance(stored, dict) else ""
                )
                valid = False
                try:
                    after_id = (
                        int(stored.get("afterId") or 0)
                        if isinstance(stored, dict)
                        else 0
                    )
                    parsed = (
                        datetime.fromisoformat(after.replace("Z", "+00:00"))
                        if after
                        else datetime.min.replace(tzinfo=timezone.utc)
                    )
                    if parsed.tzinfo is None or after_id < 0:
                        raise ValueError
                    valid = bool(after)
                except (TypeError, ValueError):
                    parsed, after_id = datetime.min.replace(tzinfo=timezone.utc), 0
                canonical = (
                    parsed.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
                    if valid
                    else ""
                )
                self.launch_cursors[destination] = (parsed, after_id, canonical)
                if after and not valid:
                    await self.put_kv_data(
                        self._launch_cursor_key(destination),
                        {"after": "", "afterId": 0},
                    )

    async def _oldest_launch_cursor(self) -> tuple[str, int]:
        await self._load_launch_cursors()
        if not self.launch_cursors:
            now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
            return now, 0
        _, after_id, after = min(
            self.launch_cursors.values(), key=lambda cursor: (cursor[0], cursor[1])
        )
        return after, after_id

    async def _initialize_launch_cursors(self, after: str, after_id: int) -> None:
        try:
            parsed = datetime.fromisoformat(after.replace("Z", "+00:00"))
            if parsed.tzinfo is None or after_id < 0:
                raise ValueError
        except (TypeError, ValueError) as exc:
            raise ReMailError(503, "ReMail 项目订阅游标错误。") from exc
        parsed = parsed.astimezone(timezone.utc)
        canonical = parsed.isoformat().replace("+00:00", "Z")
        await self._load_launch_cursors()
        for destination, current in list(self.launch_cursors.items()):
            if current[2]:
                continue
            await self.put_kv_data(
                self._launch_cursor_key(destination),
                {"after": canonical, "afterId": after_id},
            )
            self.launch_cursors[destination] = (parsed, after_id, canonical)

    @staticmethod
    def _result_text(payload: Any, fallback: str) -> str:
        if not isinstance(payload, dict):
            return fallback
        lines = [str(payload.get("reason") or fallback)]
        if action := str(payload.get("action") or "").strip():
            lines.append(action)
        if request_id := str(payload.get("requestId") or "").strip():
            lines.append(f"排障编号：{request_id}")
        return "\n".join(lines)

    @staticmethod
    def _private(event: AstrMessageEvent) -> bool:
        return event.get_message_type() == MessageType.FRIEND_MESSAGE

    @filter.command("绑定", alias={"bind"}, priority=sys.maxsize - 1)
    async def bind(self, event: AstrMessageEvent):
        """绑定当前消息平台身份到 ReMail 账号。"""
        if not self._private(event):
            try:
                await self._authorize_event(event)
                text = "绑定只允许在私聊中执行。"
            except ReMailError as exc:
                text = exc.message
        else:
            match = _BIND_ARGUMENTS.search(event.get_message_str().strip())
            if not match:
                text = "格式：/绑定 ReMail邮箱 密码"
            else:
                email, password = match.group(1), match.group(2)
                try:
                    payload = await self._request(
                        "POST",
                        "/v1/bot/bindings",
                        event=event,
                        body={"email": email, "password": password},
                    )
                    text = self._result_text(payload, "绑定成功。")
                except ReMailError as exc:
                    text = exc.message
        try:
            await event.send(MessageChain([Plain(text)]))
        finally:
            event.stop_event()

    @filter.command("绑定状态")
    async def binding_status(self, event: AstrMessageEvent):
        """查询当前消息平台身份的 ReMail 绑定状态。"""
        if not self._private(event):
            yield event.plain_result("绑定状态只允许在私聊中查询。")
            return
        try:
            payload = await self._request("GET", "/v1/bot/binding", event=event)
            text = self._result_text(payload, "尚未绑定 ReMail。")
            if isinstance(payload, dict) and payload.get("accountDisplay"):
                text += f"\n账号：{payload['accountDisplay']}"
            yield event.plain_result(text)
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("解绑")
    async def unbind(self, event: AstrMessageEvent):
        """解绑当前消息平台身份。"""
        if not self._private(event):
            yield event.plain_result("解绑只允许在私聊中执行。")
            return
        try:
            await self._request("DELETE", "/v1/bot/binding", event=event)
            yield event.plain_result("解绑成功。")
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("查码")
    async def diagnose_code(self, event: AstrMessageEvent, email: str, project_id: int):
        """诊断指定邮箱和项目为什么没有收到验证码。"""
        if not self._private(event):
            yield event.plain_result("订单诊断只允许在私聊中执行。")
            return
        try:
            payload = await self._request(
                "POST",
                "/v1/bot/diagnoses/code",
                event=event,
                body={"email": email, "projectId": project_id},
            )
            yield event.plain_result(
                self._result_text(payload, "暂时无法判断未收到验证码的原因。")
            )
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("项目")
    async def projects(self, event: AstrMessageEvent, search: str = ""):
        """查询 ReMail 工作台项目、价格和库存。"""
        try:
            params = {"scope": "visible", "limit": 20}
            if search:
                params["search"] = search
            payload = await self._request(
                "GET", "/v1/bot/projects", event=event, params=params
            )
            yield event.plain_result(self._format_projects(payload))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("库存")
    async def inventory(self, event: AstrMessageEvent, project_id: int):
        """查询 ReMail 项目实时库存。"""
        try:
            payload = await self._request(
                "GET", f"/v1/bot/projects/{project_id}/inventory", event=event
            )
            yield event.plain_result(self._format_inventory(payload))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("排行榜")
    async def rankings(self, event: AstrMessageEvent):
        """查询今日和历史成功订单排行榜。"""
        try:
            payload = await self._request(
                "GET", "/v1/bot/rankings/orders", event=event, params={"limit": 10}
            )
            yield event.plain_result(self._format_rankings(payload))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("排行榜奖励")
    async def ranking_rewards(self, event: AstrMessageEvent):
        """查询上一次排行榜奖励清单。"""
        try:
            payload = await self._request(
                "GET", "/v1/bot/rankings/rewards/latest", event=event
            )
            yield event.plain_result(self._format_rewards(payload))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("接口文档")
    async def docs(self, event: AstrMessageEvent):
        """返回 ReMail API 文档地址。"""
        try:
            await self._authorize_event(event)
            yield event.plain_result(
                str(self.config.get("docs_url", "")).strip()
                or f"{self.client.base_url}/docs"
            )
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("公告")
    async def announcements(self, event: AstrMessageEvent):
        """查询 ReMail 当前系统通知和公告。"""
        try:
            await self._authorize_event(event)
            notice, announcements = await asyncio.gather(
                self._public_request("/v1/notice"),
                self._public_request("/v1/announcements"),
            )
            yield event.plain_result(self._format_announcements(notice, announcements))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.command("常见问题")
    async def faqs(self, event: AstrMessageEvent):
        """查询 ReMail 发布的常见问题。"""
        try:
            await self._authorize_event(event)
            payload = await self._public_request("/v1/faqs")
            yield event.plain_result(self._format_faqs(payload))
        except ReMailError as exc:
            yield event.plain_result(exc.message)

    @filter.llm_tool(name="remail_projects")
    async def remail_projects(self, event: AstrMessageEvent, search: str = "") -> str:
        """查询 ReMail 工作台中的项目、价格、时效和库存。

        Args:
            search(string): 可选项目名称或目标平台关键词。
        """
        params = {"scope": "visible", "limit": 20}
        if search:
            params["search"] = search
        payload = await self._request(
            "GET", "/v1/bot/projects", event=event, params=params
        )
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_project_inventory")
    async def remail_project_inventory(
        self, event: AstrMessageEvent, project_id: int
    ) -> str:
        """查询 ReMail 项目的当前库存。

        Args:
            project_id(number): 从 ReMail 项目列表取得的项目 ID。
        """
        payload = await self._request(
            "GET", f"/v1/bot/projects/{project_id}/inventory", event=event
        )
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_code_diagnosis")
    async def remail_code_diagnosis(
        self, event: AstrMessageEvent, email: str, project_id: int
    ) -> str:
        """诊断绑定用户的指定邮箱和项目为什么没有收到验证码；只能在私聊使用。

        Args:
            email(string): 用户提供的订单交付邮箱。
            project_id(number): 从 ReMail 项目列表取得的项目 ID。
        """
        if not self._private(event):
            return json.dumps(
                {"result": "private_required", "reason": "账号诊断只能在私聊中执行。"},
                ensure_ascii=False,
            )
        payload = await self._request(
            "POST",
            "/v1/bot/diagnoses/code",
            event=event,
            body={"email": email, "projectId": project_id},
        )
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_faqs")
    async def remail_faqs(self, event: AstrMessageEvent) -> str:
        """获取 ReMail 发布的常见问题，用于回答接码、购买、邮箱有效期等产品问题。"""
        await self._authorize_event(event)
        payload = await self._public_request("/v1/faqs")
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_announcements")
    async def remail_announcements(self, event: AstrMessageEvent) -> str:
        """获取 ReMail 当前系统通知和公告。"""
        await self._authorize_event(event)
        notice, announcements = await asyncio.gather(
            self._public_request("/v1/notice"),
            self._public_request("/v1/announcements"),
        )
        return json.dumps(
            {"notice": notice, "announcements": announcements}, ensure_ascii=False
        )

    @filter.llm_tool(name="remail_order_rankings")
    async def remail_order_rankings(self, event: AstrMessageEvent) -> str:
        """获取 ReMail 今日和历史成功订单排行榜。"""
        payload = await self._request(
            "GET", "/v1/bot/rankings/orders", event=event, params={"limit": 10}
        )
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_latest_ranking_rewards")
    async def remail_latest_ranking_rewards(self, event: AstrMessageEvent) -> str:
        """获取 ReMail 上一次已结算的排行榜奖励清单。"""
        payload = await self._request(
            "GET", "/v1/bot/rankings/rewards/latest", event=event
        )
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_binding_status")
    async def remail_binding_status(self, event: AstrMessageEvent) -> str:
        """在私聊中查询当前消息平台用户的 ReMail 绑定状态。"""
        if not self._private(event):
            return json.dumps(
                {"result": "private_required", "reason": "绑定状态只能在私聊中查询。"},
                ensure_ascii=False,
            )
        payload = await self._request("GET", "/v1/bot/binding", event=event)
        return json.dumps(payload, ensure_ascii=False)

    @filter.llm_tool(name="remail_api_documentation")
    async def remail_api_documentation(
        self, event: AstrMessageEvent, query: str
    ) -> str:
        """查询 ReMail 公共 API 文档中的路径、参数、请求体、响应和相关 schema。

        Args:
            query(string): 用户的 API 对接问题或接口路径。
        """
        await self._authorize_event(event)
        url = (
            str(self.config.get("docs_url", "")).strip()
            or f"{self.client.base_url}/docs"
        )
        if self.openapi_spec is None or monotonic() - self.openapi_cached_at >= 300:
            payload = await self._request("GET", "/openapi.json")
            self.openapi_spec = payload if isinstance(payload, dict) else {}
            self.openapi_cached_at = monotonic()
        excerpt = self._openapi_excerpt(self.openapi_spec, query)
        excerpt["documentationUrl"] = url
        encoded = json.dumps(excerpt, ensure_ascii=False)
        if len(encoded) > 12000:
            excerpt["components"] = {}
            excerpt["truncated"] = True
            encoded = json.dumps(excerpt, ensure_ascii=False)
        if len(encoded) > 12000:
            excerpt["operations"] = excerpt["operations"][:3]
            encoded = json.dumps(excerpt, ensure_ascii=False)
        return encoded

    @staticmethod
    def _openapi_excerpt(spec: dict[str, Any], query: str) -> dict[str, Any]:
        tokens = re.findall(r"[a-z0-9_./{}-]+|[\u4e00-\u9fff]", query.casefold())
        ranked: list[tuple[int, dict[str, Any]]] = []
        for path, operations in (spec.get("paths") or {}).items():
            if not isinstance(operations, dict):
                continue
            for method, operation in operations.items():
                if method.lower() not in {
                    "get",
                    "post",
                    "put",
                    "patch",
                    "delete",
                } or not isinstance(operation, dict):
                    continue
                haystack = json.dumps(
                    {"path": path, **operation}, ensure_ascii=False
                ).casefold()
                score = sum(
                    3 if token in str(path).casefold() else 1
                    for token in tokens
                    if token in haystack
                )
                if score > 0:
                    ranked.append(
                        (
                            score,
                            {
                                "method": method.upper(),
                                "path": path,
                                "summary": operation.get("summary"),
                                "description": operation.get("description"),
                                "security": operation.get("security"),
                                "parameters": operation.get("parameters"),
                                "requestBody": operation.get("requestBody"),
                                "responses": operation.get("responses"),
                            },
                        )
                    )
        operations = [item for _, item in sorted(ranked, key=lambda item: -item[0])[:6]]
        source_components = (
            spec.get("components", {})
            if isinstance(spec.get("components"), dict)
            else {}
        )
        referenced: dict[str, dict[str, Any]] = {}
        pending = re.findall(
            r"#/components/(schemas|parameters|responses|requestBodies)/([A-Za-z0-9_.-]+)",
            json.dumps(operations),
        )
        while pending and sum(len(values) for values in referenced.values()) < 20:
            section, name = pending.pop(0)
            target = referenced.setdefault(section, {})
            source = source_components.get(section, {})
            if name in target or not isinstance(source, dict) or name not in source:
                continue
            target[name] = source[name]
            pending.extend(
                re.findall(
                    r"#/components/(schemas|parameters|responses|requestBodies)/([A-Za-z0-9_.-]+)",
                    json.dumps(source[name]),
                )
            )
        security_schemes = source_components.get("securitySchemes", {})
        if isinstance(security_schemes, dict):
            names = {
                name
                for operation in operations
                for requirement in operation.get("security") or []
                for name in requirement
            }
            selected = {
                name: security_schemes[name]
                for name in names
                if name in security_schemes
            }
            if selected:
                referenced["securitySchemes"] = selected
        return {"operations": operations, "components": referenced}

    @staticmethod
    def _format_projects(payload: Any) -> str:
        items = payload.get("items", []) if isinstance(payload, dict) else []
        if not items:
            return "没有找到可用项目。"
        lines: list[str] = []
        for project in items[:20]:
            products = project.get("products", []) or []
            summaries = []
            for product in products:
                modes = []
                if product.get("status") == "enabled" and product.get("codeEnabled"):
                    modes.append(
                        f"接码 {product.get('effectiveCodePrice') or product.get('codePrice')}"
                    )
                if product.get("status") == "enabled" and product.get(
                    "purchaseEnabled"
                ):
                    modes.append(
                        f"购买 {product.get('effectivePurchasePrice') or product.get('purchasePrice')}"
                    )
                summaries.append(
                    f"{product.get('type')} {' / '.join(modes) if modes else '暂未开放'} / 库存 {product.get('publicAvailable', 0)}"
                )
            lines.append(
                f"#{project.get('id')} {project.get('name')}：" + "；".join(summaries)
            )
        return "\n".join(lines)

    @staticmethod
    def _format_inventory(payload: Any) -> str:
        if not isinstance(payload, dict):
            return "暂时无法读取库存。"
        lines = [
            f"项目 #{payload.get('projectId')} 总库存：{payload.get('totalAvailable', 0)}"
        ]
        for product in payload.get("products", []) or []:
            lines.append(
                f"{product.get('productType')}：总 {product.get('totalAvailable', 0)}，公共 {product.get('publicAvailable', 0)}"
            )
        return "\n".join(lines)

    @staticmethod
    def _format_rankings(payload: Any) -> str:
        if not isinstance(payload, dict):
            return "暂时无法读取排行榜。"
        lines = [f"今日成功榜（{payload.get('businessDate', '')}）"]
        for item in payload.get("today", []) or []:
            lines.append(
                f"{item.get('rank')}. {item.get('name')} — {item.get('successCount')} 单"
            )
        lines.append("历史成功榜")
        for item in payload.get("historical", []) or []:
            lines.append(
                f"{item.get('rank')}. {item.get('name')} — {item.get('successCount')} 单"
            )
        return "\n".join(lines)

    @staticmethod
    def _format_rewards(payload: Any) -> str:
        if not isinstance(payload, dict) or not payload.get("available"):
            return "暂无已结算的排行榜奖励。"
        lines = [f"{payload.get('businessDate')} 排行榜奖励"]
        for item in payload.get("items", []) or []:
            lines.append(
                f"{item.get('rank')}. {item.get('name')} — {item.get('successCount')} 单，奖励 {item.get('rewardAmount')}"
            )
        return "\n".join(lines)

    @staticmethod
    def _format_announcements(notice: Any, payload: Any) -> str:
        lines: list[str] = []
        if isinstance(notice, dict) and str(notice.get("notice") or "").strip():
            lines.extend(["系统通知", str(notice["notice"]).strip()])
        for item in (
            payload.get("announcements", []) if isinstance(payload, dict) else []
        ):
            lines.extend(
                [
                    f"公告：{item.get('title', '')}",
                    str(item.get("content") or "").strip(),
                ]
            )
        return Main._clip("\n".join(filter(None, lines)) or "暂无系统通知或公告。")

    @staticmethod
    def _format_faqs(payload: Any) -> str:
        items = (
            payload.get("items", [])
            if isinstance(payload, dict) and payload.get("enabled", True)
            else []
        )
        lines = [
            f"问：{item.get('question', '')}\n答：{item.get('answer', '')}"
            for item in items
        ]
        return Main._clip("\n\n".join(lines) or "暂无常见问题。")

    @staticmethod
    def _clip(value: str, limit: int = 4000) -> str:
        return value if len(value) <= limit else value[: limit - 6] + "\n（已截断）"

    async def _deliver_push_event(self, payload: dict[str, Any]) -> None:
        topic = str(payload.get("topic") or payload.get("event") or "")
        data = payload.get("data")
        cursor = payload.get("cursor")
        if (
            topic not in _PUSH_TOPICS
            or not isinstance(data, dict)
            or not isinstance(cursor, dict)
        ):
            raise ReMailError(503, "ReMail 主动推送格式错误。")
        raw_after_id = cursor.get("afterId")
        if isinstance(raw_after_id, bool):
            raise ReMailError(503, "ReMail 主动推送游标错误。")
        if isinstance(raw_after_id, int):
            after_id = raw_after_id
        elif isinstance(raw_after_id, str) and raw_after_id.isdecimal():
            after_id = int(raw_after_id)
        else:
            raise ReMailError(503, "ReMail 主动推送游标错误。")
        await self._deliver_push_to_destinations(
            topic,
            data,
            str(cursor.get("after") or ""),
            after_id,
        )

    async def _deliver_push_to_destinations(
        self,
        topic: str,
        data: dict[str, Any],
        after: str,
        after_id: int,
    ) -> None:
        try:
            parsed = datetime.fromisoformat(after.replace("Z", "+00:00"))
            if parsed.tzinfo is None or after_id <= 0:
                raise ValueError
        except (TypeError, ValueError) as exc:
            raise ReMailError(503, "ReMail 主动推送游标错误。") from exc
        parsed = parsed.astimezone(timezone.utc)
        canonical = parsed.isoformat().replace("+00:00", "Z")
        await self._load_launch_cursors()
        text = _render_push_text(topic, data)
        if not text:
            raise ReMailError(503, "ReMail 主动推送内容错误。")
        message = MessageChain([Plain(text)])
        failures = 0
        for raw_destination in self.config.get("launch_destinations", []) or []:
            destination = str(raw_destination)
            current = self.launch_cursors.get(destination)
            if current and (parsed, after_id) <= (current[0], current[1]):
                continue
            try:
                sent = await self.context.send_message(destination, message)
                if not sent:
                    raise ReMailError(503, "AstrBot 未找到项目通知目标。")
                await self.put_kv_data(
                    self._launch_cursor_key(destination),
                    {"after": canonical, "afterId": after_id},
                )
                self.launch_cursors[destination] = (parsed, after_id, canonical)
            except Exception:
                failures += 1
        if failures:
            raise ReMailError(503, f"{failures} 个主动推送目标发送失败。")

    async def terminate(self) -> None:
        for task in self.websocket_tasks:
            task.cancel()
        for task in self.websocket_tasks:
            with contextlib.suppress(asyncio.CancelledError):
                await task
        if self.launch_worker:
            self.launch_worker.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self.launch_worker
        for pending in self.websocket_pending.values():
            if not pending.future.done():
                pending.future.cancel()
        await self.client.aclose()
