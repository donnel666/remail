"""Complete workflow recordings, separate from the model's conversation history."""

from __future__ import annotations

import asyncio
import base64
import inspect
import json
import math
import os
import re
import sqlite3
import threading
import traceback
from concurrent.futures import Future, ThreadPoolExecutor
from contextvars import ContextVar
from dataclasses import fields, is_dataclass
from datetime import datetime, timezone
from enum import Enum
from functools import wraps
from pathlib import Path
from time import monotonic
from uuid import uuid4

from .sessions import (
    SESSION_REFERENCE_KEY,
    _owner_umo,
    _valid_native_origin,
    reset_target_from_record,
    session_request_matches,
)


TRACE_KEY = "_remail_diagnostic_trace"
LLM_TIMEOUT_TEXT = "本次处理耗时较长，请稍后重试。"


class LLMDeadlineExceeded(TimeoutError):
    def __init__(self, stage, seconds):
        self.stage = stage
        self.seconds = max(0, seconds)
        super().__init__(f"模型等待超时：{stage}，本次等待上限 {self.seconds:.1f} 秒。")


def llm_time_budget(event, stage):
    """A per-event budget independent of diagnostics and shared provider settings."""
    deadline = event.get_extra("_remail_llm_deadline", None)
    limit = event.get_extra("_remail_llm_timeout", None)
    if not isinstance(deadline, (int, float)) or not isinstance(limit, (int, float)):
        return None
    remaining = min(limit, deadline - monotonic())
    if remaining <= 0:
        event.set_extra("_remail_llm_timeout_stage", stage)
        raise LLMDeadlineExceeded(stage, 0)
    return remaining


MAX_TURNS = 200
MAX_PUSHES = 500
STAGES = frozenset(
    "entry session intent background planner agent react tool api evidence privacy "
    "writer critic delivery end".split()
)
OUTCOMES = frozenset(
    "started completed accepted rejected failed cancelled fallback blocked ready sent "
    "suppressed chunk capture_unavailable running interrupted skipped partial "
    "not_needed not_applicable".split()
)
_CURRENT_ACTION: ContextVar[tuple | None] = ContextVar(
    "remail_diagnostic_action", default=None
)
_LLM_STAGES = {"intent", "planner", "writer", "critic", "react"}
_SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    trace_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    qq TEXT NOT NULL,
    time TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT 'running',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    complete INTEGER NOT NULL DEFAULT 0,
    capture_complete INTEGER NOT NULL DEFAULT 1,
    recording_error TEXT NOT NULL DEFAULT '',
    event_count INTEGER NOT NULL DEFAULT 0,
    question TEXT NOT NULL DEFAULT '',
    answer TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS runs_session_time ON runs(session_id, time DESC);
