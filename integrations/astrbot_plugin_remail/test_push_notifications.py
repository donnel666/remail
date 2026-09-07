"""Offline check of settlement frames, persistent group cursors and replay."""

import asyncio
import contextlib
import hashlib
import json
import logging
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .diagnostics import DiagnosticLog
from .test_entry import _load
from .test_security import _load_push_renderer


def test_settlement_push_uses_server_cursor_and_replays_only_failed_group(caplog):
    caplog.set_level(logging.INFO, logger=__name__)
    methods = {
        "_handle_websocket_message",
        "_deliver_push_event",
        "_deliver_push_to_destinations",
        "_launch_cursor_key",
        "_load_launch_cursors",
        "_oldest_launch_cursor",
        "_initialize_launch_cursors",
    }
    namespace = {
        "asyncio": asyncio,
        "contextlib": contextlib,
        "datetime": datetime,
        "timezone": timezone,
        "hashlib": hashlib,
        "json": json,
        "logger": logging.getLogger(__name__),
        "MessageChain": list,
        "Plain": str,
        "_safe_egress_text": lambda text, **kwargs: text,
        "_render_push_text": _load_push_renderer(),
    }
    _load(
        Path(__file__).with_name("main.py"),
        methods | {"ReMailError", "_PUSH_TOPICS"},
        namespace,
    )
    plugin_type = type("PushPlugin", (), {name: namespace[name] for name in methods})
    plugin_type._launch_cursor_key = staticmethod(namespace["_launch_cursor_key"])

    async def run():
        first, second = "qq:GroupMessage:20001", "qq:GroupMessage:20002"
        failed = {second}
        saved, sent = {}, []
        diagnostics = DiagnosticLog(None)

        async def send_message(destination, message):
            if destination in failed:
                return False
            sent.append((destination, "".join(message)))
            return True

        def plugin():
            result = plugin_type()
            result.config = {"launch_destinations": [first, second]}
            result.context = SimpleNamespace(send_message=send_message)
            result.launch_cursors = {}
            result.launch_cursor_lock = asyncio.Lock()
            result.launch_queue = asyncio.Queue()
            result.diagnostics = diagnostics
            result.get_kv_data = AsyncMock(
                side_effect=lambda key, default: saved.get(key, default)
            )
            result.put_kv_data = AsyncMock(side_effect=saved.__setitem__)
            return result

        active = plugin()
        # The server, not the AstrBot machine's potentially skewed clock, sets the baseline.
        assert await active._oldest_launch_cursor() == ("", 0)
        server_after = "2026-09-06T00:00:00Z"
        await active._handle_websocket_message(
            "test-channel",
            None,
            json.dumps(
                {
                    "type": "subscribed",
                    "topics": namespace["_PUSH_TOPICS"],
                    "cursor": {"after": server_after, "afterId": "0"},
                }
            ),
        )
        assert await active._oldest_launch_cursor() == (server_after, 0)

        after, after_id = "2026-09-06T00:01:00Z", 2**63 + 10
        frame = {
            "type": "event",
            "topic": "leaderboard.settled",
            "cursor": {"after": after, "afterId": str(after_id)},
            "data": {
                "businessDate": "2026-09-05",
                "settledAt": after,
                "items": [
                    {
                        "rank": 1,
                        "name": "测试用户",
                        "successCount": 9,
                        "rewardAmount": "500.00",
                    }
                ],
            },
        }
        await active._handle_websocket_message("test-channel", None, json.dumps(frame))
        _, _, queued = active.launch_queue.get_nowait()
        with pytest.raises(namespace["ReMailError"]):
            await active._deliver_push_event(queued)
        assert len(sent) == 1 and sent[0][0] == first
        assert "2026-09-05 排行榜奖励已结算" in sent[0][1]
        assert "测试用户 — 9 单，奖励 500.00 积分" in sent[0][1]
        assert saved[active._launch_cursor_key(first)] == {
            "after": after,
            "afterId": after_id,
        }
        assert saved[active._launch_cursor_key(second)] == {
            "after": server_after,
            "afterId": 0,
        }
        assert f"topic=leaderboard.settled destination={second}" in caplog.text
        assert "未推进游标" in caplog.text
        attempts = diagnostics.snapshot(view="pushes")["items"]
        assert [(item["destination"], item["outcome"]) for item in attempts] == [
            (second, "failed"),
            (first, "sent"),
        ]
        assert all("排行榜奖励已结算" in item["text"] for item in attempts)

        # Reload persisted state as a reconnect/restart would; don't resend to the first group.
        recovered = plugin()
        assert await recovered._oldest_launch_cursor() == (server_after, 0)
        failed.clear()
        await recovered._deliver_push_event(frame)
        await recovered._deliver_push_event(frame)
        assert [destination for destination, _ in sent] == [first, second]
        assert await recovered._oldest_launch_cursor() == (after, after_id)
        await recovered._handle_websocket_message(
            "test-channel",
            None,
            json.dumps(
                {
                    "type": "subscribed",
                    "topic": "project.launched",
                    "cursor": {"after": after, "afterId": str(after_id)},
                }
            ),
        )
        assert "后端未确认排行榜奖励订阅" in caplog.text
        attempts = diagnostics.snapshot(view="pushes")["items"]
        assert [(item["destination"], item["outcome"]) for item in attempts[:4]] == [
            (second, "skipped"),
            (first, "skipped"),
            (second, "sent"),
            (first, "skipped"),
        ]

        partial = plugin()
        partial.config = {"launch_destinations": ["qq:GroupMessage:20003"]}
        partial.put_kv_data = AsyncMock(side_effect=RuntimeError("disk unavailable"))
        with pytest.raises(namespace["ReMailError"]):
            await partial._deliver_push_event(frame)
        latest = diagnostics.snapshot(view="pushes")["items"][0]
        assert latest["destination"] == "qq:GroupMessage:20003"
        assert latest["outcome"] == "partial"
        assert latest["error"] == "RuntimeError: disk unavailable"
        diagnostics.close()

    asyncio.run(run())