CREATE INDEX IF NOT EXISTS runs_qq_time ON runs(qq, time DESC);
CREATE TABLE IF NOT EXISTS events (
    trace_id TEXT NOT NULL REFERENCES runs(trace_id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    time TEXT NOT NULL,
    elapsed_ms INTEGER NOT NULL,
    stage TEXT NOT NULL,
    outcome TEXT NOT NULL,
    details TEXT NOT NULL,
    PRIMARY KEY(trace_id, seq)
);
CREATE TABLE IF NOT EXISTS push_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    time TEXT NOT NULL,
    topic TEXT NOT NULL,
    destination TEXT NOT NULL,
    outcome TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    cursor_after TEXT NOT NULL DEFAULT '',
    cursor_after_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS push_deliveries_time ON push_deliveries(time DESC, id DESC);
"""


def snapshot_response(value):
    """Copy actual values before callers mutate them; report unsupported objects."""

    def copy(item, seen):
        if item is None or isinstance(item, (str, bool, int)):
            return item
        if isinstance(item, float):
            return (
                item if math.isfinite(item) else {"type": "float", "value": str(item)}
            )
        kind = f"{type(item).__module__}.{type(item).__qualname__}"
        if id(item) in seen:
            return {"serializationError": "Circular reference", "type": kind}
        seen = seen | {id(item)}
        try:
            if isinstance(item, dict):
                return {str(key): copy(val, seen) for key, val in item.items()}
            if isinstance(item, (list, tuple)):
                return [copy(val, seen) for val in item]
            if isinstance(item, (bytes, bytearray)):
                return {"type": kind, "base64": base64.b64encode(item).decode("ascii")}
            if isinstance(item, Enum):
                return copy(item.value, seen)
            if isinstance(item, (datetime, Path)):
                return item.isoformat() if isinstance(item, datetime) else str(item)
            if isinstance(item, asyncio.Event):
                return {"type": "asyncio.Event", "isSet": item.is_set()}
            if callable(getattr(item, "get_extra", None)) or callable(
                getattr(item, "llm_generate", None)
            ):
                return {
                    "serializationError": "Runtime event/context is not a payload",
                    "type": kind,
                }
            async_serializers = []
            for name, options in (
                ("openai_schema", {}),
                ("model_dump", {"mode": "json"}),
                ("dict", {}),
                ("to_dict", {}),
                ("toDict", {}),
            ):
                # Pydantic v1 preserves fields that component toDict() omits.
                if name == "dict" and not hasattr(item, "__fields__"):
                    continue
                serializer = getattr(item, name, None)
                if not callable(serializer):
                    continue
                if any(
                    inspect.iscoroutinefunction(target)
                    or inspect.isasyncgenfunction(target)
                    for target in (serializer, getattr(serializer, "__call__", None))
                ):
                    async_serializers.append(name)
                    continue
                serialized = serializer(**options)
                if inspect.isawaitable(serialized) or inspect.isasyncgen(serialized):
                    if inspect.iscoroutine(serialized):
                        serialized.close()
                    return {
                        "serializationError": f"{name} returned an asynchronous value",
                        "type": kind,
                    }
                return copy(serialized, seen)
            if hasattr(item, "completion_text"):
                names = (
                    "role",
                    "completion_text",
                    "result_chain",
                    "tools_call_args",
                    "tools_call_name",
                    "tools_call_ids",
                    "tools_call_extra_content",
                    "reasoning_content",
                    "reasoning_signature",
                    "raw_completion",
                    "usage",
                    "id",
                    "is_chunk",
                )
                return {
                    name: copy(getattr(item, name), seen)
                    for name in names
                    if hasattr(item, name)
                }
            if is_dataclass(item) and not isinstance(item, type):
                return {
                    field.name: copy(getattr(item, field.name), seen)
                    for field in fields(item)
                }
            if type(item).__name__ == "SimpleNamespace":
                return copy(vars(item), seen)
            if hasattr(item, "chain"):
                return {"chain": copy(item.chain, seen)}
            return {
                "serializationError": (
                    "Only asynchronous serializers are available: "
                    + ", ".join(async_serializers)
                    if async_serializers
                    else "Unsupported payload type"
                ),
                "type": kind,
            }
        except Exception as exc:
            return {"serializationError": f"{type(exc).__name__}: {exc}", "type": kind}

    try:
        return copy(value, set())
    except Exception as exc:
        return {
            "serializationError": f"{type(exc).__name__}: {exc}",
            "type": type(value).__name__,
        }


def _json(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


class DiagnosticLog:
    """Append events transactionally; retain whole finished turns, never fragments."""

    def __init__(self, directory: Path | None, *, enabled=True, capture_text=None):
        self.enabled = enabled
        self.capture_text = (
            True  # Compatibility: recordings always contain original text.
        )
        self.closed = False
        self.storage_ok = False
        self.recording_error = ""
        self.path = None
        self._persistent_requested = directory is not None
        self._disk_connected = False
        self._lock = threading.RLock()
        self._submission_lock = threading.Lock()
        self._writer = None
        self._close_future = None
        self._db = None
        try:
            if directory is not None and enabled:
                directory = Path(directory)
                directory.mkdir(mode=0o700, parents=True, exist_ok=True)
                if directory.is_symlink():
                    raise OSError("Diagnostic directory is a symbolic link")
                os.chmod(directory, 0o700)
                self.path = directory / "fae-diagnostics.sqlite3"
                if self.path.is_symlink():
                    raise OSError("Diagnostic database is a symbolic link")
                self.path.touch(mode=0o600, exist_ok=True)
                os.chmod(self.path, 0o600)
            self._db = self._connect(self.path if enabled else None)
            self._db.executescript(_SCHEMA)
            self.storage_ok = bool(enabled and self.path)
            self._disk_connected = self.storage_ok
            if enabled and not self.storage_ok:
                self.recording_error = (
                    "Recordings are in memory only; no storage directory is available."
                )
        except Exception as exc:
            self._fallback(exc)
        with self._db:
            # A process restart cannot prove the previous operation finished.
            self._db.execute(
                "UPDATE runs SET outcome='interrupted' WHERE complete=0 AND outcome='running'"
            )
            self._migrate_session_ids()
            self._prune()
        if enabled and self.path:
            # Snapshot on the caller before mutation; serialize disk operations off
            # the event loop. Reads/deletes/close join the same FIFO, never drop data.
            # ponytail: one FIFO writer; add async backpressure if sustained logging exceeds disk throughput.
            self._writer = ThreadPoolExecutor(
                max_workers=1, thread_name_prefix="remail-log"
            )

    @staticmethod
    def _connect(path):
        connection = sqlite3.connect(
            str(path) if path else ":memory:", timeout=1, check_same_thread=False
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys=ON")
        if path:
            connection.execute("PRAGMA journal_mode=WAL")
        return connection

    def _fallback(self, exc):
        replacement = self._connect(None)
        if self._db is not None:
            try:
                self._db.backup(replacement)
            except Exception:
                pass
            try:
                self._db.close()
            except Exception:
                pass
        self._db = replacement
        self._db.executescript(_SCHEMA)
        self._disk_connected = False
        self.storage_ok = False
        self.recording_error = f"Persistence failed; recordings are in memory only. {type(exc).__name__}: {exc}"

    def _prune(self):
        self._db.execute(
            "DELETE FROM runs WHERE trace_id IN (SELECT trace_id FROM runs "
            "WHERE complete=1 OR outcome='interrupted' ORDER BY updated_at DESC, rowid DESC LIMIT -1 OFFSET ?)",
            (MAX_TURNS,),
        )
        self._db.execute(
            "DELETE FROM push_deliveries WHERE id NOT IN ("
            "SELECT id FROM push_deliveries ORDER BY time DESC, id DESC LIMIT ?)",
            (MAX_PUSHES,),
        )

    def _migrate_session_ids(self):
        """Group legacy diagnostic cards by QQ without changing their raw turns."""
        rows = self._db.execute(
            "SELECT trace_id,session_id,metadata FROM runs"
        ).fetchall()
        for row in rows:
            try:
                metadata = json.loads(row["metadata"])
                scope = tuple(metadata["scope"])
                origin = metadata["nativeOrigin"]
                if not _valid_native_origin(scope, origin):
                    continue
                session_id, session_ref = _owner_umo(scope, origin)
                if row["session_id"] == session_id:
                    continue
                metadata.setdefault("nativeSessionId", row["session_id"])
                metadata.setdefault("nativeSessionRef", metadata.get("sessionRef", ""))
                metadata["sessionId"] = session_id
                metadata["sessionRef"] = session_ref
                self._db.execute(
                    "UPDATE runs SET session_id=?,metadata=? WHERE trace_id=?",
                    (session_id, _json(metadata), row["trace_id"]),
                )
            except (KeyError, TypeError, ValueError):
                continue

    def record_push(
        self,
        topic: str,
        destination: str,
        outcome: str,
        text: str,
        *,
        after: str = "",
        after_id: int | str = "",
        error: str = "",
    ) -> None:
        """Record one public push attempt without attaching it to a user turn."""
        if not self.enabled or self.closed:
            return
        try:
            values = (
                datetime.now(timezone.utc).isoformat(),
                str(topic)[:128],
                str(destination)[:2048],
                str(outcome)[:64],
                str(text)[:8000],
                str(after)[:128],
                str(after_id)[:128],
                str(error)[:2000],
            )
            with self._submission_lock:
                if self.closed:
                    return
                if self._writer is not None:
                    self._writer.submit(self._record_push_snapshot, values)
                    return
                self._record_push_snapshot(values)
        except Exception as exc:
            self.recording_error = f"Push recording failed: {type(exc).__name__}: {exc}"
            self.storage_ok = False

    def _record_push_snapshot(self, values):
        try:
            with self._lock:
                try:
                    self._append_push(values)
                except Exception as exc:
                    self._fallback(exc)
                    self._append_push(values)
        except Exception as exc:
            self.recording_error = f"Push recording failed: {type(exc).__name__}: {exc}"
            self.storage_ok = False

    def _append_push(self, values):
        with self._db:
            self._db.execute(
                "INSERT INTO push_deliveries("
                "time,topic,destination,outcome,text,cursor_after,cursor_after_id,error) "
                "VALUES (?,?,?,?,?,?,?,?)",
                values,
            )
            self._db.execute(
                "DELETE FROM push_deliveries WHERE id NOT IN ("
                "SELECT id FROM push_deliveries ORDER BY time DESC, id DESC LIMIT ?)",
                (MAX_PUSHES,),
            )

    def attach(self, event) -> bool:
        if not self.enabled or self.closed or event.get_extra(TRACE_KEY, None):
            return False
        try:

            def identity(method):
                function = getattr(event, method, None)
                return str(function() or "") if callable(function) else ""

            scope = tuple(
                identity(method)
                for method in (
                    "get_platform_id",
                    "get_platform_name",
                    "get_self_id",
                    "get_group_id",
                    "get_sender_id",
                )
            )
            message_type = (
                getattr(event.get_message_type(), "value", "")
                if callable(getattr(event, "get_message_type", None))
                else ""
            )
            message_type = message_type or (
                "GroupMessage" if scope[3] else "FriendMessage"
            )
            native_origin = str(getattr(event, "unified_msg_origin", "") or "")
            session_id, session_ref = _owner_umo(scope, native_origin)
            message_obj = getattr(event, "message_obj", None)
            metadata = {
                "sessionId": session_id,
                "qq": scope[4],
                "senderId": scope[4],
                "platformId": scope[0],
                "platform": scope[1],
                "botId": scope[2],
                "groupId": scope[3],
                "messageType": message_type,
                "messageId": str(getattr(message_obj, "message_id", "") or ""),
                "nativeOrigin": native_origin,
                "sessionRef": session_ref,
                "scope": list(scope),
            }
            trace_id = uuid4().hex
            event.set_extra(TRACE_KEY, (self, trace_id, monotonic()))
            self.record(
                trace_id,
                0,
                "entry",
                "ready",
                {
                    "name": "receive_message",
                    "session": metadata,
                    "question": str(getattr(event, "message_str", "") or ""),
                    "input": {
                        "message": getattr(event, "message_str", ""),
                        "messageChain": getattr(message_obj, "message", None),
                        "rawMessage": getattr(message_obj, "raw_message", None),
                    },
                },
                _register=True,
            )
            return True
        except Exception as exc:
            self.recording_error = (
                f"Could not attach workflow recording: {type(exc).__name__}: {exc}"
            )
            return False

    def record(self, trace_id, elapsed_ms, stage, outcome, details, *, _register=False):
        if not self.enabled or self.closed:
            return
        try:
            details = snapshot_response(details)
            raw = _json(details)
            now = datetime.now(timezone.utc).isoformat()
            with self._submission_lock:
                if self.closed:
                    return
                if self._writer is not None:
                    self._writer.submit(
                        self._record_snapshot,
                        trace_id,
                        elapsed_ms,
                        stage,
                        outcome,
                        details,
                        raw,
                        now,
                        _register,
                    )
                    return
                self._record_snapshot(
                    trace_id, elapsed_ms, stage, outcome, details, raw, now, _register
                )
        except Exception as exc:
            self.recording_error = f"Recording failed: {type(exc).__name__}: {exc}"
            self.storage_ok = False

    def _record_snapshot(
        self, trace_id, elapsed_ms, stage, outcome, details, raw, now, register
    ):
        try:
            with self._lock:
                try:
                    self._append(
                        trace_id,
                        elapsed_ms,
                        stage,
                        outcome,
                        details,
                        raw,
                        now,
                        register,
                    )
                except Exception as exc:
                    self._fallback(exc)
                    self._append(
                        trace_id,
                        elapsed_ms,
                        stage,
                        outcome,
                        details,
                        raw,
                        now,
                        register,
                    )
        except Exception as exc:
            self.recording_error = f"Recording failed: {type(exc).__name__}: {exc}"
            self.storage_ok = False

    def _append(
        self, trace_id, elapsed_ms, stage, outcome, details, raw, now, register=False
    ):
        with self._db:
            run = self._db.execute(
                "SELECT * FROM runs WHERE trace_id=?", (trace_id,)
            ).fetchone()
            if run is None:
                # Only attach registers a turn. Late callbacks cannot recreate cleared runs.
                if not register:
                    return
                metadata = details.get("session", {})
                metadata = metadata if isinstance(metadata, dict) else {}
                question = details.get("question", "")
                question = question if isinstance(question, str) else _json(question)
                self._db.execute(
                    "INSERT INTO runs(trace_id,session_id,qq,time,updated_at,question,metadata) VALUES (?,?,?,?,?,?,?)",
                    (
                        trace_id,
                        metadata.get("sessionId", ""),
                        metadata.get("qq", ""),
                        now,
                        now,
                        question,
                        _json(metadata),
                    ),
                )
                run = self._db.execute(
                    "SELECT * FROM runs WHERE trace_id=?", (trace_id,)
                ).fetchone()
            if stage == "end" and run["complete"]:
                return
            seq = run["event_count"] + 1
            self._db.execute(
                "INSERT INTO events VALUES (?,?,?,?,?,?,?)",
                (trace_id, seq, now, elapsed_ms, stage, outcome, raw),
            )
            metadata = json.loads(run["metadata"])
            for name in ("conversationId", "sessionRef"):
                if name in details:
                    metadata[name] = details[name]
            answer = run["answer"]
            if stage in {"delivery", "end"}:
                candidate = details.get("answer", details.get("text"))
                if isinstance(candidate, str):
                    answer = candidate
            capture_error = (
                outcome == "capture_unavailable"
                or details.get("reason") == "capture_unavailable"
                or details.get("captureComplete") is False
                or '"serializationError":' in raw
            )
            recording_error = run["recording_error"]
            if capture_error:
                recording_error = recording_error or str(
                    details.get("error")
                    or details.get("reason")
                    or "Some original values could not be captured; inspect the marked event."
                )
            self._db.execute(
                "UPDATE runs SET updated_at=?,duration_ms=?,event_count=?,outcome=?,complete=?,answer=?,metadata=?,capture_complete=?,recording_error=? WHERE trace_id=?",
                (
                    now,
                    max(elapsed_ms, run["duration_ms"]),
                    seq,
                    outcome if stage == "end" else run["outcome"],
                    int(stage == "end" or run["complete"]),
                    answer,
                    _json(metadata),
                    int(bool(run["capture_complete"]) and not capture_error),
                    recording_error,
                    trace_id,
                ),
            )
            if stage == "end":
                self._prune()

    @staticmethod
    def _event_row(row):
        return {
            "traceId": row["trace_id"],
            "seq": row["seq"],
            "time": row["time"],
            "elapsedMs": row["elapsed_ms"],
            "stage": row["stage"],
            "outcome": row["outcome"],
            "details": json.loads(row["details"]),
            "detailsRaw": row["details"],
        }

    @staticmethod
    def _turn(row):
        return {
            **json.loads(row["metadata"]),
            "traceId": row["trace_id"],
            "sessionId": row["session_id"],
            "qq": row["qq"],
            "time": row["time"],
            "updatedAt": row["updated_at"],
            "outcome": row["outcome"],
            "question": row["question"],
            "answer": row["answer"],
            "durationMs": row["duration_ms"],
            "eventCount": row["event_count"],
            "complete": bool(row["complete"]),
            "captureComplete": bool(row["capture_complete"]),
            "recordingError": row["recording_error"],
        }

    @property
    def entries(self):
        if self.closed:
            return []
        try:
            return self._on_writer(self._entries)
        except RuntimeError:
            if self.closed:
                return []
            raise

    def _on_writer(self, operation, *args, **kwargs):
        with self._submission_lock:
            if self.closed:
                raise RuntimeError("诊断日志已关闭。")
            writer = self._writer
            if writer is None:
                return operation(*args, **kwargs)
            pending = writer.submit(operation, *args, **kwargs)
        return pending.result()

    def _entries(self):
        with self._lock:
            return [
                self._event_row(row)
                for row in self._db.execute(
                    "SELECT * FROM events ORDER BY time, trace_id, seq"
                )
            ]

    def snapshot(self, **kwargs):
        return self._on_writer(self._snapshot, **kwargs)

    def _snapshot(
        self,
        *,
        view="events",
        qq="",
        session_id="",
        trace_id="",
        limit=30,
        offset=0,
        outcome="",
    ):
        if view not in {"sessions", "turns", "trace", "export", "events", "pushes"}:
            raise ValueError("invalid view")
        if (
            type(limit) is not int
            or not 1 <= limit <= 200
            or type(offset) is not int
            or offset < 0
        ):
            raise ValueError("invalid pagination")
        if not isinstance(trace_id, str) or (
            trace_id and not re.fullmatch(r"[a-f0-9]{32}", trace_id)
        ):
            raise ValueError("invalid trace")
        if (
            not isinstance(session_id, str)
            or len(session_id) > 2048
            or not isinstance(qq, str)
            or len(qq) > 128
        ):
            raise ValueError("invalid session")
        if not isinstance(outcome, str) or len(outcome) > 128:
            raise ValueError("invalid outcome")
        if (view == "trace" and not trace_id) or (
            view == "export" and not (trace_id or session_id)
        ):
            raise ValueError("a trace or session is required")
        base = {
            "enabled": self.enabled,
            "captureText": True,
            "storageAvailable": self.storage_ok,
            "recordingError": self.recording_error,
            "storageMode": "sqlite" if self.storage_ok else "memory",
            "retentionTurns": MAX_TURNS,
            "retentionPushes": MAX_PUSHES,
        }
        clauses, parameters = [], []
        for column, value in (
            ("qq", qq),
            ("session_id", session_id),
            ("trace_id", trace_id),
        ):
            if value:
                clauses.append(f"r.{column}=?")
                parameters.append(value)
        if outcome:
            clauses.append("e.outcome=?" if view == "events" else "r.outcome=?")
            parameters.append(outcome)
        where = " WHERE " + " AND ".join(clauses) if clauses else ""
        with self._lock:
            if view == "pushes":
                total = self._db.execute(
                    "SELECT count(*) FROM push_deliveries"
                ).fetchone()[0]
                rows = self._db.execute(
                    "SELECT id,time,topic,destination,outcome,text,cursor_after,cursor_after_id,error "
                    "FROM push_deliveries ORDER BY time DESC,id DESC LIMIT ? OFFSET ?",
                    (limit, offset),
                )
                items = [
                    {
                        "id": row["id"],
                        "time": row["time"],
                        "topic": row["topic"],
                        "destination": row["destination"],
                        "outcome": row["outcome"],
                        "text": row["text"],
                        "after": row["cursor_after"],
                        "afterId": row["cursor_after_id"],
                        "error": row["error"],
                    }
                    for row in rows
                ]
                return {
                    **base,
                    "items": items,
                    "total": total,
                    "offset": offset,
                    "limit": limit,
                    "truncated": offset + len(items) < total,
                }
            if view == "sessions":
                totals = self._db.execute(
                    "SELECT COUNT(DISTINCT session_id),COUNT(*) FROM runs"
                ).fetchone()
                base.update(totalSessions=totals[0], totalTurns=totals[1])
            if view in {"trace", "export"}:
                runs = self._db.execute(
                    "SELECT r.* FROM runs r"
                    + where
                    + " ORDER BY r.time DESC,r.rowid DESC",
                    parameters,
                ).fetchall()
                turns = []
                for run in runs:
                    events = [
                        self._event_row(row)
                        for row in self._db.execute(
                            "SELECT * FROM events WHERE trace_id=? ORDER BY seq",
                            (run["trace_id"],),
                        )
                    ]
                    turns.append({**self._turn(run), "events": events})
                if view == "trace":
                    turn = turns[0] if turns else None
                    events = turn.pop("events") if turn else []
                    return {
                        **base,
                        "turn": turn,
                        "items": events,
                        "total": len(events),
                        "truncated": False,
                    }
                result = {
                    **base,
                    "sessionId": session_id or (turns[0]["sessionId"] if turns else ""),
                    "turns": turns,
                }
                result["rawExport"] = _json(result)
                return result
            if view == "events":
                source = " FROM events e JOIN runs r ON r.trace_id=e.trace_id" + where
                total = self._db.execute(
                    "SELECT count(*)" + source, parameters
                ).fetchone()[0]
                rows = self._db.execute(
                    "SELECT e.*"
                    + source
                    + " ORDER BY e.time DESC,r.rowid DESC,e.seq DESC LIMIT ? OFFSET ?",
                    (*parameters, limit, offset),
                )
                items = [self._event_row(row) for row in rows]
            elif view == "turns":
                source = " FROM runs r" + where
                total = self._db.execute(
                    "SELECT count(*)" + source, parameters
                ).fetchone()[0]
                rows = self._db.execute(
                    "SELECT r.*"
                    + source
                    + " ORDER BY r.time DESC,r.rowid DESC LIMIT ? OFFSET ?",
                    (*parameters, limit, offset),
                )
                items = [self._turn(row) for row in rows]
            else:
                source = " FROM runs r" + where + " GROUP BY r.session_id"
                total = self._db.execute(
                    "SELECT count(*) FROM (SELECT r.session_id" + source + ")",
                    parameters,
                ).fetchone()[0]
                rows = self._db.execute(
                    "SELECT r.session_id,MAX(r.updated_at) last_time,COUNT(*) turn_count,MIN(r.capture_complete) capture_complete"
                    + source
                    + " ORDER BY last_time DESC,r.session_id LIMIT ? OFFSET ?",
                    (*parameters, limit, offset),
                ).fetchall()
                items = []
                for row in rows:
                    latest = self._db.execute(
                        "SELECT metadata FROM runs WHERE session_id=? ORDER BY updated_at DESC,rowid DESC LIMIT 1",
                        (row["session_id"],),
                    ).fetchone()
                    items.append(
                        {
                            **json.loads(latest["metadata"]),
                            "sessionId": row["session_id"],
                            "lastTime": row["last_time"],
                            "turnCount": row["turn_count"],
                            "captureComplete": bool(row["capture_complete"]),
                        }
                    )
            return {
                **base,
                "items": items,
                "total": total,
                "offset": offset,
                "limit": limit,
                "truncated": offset + len(items) < total,
            }

    def reset_targets(
        self, *, session_id: str | None = None, all_sessions: bool = False
    ):
        """Read trusted reset targets; the API accepts selectors, never owner/CID data."""
        return self._on_writer(
            self._reset_targets, session_id=session_id, all_sessions=all_sessions
        )

    def _reset_targets(
        self, *, session_id: str | None = None, all_sessions: bool = False
    ):
        if type(all_sessions) is not bool:
            raise ValueError("all_sessions must be a boolean")
        if session_id is not None and (
            not isinstance(session_id, str)
            or not session_id.strip()
            or len(session_id) > 2048
        ):
            raise ValueError("a non-empty session_id is required")
        if (session_id is not None) == all_sessions:
            raise ValueError("select exactly one session or all sessions")
        where = " WHERE session_id=?" if session_id is not None else ""
        parameters = (session_id,) if session_id is not None else ()
        with self._lock:
            rows = self._db.execute(
                "SELECT session_id,metadata FROM runs"
                + where
                + " ORDER BY updated_at DESC,rowid DESC",
                parameters,
            ).fetchall()
            targets, seen = [], set()
            for row in rows:
                metadata = json.loads(row["metadata"])
                if not isinstance(metadata, dict):
                    raise ValueError("invalid recorded session metadata")
                if not metadata.get("conversationId"):
                    continue
                target = reset_target_from_record(row["session_id"], metadata)
                if target is None:
                    raise ValueError("recorded session ownership could not be verified")
                key = (target.session_id, target.native_origin, target.conversation_id)
                if key not in seen:
                    targets.append(target)
                    seen.add(key)
            return tuple(targets)

    def clear(self, *, session_id: str | None = None, all_sessions: bool = False):
        return self._on_writer(
            self._clear, session_id=session_id, all_sessions=all_sessions
        )

    def _clear(self, *, session_id: str | None = None, all_sessions: bool = False):
        """Delete only the explicitly selected diagnostic records, including active turns."""
        if type(all_sessions) is not bool:
            raise ValueError("all_sessions must be a boolean")
        if session_id is not None and (
            not isinstance(session_id, str)
            or not session_id.strip()
            or len(session_id) > 2048
        ):
            raise ValueError("a non-empty session_id is required")
        if (session_id is not None) == all_sessions:
            raise ValueError("select exactly one session or all sessions")
        with self._lock:
            if not self.enabled:
                raise RuntimeError(
                    "请先启用会话调试记录并重载插件，再清理已保存的记录。"
                )
            if (
                self._persistent_requested or self.path is not None
            ) and not self._disk_connected:
                raise RuntimeError(
                    "日志存储不可用，本次未清理记录。请检查数据目录权限和剩余空间，重载插件后重试。"
                )
            where = " WHERE session_id=?" if session_id is not None else ""
            parameters = (session_id,) if session_id is not None else ()
            # Count and delete from the same database transaction; failures must reach the API.
            with self._db:
                self._db.execute("BEGIN IMMEDIATE")
                sessions, turns = self._db.execute(
                    "SELECT COUNT(DISTINCT session_id),COUNT(*) FROM runs" + where,
                    parameters,
                ).fetchone()
                events = self._db.execute(
                    "SELECT COUNT(*) FROM events WHERE trace_id IN "
                    "(SELECT trace_id FROM runs" + where + ")",
                    parameters,
                ).fetchone()[0]
                self._db.execute("DELETE FROM runs" + where, parameters)
                push_count = 0
                if all_sessions:
                    push_count = self._db.execute(
                        "SELECT count(*) FROM push_deliveries"
                    ).fetchone()[0]
                    self._db.execute("DELETE FROM push_deliveries")
            result = {
                "deletedSessions": sessions,
                "deletedTurns": turns,
                "deletedEvents": events,
            }
            if push_count:
                result["deletedPushes"] = push_count
            return result

    def close(self):
        close_here = False
        with self._submission_lock:
            writer = self._writer
            if self._close_future is None:
                self.closed = True
                if writer is not None:
                    self._close_future = writer.submit(self._close_db)
                else:
                    self._close_future = Future()
                    close_here = True
            pending = self._close_future
        if close_here:
            try:
                self._close_db()
            except Exception as exc:
                pending.set_exception(exc)
            else:
                pending.set_result(None)
        try:
            pending.result()
        finally:
            if writer is not None:
                writer.shutdown(wait=True)

    def _close_db(self):
        with self._lock:
            self._db.close()


def trace_note(event, stage, outcome, **details):
    state = None
    try:
        current = _CURRENT_ACTION.get()
        if event is None and current:
            event = current[0]
        state = getattr(event, "get_extra", lambda *_: None)(TRACE_KEY, None)
        if (
            not isinstance(state, tuple)
            or len(state) != 3
            or not isinstance(state[0], DiagnosticLog)
        ):
            return
        if "actionId" not in details:
            action = (
                current[1]
                if current and current[0] is event and current[1]["stage"] == stage
                else None
            )
            if action is None and stage in _LLM_STAGES:
                action = event.get_extra("_remail_diagnostic_last_actions", {}).get(
                    stage
                )
            if action:
                details = {
                    **{key: value for key, value in action.items() if key != "stage"},
                    **details,
                }
            elif current and current[0] is event:
                details.setdefault("parentId", current[1]["actionId"])
        session_ref = event.get_extra("_remail_diagnostic_session_ref", None)
        if session_ref:
            details.setdefault("sessionRef", session_ref)
        state[0].record(
            state[1],
            max(0, int((monotonic() - state[2]) * 1000)),
            stage,
            outcome,
            details,
        )
    except Exception as exc:
        if isinstance(state, tuple) and state and isinstance(state[0], DiagnosticLog):
            state[0].recording_error = f"Recording failed: {type(exc).__name__}: {exc}"


def trace_finish(event, outcome, **details):
    """Finish once; later observations may still supplement the same recording."""
    trace_note(event, "end", outcome, **details)


async def logged_operation(event, stage, name, inputs, awaitable):
    """Record an asynchronous operation without replacing its result or exception."""
    try:
        current = _CURRENT_ACTION.get()
        if event is None and current:
            event = current[0]
        state = getattr(event, "get_extra", lambda *_: None)(TRACE_KEY, None)
        enabled = (
            isinstance(state, tuple)
            and len(state) == 3
            and isinstance(state[0], DiagnosticLog)
            and state[0].enabled
            and not state[0].closed
        )
        action = None
        if enabled:
            attempts = getattr(event, "get_extra", lambda *_: {})(
                "_remail_diagnostic_attempts", {}
            )
            attempt_key = (stage, name)
            attempts[attempt_key] = attempts.get(attempt_key, 0) + 1
            if callable(getattr(event, "set_extra", None)):
                event.set_extra("_remail_diagnostic_attempts", attempts)
            action = {
                "stage": stage,
                "name": name,
                "actionId": uuid4().hex,
                "parentId": current[1]["actionId"]
                if current and current[0] is event
                else "",
                "attempt": attempts[attempt_key],
            }
            if stage == "react" and not action["parentId"]:
                action["parentId"] = event.get_extra("_remail_agent_action_id", "")
            if stage == "tool":
                calls = getattr(event, "get_extra", lambda *_: [])(
                    "_remail_react_tool_calls", []
                )
                consumed = getattr(event, "get_extra", lambda *_: set())(
                    "_remail_diagnostic_used_tool_calls", set()
                )
                active = event.get_extra("_remail_active_tool_call", None)
                if isinstance(active, dict) and active.get("name") == name:
                    calls = [active]
                elif event.get_extra("_remail_native_tool_observer", False) is True:
                    calls = []
                matching = [
                    call
                    for call in calls
                    if isinstance(call, dict)
                    and call.get("name") == name
                    and isinstance(call.get("id"), str)
                    and call["id"]
                    and isinstance(call.get("actionId"), str)
                    and call["actionId"]
                    and (call.get("actionId"), call.get("id")) not in consumed
                ]
                # Only the native call in progress or one unambiguous candidate owns an ID.
                call = matching[0] if len(matching) == 1 else None
                if call:
                    action.update(
                        toolCallId=call.get("id", ""),
                        parentId=call.get("actionId", action["parentId"]),
                    )
                    consumed.add((call.get("actionId"), call.get("id")))
                    event.set_extra("_remail_diagnostic_used_tool_calls", consumed)
                elif callable(getattr(event, "get_extra", None)):
                    action["toolCallIdUnavailable"] = True
                    action["parentId"] = (
                        event.get_extra("_remail_react_action_id", "")
                        or action["parentId"]
                    )
                action["tool"] = name
            if stage in _LLM_STAGES and callable(getattr(event, "get_extra", None)):
                previous = event.get_extra("_remail_diagnostic_last_actions", {})
                previous[stage] = action
                event.set_extra("_remail_diagnostic_last_actions", previous)
    except Exception as exc:
        action = None
        trace_note(
            event,
            stage,
            "capture_unavailable",
            actionId="",
            reason="capture_unavailable",
            error=f"{type(exc).__name__}: {exc}",
        )
    if action is None:
        return await awaitable
    token = _CURRENT_ACTION.set((event, action))
    started = monotonic()
    trace_note(event, stage, "started", input=inputs)
    try:
        result = await awaitable
    except (Exception, asyncio.CancelledError) as exc:
        trace_note(
            event,
            stage,
            "cancelled" if isinstance(exc, asyncio.CancelledError) else "failed",
            durationMs=int((monotonic() - started) * 1000),
            errorType=type(exc).__name__,
            error=str(exc),
            traceback="".join(traceback.format_exception(exc)),
            exception=snapshot_response(vars(exc)),
        )
        raise
    else:
        failed = False
        if stage == "tool" and isinstance(result, str):
            try:
                payload = json.loads(result)
                failed = isinstance(payload, dict) and (
                    payload.get("ok") is False or payload.get("sourceValid") is False
                )
            except Exception:
                pass
        trace_note(
            event,
            stage,
            "failed" if failed else "completed",
            durationMs=int((monotonic() - started) * 1000),
            output=result,
        )
        return result
    finally:
        _CURRENT_ACTION.reset(token)


async def logged_llm_call(context, event, stage, *, operation_name=None, **kwargs):
    reference = event.get_extra(SESSION_REFERENCE_KEY, None)
    if reference is not None and not session_request_matches(
        event,
        event.get_extra("provider_request", None),
        scope=getattr(reference, "scope", ()),
    ):
        trace_note(event, "session", "blocked", reason="session_mismatch")
        raise ValueError("session boundary changed")

    async def generate():
        timeout = llm_time_budget(event, stage)
        if timeout is None:
            return await context.llm_generate(**kwargs)
        try:
            return await asyncio.wait_for(
                context.llm_generate(**kwargs), timeout=timeout
            )
        except asyncio.TimeoutError as exc:
            event.set_extra("_remail_llm_timeout_stage", stage)
            raise LLMDeadlineExceeded(stage, timeout) from exc

    return await logged_operation(
        event, stage, operation_name or stage, kwargs, generate()
    )


def trace_tool(function):
    signature = inspect.signature(function)

    @wraps(function)
    async def wrapped(self, event, *args, **kwargs):
        reference = event.get_extra(SESSION_REFERENCE_KEY, None)
        scope = getattr(reference, "scope", ())
        triggered = (
            event.get_extra("_remail_owned", False) is True
            and event.get_extra("_remail_main_agent_ready", False) is True
            and isinstance(scope, tuple)
            and len(scope) == 5
            and (
                not scope[3]
                or event.get_extra("_remail_group_trigger_verified", False) is True
            )
        )
        if not triggered or not session_request_matches(
            event, event.get_extra("provider_request", None), scope=scope
        ):
            trace_note(
                event,
                "tool",
                "blocked",
                tool=function.__name__,
                reason="session_mismatch" if triggered else "scope_refused",
            )
            return json.dumps(
                {
                    "ok": False,
                    "message": "当前无法安全完成本次查询，请重新发起询问。",
                },
                ensure_ascii=False,
            )
        try:
            bound = signature.bind(self, event, *args, **kwargs)
            bound.apply_defaults()
            inputs = {
                key: value
                for key, value in bound.arguments.items()
                if key not in {"self", "event"}
            }
        except TypeError:
            inputs = {"args": args, "kwargs": kwargs}
        return await logged_operation(
            event,
            "tool",
            function.__name__,
            inputs,
            function(self, event, *args, **kwargs),
        )

    return wrapped


def trace_api(function):
    signature = inspect.signature(function)

    @wraps(function)
    async def wrapped(self, *args, **kwargs):
        try:
            bound = signature.bind(self, *args, **kwargs)
            bound.apply_defaults()
            inputs = {
                key: value
                for key, value in bound.arguments.items()
                if key not in {"self", "event"}
            }
            event = bound.arguments.get("event")
        except TypeError:
            inputs, event = {"args": args, "kwargs": kwargs}, kwargs.get("event")
        name = (
            f"{inputs.get('method', '')} {inputs.get('path', '')}".strip()
            or function.__name__
        )
        return await logged_operation(
            event, "api", name, inputs, function(self, *args, **kwargs)
        )

    return wrapped
