"""Offline checks for original values, complete recordings and native page access."""

import ast
import asyncio
import hashlib
import inspect
import json
import os
import re
import sqlite3
import sys
import threading
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType, SimpleNamespace
from unittest.mock import AsyncMock
from urllib.parse import quote

import pytest

from . import diagnostics
from .diagnostics import (
    TRACE_KEY,
    DiagnosticLog,
    logged_llm_call,
    logged_operation,
    snapshot_response,
    trace_api,
    trace_finish,
    trace_note,
    trace_tool,
)
from .sessions import NativeSessionReference, SESSION_REFERENCE_KEY, _owner_umo


def _event(
    *, qq="123456789", group="987654321", platform="qq-one", question="原始问题？\n"
):
    extras = {}
    return SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
        get_platform_id=lambda: platform,
        get_platform_name=lambda: "aiocqhttp",
        get_self_id=lambda: "90001",
        get_group_id=lambda: group,
        get_sender_id=lambda: qq,
        get_message_type=lambda: SimpleNamespace(
            value="GroupMessage" if group else "FriendMessage"
        ),
        unified_msg_origin=f"{platform}:{'GroupMessage' if group else 'FriendMessage'}:{group or qq}",
        message_str=question,
        message_obj=SimpleNamespace(
            message_id="900719925474099312345",
            message=[{"type": "text", "text": question}],
            raw_message={"raw": question},
        ),
    )


def _trace(log, event):
    return log.snapshot(view="trace", trace_id=event.get_extra(TRACE_KEY)[1])


def _tool_event(**kwargs):
    event = _event(**kwargs)
    scope = tuple(
        getattr(event, method)()
        for method in (
            "get_platform_id",
            "get_platform_name",
            "get_self_id",
            "get_group_id",
            "get_sender_id",
        )
    )
    event.unified_msg_origin = (
        f"{scope[0]}:{event.get_message_type().value}:{scope[3] or scope[4]}"
    )
    owner, session_ref = _owner_umo(scope, event.unified_msg_origin)
    conversation = SimpleNamespace(
        cid="native-cid", user_id=owner, platform_id=scope[0]
    )
    request = SimpleNamespace(conversation=conversation, session_id=owner)
    event.set_extra(
        SESSION_REFERENCE_KEY,
        NativeSessionReference(
            scope=scope,
            native_umo=event.unified_msg_origin,
            owner_umo=owner,
            cid=conversation.cid,
            conversation=conversation,
            session_ref=session_ref,
            request=request,
        ),
    )
    event.set_extra("provider_request", request)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_main_agent_ready", True)
    event.set_extra("_remail_group_trigger_verified", True)
    return event


def _ready_reset_record(log, event, cid="recorded-native-cid"):
    assert log.attach(event)
    reference = event.get_extra(SESSION_REFERENCE_KEY)
    trace_note(
        event, "session", "ready",
        conversationId=cid, sessionRef=reference.session_ref,
    )
    return _trace(log, event)["turn"]["sessionId"]


def test_reset_targets_use_verified_record_metadata_and_exact_selectors(tmp_path):
    log = DiagnosticLog(tmp_path)
    first = _tool_event(group="")
    duplicate = _tool_event(group="")
    other = _tool_event(qq="555555555", group="")
    try:
        session_id = _ready_reset_record(log, first)
        _ready_reset_record(log, duplicate)
        _ready_reset_record(log, other, "another-native-cid")
        before = log.entries
        selected = log.reset_targets(session_id=session_id)
        assert len(selected) == 1
        assert selected[0].scope == first.get_extra(SESSION_REFERENCE_KEY).scope
        assert selected[0].native_origin == first.unified_msg_origin
        assert selected[0].conversation_id == "recorded-native-cid"
        assert len(log.reset_targets(all_sessions=True)) == 2
        assert log.reset_targets(session_id="nonexistent-session") == ()
        assert log.entries == before, "reading targets cannot clear diagnostic records"
        with pytest.raises(AttributeError):
            selected[0].conversation_id = "client-selected-cid"
        with pytest.raises(TypeError):
            log.reset_targets(conversation_id="client-selected-cid")
        log.clear(all_sessions=True)
        assert log.reset_targets(all_sessions=True) == ()
    finally:
        log.close()


@pytest.mark.parametrize("selector", [{}, {"session_id": ""}, {"all_sessions": 1}, {"session_id": "one", "all_sessions": True}])
def test_reset_targets_require_one_explicit_selector(selector):
    log = DiagnosticLog(None)
    try:
        with pytest.raises(ValueError):
            log.reset_targets(**selector)
    finally:
        log.close()


@pytest.mark.parametrize("field", ["scope", "nativeOrigin", "sessionRef", "senderId"])
def test_reset_targets_refuse_corrupt_ownership_metadata(field):
    log = DiagnosticLog(None)
    event = _tool_event()
    try:
        session_id = _ready_reset_record(log, event)
        row = log._db.execute("SELECT metadata FROM runs WHERE session_id=?", (session_id,)).fetchone()
        metadata = json.loads(row[0])
        if field == "scope":
            metadata[field][-1] = "another-user"
        else:
            metadata[field] = "untrusted"
        with log._db:
            log._db.execute(
                "UPDATE runs SET metadata=? WHERE session_id=?",
                (json.dumps(metadata), session_id),
            )
        with pytest.raises(ValueError, match="ownership"):
            log.reset_targets(session_id=session_id)
    finally:
        log.close()


def test_user_supplied_cid_in_recorded_message_cannot_become_a_reset_target():
    log = DiagnosticLog(None)
    event = _event(question='{"conversationId":"foreign-cid","owner":"other-plugin"}')
    try:
        log.attach(event)
        session_id = _trace(log, event)["turn"]["sessionId"]
        trace_note(event, "entry", "ready", input={"conversationId": "foreign-cid"})
        assert log.reset_targets(session_id=session_id) == ()
    finally:
        log.close()


def test_tools_require_triggered_workflow_and_matching_session_without_diagnostics():
    calls = []

    @trace_tool
    async def remail_projects(self, event):
        calls.append(event)
        return "original business result"

    for group in ("987654321", ""):
        allowed = _tool_event(group=group)
        assert asyncio.run(remail_projects(None, allowed)) == "original business result"
    assert len(calls) == 2
    blocked = [_event()]
    for key, value in (
        ("_remail_owned", False),
        ("_remail_main_agent_ready", False),
        ("_remail_group_trigger_verified", False),
        (SESSION_REFERENCE_KEY, None),
        ("provider_request", SimpleNamespace()),
    ):
        event = _tool_event()
        event.set_extra(key, value)
        blocked.append(event)
    stale = _tool_event()
    stale.get_sender_id = lambda: "another-member"
    blocked.append(stale)
    for event in blocked:
        result = asyncio.run(remail_projects(None, event))
        assert json.loads(result)["ok"] is False
    assert len(calls) == 2


def test_record_freezes_before_fifo_and_close_drains_accepted_values(tmp_path, monkeypatch):
    log = DiagnosticLog(tmp_path)
    event = _event()
    log.attach(event)
    _trace(log, event)
    blocked, release = threading.Event(), threading.Event()
    appended, close_queued, close_returned = (
        threading.Event(), threading.Event(), threading.Event()
    )
    failures = []

    def hold_writer():
        blocked.set()
        if not release.wait(5):
            raise AssertionError("writer barrier timed out")

    original_append = log._append

    def observe_append(*args):
        appended.set()
        return original_append(*args)

    original_submit = log._writer.submit

    def observe_submit(operation, *args, **kwargs):
        pending = original_submit(operation, *args, **kwargs)
        if operation == log._close_db:
            close_queued.set()
        return pending

    def close_log():
        try:
            log.close()
        except Exception as exc:
            failures.append(exc)
        finally:
            close_returned.set()

    monkeypatch.setattr(log, "_append", observe_append)
    monkeypatch.setattr(log._writer, "submit", observe_submit)
    waiting = log._writer.submit(hold_writer)
    closer = None
    try:
        assert blocked.wait(2)
        payload = {
            "items": [{"number": 900719925474099312345, "text": "原文\r\n"}],
            "empty": None,
        }
        trace_note(event, "tool", "completed", output=payload)
        assert not appended.is_set(), "record must return before the busy writer can append"
        payload["items"][0]["number"] = 1
        payload["items"][0]["text"] = "changed after record returned"
        trace_finish(event, "sent", answer="已完成")
        closer = threading.Thread(target=close_log)
        closer.start()
        assert close_queued.wait(2)
        assert not close_returned.wait(0.05), "close must drain the queued records"
    finally:
        release.set()
        waiting.result(timeout=5)
        if closer is not None:
            closer.join(5)
            assert not closer.is_alive()
        log.close()
    assert not failures
    restored = DiagnosticLog(tmp_path)
    recorded = _trace(restored, event)
    assert recorded["turn"]["complete"] is True
    assert recorded["items"][1]["details"]["output"] == {
        "items": [{"number": 900719925474099312345, "text": "原文\r\n"}],
        "empty": None,
    }
    exported = restored.snapshot(view="export", trace_id=event.get_extra(TRACE_KEY)[1])
    raw_turn = json.loads(exported["rawExport"])["turns"][0]
    assert raw_turn["events"][1]["details"]["output"]["items"][0]["number"] == 900719925474099312345
    restored.close()


def test_fifo_clear_accepted_before_close_runs_and_late_callbacks_stay_deleted(tmp_path, monkeypatch):
    log = DiagnosticLog(tmp_path)
    old, fresh = _event(), _event()
    log.attach(old)
    session_id = _trace(log, old)["turn"]["sessionId"]
    blocked, release, clear_queued, close_queued = (
        threading.Event(), threading.Event(), threading.Event(), threading.Event()
    )
    counts, failures = [], []

    def hold_writer():
        blocked.set()
        if not release.wait(5):
            raise AssertionError("writer barrier timed out")

    original_submit = log._writer.submit

    def observe_submit(operation, *args, **kwargs):
        pending = original_submit(operation, *args, **kwargs)
        if operation == log._clear:
            clear_queued.set()
        if operation == log._close_db:
            close_queued.set()
        return pending

    def clear_log():
        try:
            counts.append(log.clear(session_id=session_id))
        except Exception as exc:
            failures.append(exc)

    def close_log():
        try:
            log.close()
        except Exception as exc:
            failures.append(exc)

    monkeypatch.setattr(log._writer, "submit", observe_submit)
    waiting = log._writer.submit(hold_writer)
    threads = []
    try:
        assert blocked.wait(2)
        clearer = threading.Thread(target=clear_log)
        threads.append(clearer)
        clearer.start()
        assert clear_queued.wait(2)
        trace_note(old, "tool", "completed", output="late result must not resurrect")
        log.attach(fresh)
        trace_finish(fresh, "sent", answer="new turn survives")
        closer = threading.Thread(target=close_log)
        threads.append(closer)
        closer.start()
        assert close_queued.wait(2)
    finally:
        release.set()
        waiting.result(timeout=5)
        for thread in threads:
            thread.join(5)
            assert not thread.is_alive()
        log.close()
    assert not failures
    assert counts == [{"deletedSessions": 1, "deletedTurns": 1, "deletedEvents": 1}]
    with pytest.raises(RuntimeError):
        log.snapshot()
    restored = DiagnosticLog(tmp_path)
    assert _trace(restored, old)["turn"] is None
    assert _trace(restored, fresh)["turn"]["answer"] == "new turn survives"
    assert restored.snapshot(view="turns")["total"] == 1
    restored.close()


def test_full_original_values_and_large_numbers_survive_restart_and_export(tmp_path):
    question = "@管理员 private@example.test 密码=sentinel？\r\n  原文  "
    log = DiagnosticLog(tmp_path, capture_text=False)
    event = _event(question=question)
    assert log.attach(event) is True
    assert log.attach(event) is False
    event.message_str = "业务预处理后的内容"
    event.message_obj.message.clear()
    body = "邮件原文 secret=sentinel\n" + "原" * (512 * 1024)
    huge = 900719925474099312345
    trace_note(
        event,
        "custom_stage",
        "ready",
        input={"second": huge, "first": "\t exact\r\n"},
        output=body,
        arbitraryField="不能被白名单丢掉",
    )
    trace_note(
        event, "session", "ready", conversationId="native-cid", sessionRef="QQ123456789"
    )
    trace_finish(event, "completed", answer="确切发送的答案")
    before = _trace(log, event)
    assert before["turn"]["question"] == question
    assert before["turn"]["conversationId"] == "native-cid"
    assert before["turn"]["complete"] and before["turn"]["captureComplete"]
    original = before["items"][0]["details"]["input"]
    assert original["message"] == question
    assert original["messageChain"] == [{"type": "text", "text": question}]
    row = before["items"][1]
    assert row["details"]["output"] == body
    assert row["details"]["input"]["second"] == huge
    assert row["detailsRaw"].index('"second"') < row["detailsRaw"].index('"first"')
    assert str(huge) in row["detailsRaw"]
    assert "sentinel" in row["detailsRaw"] and "arbitraryField" in row["details"]
    session_id = before["turn"]["sessionId"]
    log.close()
    path = tmp_path / "fae-diagnostics.sqlite3"
    assert path.stat().st_mode & 0o777 == 0o600
    assert tmp_path.stat().st_mode & 0o777 == 0o700
    restored = DiagnosticLog(tmp_path)
    assert _trace(restored, event)["items"] == before["items"]
    exported = restored.snapshot(view="export", session_id=session_id)
    raw = json.loads(exported["rawExport"])
    assert raw["turns"][0]["events"][1]["details"]["input"]["second"] == huge
    assert raw["turns"][0]["events"][1]["details"]["output"] == body
    assert raw["turns"][0]["question"] == question
    assert exported["storageAvailable"] is True
    restored.close()


def test_llm_input_output_snapshots_precede_mutation_and_validation_is_attached():
    log = DiagnosticLog(None)
    event = _event()
    log.attach(event)
    contexts = [{"role": "user", "content": "original context"}]
    response = SimpleNamespace(
        role="assistant",
        completion_text='  {"invalid": true}\r\n',
        reasoning_content="provider reasoning",
        raw_completion={"choices": [{"text": "RAW"}], "number": 90071992547409931},
        usage={"input": 123, "output": 456},
    )

    async def generate(**kwargs):
        assert _trace(log, event)["items"][-1]["outcome"] == "started"
        kwargs["contexts"][0]["content"] = "mutated by provider"
        return response

    result = asyncio.run(
        logged_llm_call(
            SimpleNamespace(llm_generate=generate),
            event,
            "planner",
            prompt="exact prompt\n",
            system_prompt="exact system\n",
            contexts=contexts,
            tools=None,
        )
    )
    assert result is response
    response.completion_text = "replaced by privacy gate"
    trace_note(
        event,
        "planner",
        "rejected",
        reason="invalid JSON",
        validationError="expected facts[]",
    )
    rows = _trace(log, event)["items"]
    started, completed, validation = rows[-3:]
    assert started["details"]["input"]["contexts"][0]["content"] == "original context"
    assert (
        completed["details"]["output"]["completion_text"] == '  {"invalid": true}\r\n'
    )
    assert completed["details"]["output"]["reasoning_content"] == "provider reasoning"
    assert (
        completed["details"]["output"]["raw_completion"]["number"] == 90071992547409931
    )
    assert (
        len({row["details"]["actionId"] for row in (started, completed, validation)})
        == 1
    )
    assert validation["details"]["validationError"] == "expected facts[]"
    asyncio.run(
        logged_llm_call(
            SimpleNamespace(llm_generate=generate),
            event,
            "planner",
            prompt="repair",
            contexts=contexts,
        )
    )
    assert _trace(log, event)["items"][-1]["details"]["attempt"] == 2
    log.close()


def test_whole_turn_retention_never_splits_running_turn_and_finish_is_idempotent(
    tmp_path, monkeypatch
):
    monkeypatch.setattr(diagnostics, "MAX_TURNS", 3)
    log = DiagnosticLog(tmp_path)
    running = _event(question="still running")
    log.attach(running)
    for index in range(12):
        trace_note(running, "react", "chunk", chunkIndex=index, output="x" * 1000)
    completed = []
    for index in range(5):
        event = _event(question=f"turn {index}")
        log.attach(event)
        for step in range(3):
            trace_note(event, "tool", "completed", output=f"{index}/{step}")
        trace_finish(event, "completed", answer=f"answer {index}")
        completed.append(event)
    assert log.snapshot(view="turns")["total"] == 4
    assert _trace(log, running)["total"] == 13
    assert _trace(log, completed[0])["turn"] is None
    assert _trace(log, completed[1])["items"] == []
    for event in completed[2:]:
        assert _trace(log, event)["total"] == 5
    last = completed[-1]
    trace_finish(last, "failed", answer="duplicate must not replace the answer")
    trace_note(last, "delivery", "ready", receipt="supplement after finish")
    turn = _trace(log, last)
    assert turn["total"] == 6 and turn["turn"]["outcome"] == "completed"
    assert turn["turn"]["answer"] == "answer 4"
    assert sum(row["stage"] == "end" for row in turn["items"]) == 1
    log.close()
    restored = DiagnosticLog(tmp_path)
    interrupted = _trace(restored, running)
    # Old interrupted turns may now be pruned as a whole, never partially.
    assert interrupted["turn"] is None or (
        interrupted["turn"]["outcome"] == "interrupted"
        and interrupted["turn"]["complete"] is False
        and interrupted["total"] == 13
    )
    restored.close()


def test_clear_one_scope_keeps_other_scopes_and_late_callbacks_cannot_recreate_it(
    tmp_path,
):
    log = DiagnosticLog(tmp_path)
    running, finished, other_user, other_platform = (
        _event(group="111"),
        _event(group="222"),
        _event(qq="other-user", group="222"),
        _event(platform="qq-two", group="222"),
    )
    for event in (running, finished, other_user, other_platform):
        log.attach(event)
    trace_note(running, "tool", "started", input={"original": "keep elsewhere"})
    trace_finish(finished, "completed")
    original_entry = _trace(log, running)["items"][0]
    session_id = _trace(log, running)["turn"]["sessionId"]
    others_before = [_trace(log, event) for event in (other_user, other_platform)]
    assert _trace(log, finished)["turn"]["sessionId"] == session_id
    assert log.clear(session_id=session_id) == {
        "deletedSessions": 1,
        "deletedTurns": 2,
        "deletedEvents": 4,
    }
    assert [_trace(log, event) for event in (other_user, other_platform)] == others_before
    for event in (running, finished):
        trace_note(event, "tool", "completed", output="late callback")
        trace_finish(event, "completed")
        assert _trace(log, event)["turn"] is None
        assert log.attach(event) is False
    # Replaying old entry metadata through record cannot register a deleted trace.
    log.record(
        running.get_extra(TRACE_KEY)[1], 0, "entry", "ready", original_entry["details"]
    )
    assert log.snapshot(view="turns", session_id=session_id)["total"] == 0
    fresh = _event(group="111")
    assert log.attach(fresh) is True
    assert fresh.get_extra(TRACE_KEY)[1] != running.get_extra(TRACE_KEY)[1]
    assert _trace(log, fresh)["turn"]["sessionId"] == session_id
    page = log.snapshot(view="sessions", qq="does-not-match")
    assert page["total"] == 0 and page["totalSessions"] == 3 and page["totalTurns"] == 3
    log.close()


def test_clear_all_requires_one_explicit_scope_and_allows_genuine_memory_records():
    log = DiagnosticLog(None)
    first, second = _event(qq="111"), _event(qq="222")
    log.attach(first)
    log.attach(second)
    trace_finish(first, "completed")
    session_id = _trace(log, first)["turn"]["sessionId"]
    for arguments in (
        {},
        {"session_id": ""},
        {"session_id": " "},
        {"session_id": 123},
        {"session_id": "x" * 2049},
        {"all_sessions": 1},
        {"all_sessions": "true"},
        {"session_id": session_id, "all_sessions": True},
    ):
        with pytest.raises(ValueError):
            log.clear(**arguments)
    assert log.snapshot(view="turns")["total"] == 2
    assert log.clear(all_sessions=True) == {
        "deletedSessions": 2,
        "deletedTurns": 2,
        "deletedEvents": 3,
    }
    trace_note(second, "agent", "completed", output="late running result")
    trace_finish(second, "sent")
    assert log.entries == []
    assert log.clear(all_sessions=True) == {
        "deletedSessions": 0,
        "deletedTurns": 0,
        "deletedEvents": 0,
    }
    assert log.attach(_event()) is True
    log.close()
    with pytest.raises(RuntimeError):
        log.clear(all_sessions=True)


def test_clear_failure_rolls_back_and_never_claims_to_clear_fallback_or_disabled_disk(
    tmp_path,
):
    log = DiagnosticLog(tmp_path / "persisted")
    log.attach(_event(qq="111"))
    log.attach(_event(qq="222"))
    before = log.snapshot(view="events")
    log._on_writer(
        lambda: log._db.execute(
            "CREATE TEMP TRIGGER reject_cleanup AFTER DELETE ON runs "
            "BEGIN SELECT RAISE(ABORT, 'injected cleanup failure'); END"
        )
    )
    with pytest.raises(sqlite3.DatabaseError, match="injected cleanup failure"):
        log.clear(all_sessions=True)
    assert log.snapshot(view="events") == before
    assert log.storage_ok is True
    log._on_writer(lambda: log._db.execute("DROP TRIGGER reject_cleanup"))
    log._on_writer(log._fallback, OSError("persistent storage failed"))
    with pytest.raises(RuntimeError, match="日志存储不可用"):
        log.clear(all_sessions=True)
    assert log.snapshot(view="events")["items"] == before["items"]
    log.close()
    disabled = DiagnosticLog(tmp_path / "persisted", enabled=False)
    with pytest.raises(RuntimeError, match="启用"):
        disabled.clear(all_sessions=True)
    disabled.close()
    disk = sqlite3.connect(tmp_path / "persisted" / "fae-diagnostics.sqlite3")
    try:
        assert disk.execute("SELECT COUNT(*) FROM runs").fetchone()[0] == 2
        assert disk.execute("SELECT COUNT(*) FROM events").fetchone()[0] == 2
    finally:
        disk.close()
    unavailable = tmp_path / "not-a-directory"
    unavailable.write_text("occupied", encoding="utf-8")
    failed_initialization = DiagnosticLog(unavailable)
    assert failed_initialization.path is None
    with pytest.raises(RuntimeError, match="日志存储不可用"):
        failed_initialization.clear(all_sessions=True)
    failed_initialization.close()


def test_restart_marks_unfinished_turn_and_disabled_mode_creates_no_files(tmp_path):
    log = DiagnosticLog(tmp_path)
    event = _event()
    log.attach(event)
    trace_note(event, "tool", "started", input={"code": "original-secret"})
    log.close()
    restored = DiagnosticLog(tmp_path)
    turn = _trace(restored, event)
    assert turn["turn"]["outcome"] == "interrupted"
    assert turn["turn"]["complete"] is False and turn["total"] == 2
    restored.close()
    disabled = DiagnosticLog(tmp_path / "disabled", enabled=False)
    other = _event()
    assert disabled.attach(other) is False
    trace_note(other, "entry", "started")
    assert disabled.snapshot()["items"] == []
    assert not (tmp_path / "disabled").exists()
    disabled.close()


def test_concurrent_tools_and_implicit_api_events_keep_per_request_parentage():
    log = DiagnosticLog(None)
    first = _tool_event(group="111", question="first")
    second = _tool_event(group="222", question="second")
    for event in (first, second):
        log.attach(event)
        event.set_extra(
            "_remail_react_tool_calls",
            [
                {
                    "id": f"call-{event.message_str}",
                    "name": "remail_projects",
                    "arguments": {
                        "search": event.message_str,
                        "extra_arg_dropped_by_native": True,
                    },
                    "actionId": f"react-{event.message_str}",
                },
            ],
        )

    @trace_api
    async def request(self, method, path, *, event=None, params=None):
        await asyncio.sleep(0)
        trace_note(
            None, "api", "ready", responseText=f"raw:{params['search']}", statusCode=200
        )
        return {"data": params["search"]}

    @trace_tool
    async def remail_projects(self, event, search=""):
        return await request(None, "GET", "/projects", params={"search": search})

    async def run():
        return await asyncio.gather(
            remail_projects(None, first, search="first"),
            remail_projects(None, second, search="second"),
        )

    assert asyncio.run(run()) == [{"data": "first"}, {"data": "second"}]
    for event in (first, second):
        rows = _trace(log, event)["items"]
        assert [row["seq"] for row in rows] == list(range(1, len(rows) + 1))
        tools = [row for row in rows if row["stage"] == "tool"]
        apis = [row for row in rows if row["stage"] == "api"]
        tool_id = tools[0]["details"]["actionId"]
        assert len({row["details"]["actionId"] for row in tools}) == 1
        assert tools[0]["details"]["toolCallId"] == f"call-{event.message_str}"
        assert tools[0]["details"]["parentId"] == f"react-{event.message_str}"
        assert len({row["details"]["actionId"] for row in apis}) == 1
        assert all(row["details"]["parentId"] == tool_id for row in apis)
        assert (
            next(row for row in apis if row["outcome"] == "ready")["details"][
                "responseText"
            ]
            == f"raw:{event.message_str}"
        )
    assert log.snapshot(view="sessions", qq="123456789")["total"] == 1
    assert _trace(log, first)["turn"]["sessionId"] == _trace(log, second)["turn"]["sessionId"]
    assert first.get_extra(TRACE_KEY)[1] != second.get_extra(TRACE_KEY)[1]
    assert diagnostics._CURRENT_ACTION.get() is None
    log.close()


def test_sessions_are_stable_and_qq_search_does_not_merge_sources():
    log = DiagnosticLog(None)
    events = [
        _event(),
        _event(),
        _event(group=""),
        _event(platform="qq-two"),
        _event(qq="other"),
    ]
    for event in events:
        log.attach(event)
    sessions = log.snapshot(view="sessions", qq="123456789")
    assert sessions["total"] == 2
    assert sorted(row["turnCount"] for row in sessions["items"]) == [1, 3]
    assert all(row["senderId"] == "123456789" for row in sessions["items"])
    shared = _trace(log, events[0])["turn"]["sessionId"]
    assert _trace(log, events[1])["turn"]["sessionId"] == shared
    page = log.snapshot(view="turns", session_id=shared, limit=1)
    assert page["total"] == 3 and page["truncated"] is True
    assert (
        log.snapshot(view="turns", session_id=shared, limit=1, offset=2)["truncated"]
        is False
    )
    assert len(log.snapshot(view="export", session_id=shared)["turns"]) == 3
    log.close()


def test_push_delivery_log_keeps_target_content_outcome_and_cursor(tmp_path):
    log = DiagnosticLog(tmp_path)
    log.record_push(
        "leaderboard.settled",
        "qq:GroupMessage:529642597",
        "sent",
        "2026-09-07 排行榜奖励已结算\n1. 用户 — 2 单，奖励 10 积分",
        after="2026-09-07T00:00:00Z",
        after_id="9223372036854775810",
    )
    log.record_push(
        "project.launched",
        "qq:GroupMessage:650384960",
        "failed",
        "新项目上线：#7 Demo",
        after="2026-09-07T00:01:00Z",
        after_id=11,
        error="ActionFailed: target unavailable",
    )
    log.close()
    log = DiagnosticLog(tmp_path)
    page = log.snapshot(view="pushes", limit=10)
    assert page["total"] == 2 and len(page["items"]) == 2
    assert page["items"][0]["outcome"] == "failed"
    assert page["items"][0]["destination"] == "qq:GroupMessage:650384960"
    assert page["items"][0]["error"] == "ActionFailed: target unavailable"
    assert page["items"][1]["text"].endswith("奖励 10 积分")
    assert page["items"][1]["afterId"] == "9223372036854775810"
    assert log.clear(all_sessions=True)["deletedPushes"] == 2
    assert log.snapshot(view="pushes")["total"] == 0
    log.close()


def test_legacy_group_and_private_cards_merge_by_qq_after_restart(tmp_path):
    log = DiagnosticLog(tmp_path)
    events = [_tool_event(), _tool_event(group="")]
    for event in events:
        assert log.attach(event)
        reference = event.get_extra(SESSION_REFERENCE_KEY)
        trace_note(
            event,
            "session",
            "ready",
            conversationId=reference.cid,
            sessionRef=reference.session_ref,
        )
    log.snapshot(view="sessions")  # drain the writer
    for event in events:
        trace_id = event.get_extra(TRACE_KEY)[1]
        row = log._db.execute(
            "SELECT metadata FROM runs WHERE trace_id=?", (trace_id,)
        ).fetchone()
        metadata = json.loads(row["metadata"])
        scope = tuple(metadata["scope"])
        kind = metadata["messageType"]
        legacy_id = "/".join(
            quote(part, safe="") for part in ("remail", *scope, kind)
        )
        legacy_ref = hashlib.sha256(
            json.dumps(
                (*scope, kind), ensure_ascii=True, separators=(",", ":")
            ).encode()
        ).hexdigest()[:12]
        metadata["sessionId"] = legacy_id
        metadata["sessionRef"] = legacy_ref
        log._db.execute(
            "UPDATE runs SET session_id=?,metadata=? WHERE trace_id=?",
            (legacy_id, json.dumps(metadata), trace_id),
        )
    log._db.commit()
    log.close()

    restored = DiagnosticLog(tmp_path)
    page = restored.snapshot(view="sessions", qq="123456789")
    assert page["total"] == 1
    assert page["items"][0]["turnCount"] == 2
    targets = restored.reset_targets(session_id=page["items"][0]["sessionId"])
    assert len(targets) == 2
    assert all(target.session_id.startswith("remail/") for target in targets)
    restored.close()


def test_storage_failure_does_not_change_return_value_or_lose_original_exception(
    tmp_path, monkeypatch
):
    log = DiagnosticLog(tmp_path)
    event = _event()
    log.attach(event)
    _trace(log, event)  # Drain registration before injecting a later writer failure.
    original = log._append
    calls = 0

    def fail_once(*args):
        nonlocal calls
        calls += 1
        if calls == 1:
            raise OSError("disk full at exact-path")
        return original(*args)

    monkeypatch.setattr(log, "_append", fail_once)
    response = SimpleNamespace(completion_text="原始答复")
    context = SimpleNamespace(llm_generate=AsyncMock(return_value=response))
    assert (
        asyncio.run(logged_llm_call(context, event, "writer", prompt="原始输入"))
        is response
    )
    context.llm_generate.assert_awaited_once_with(prompt="原始输入")
    snapshot = _trace(log, event)
    assert snapshot["storageAvailable"] is False
    assert "disk full at exact-path" in snapshot["recordingError"]
    assert snapshot["items"][0]["stage"] == "entry"
    for error in (
        ValueError("raw credentials=sentinel"),
        asyncio.CancelledError("raw cancellation"),
    ):
        context.llm_generate.side_effect = error
        with pytest.raises(type(error)) as caught:
            asyncio.run(
                logged_llm_call(context, event, "planner", prompt="private prompt")
            )
        assert caught.value is error
        row = _trace(log, event)["items"][-1]
        assert row["details"]["error"] == str(error)
        assert type(error).__name__ in row["details"]["traceback"]
    assert diagnostics._CURRENT_ACTION.get() is None
    log.close()


def test_v1_component_fields_are_captured_without_calling_async_serialization(tmp_path):
    text = "原始消息？\r\n" + "原" * (512 * 1024)
    original = {
        "type": "Plain",
        "text": text,
        "id": 900719925474099312345,
        "optional": None,
    }

    class V1Component:
        __fields__ = dict.fromkeys(original)

        def dict(self):
            return original.copy()

        async def to_dict(self):
            raise AssertionError("Asynchronous serialization must not run")

        def toDict(self):
            raise AssertionError("Use all v1 fields instead of the transport view")

    component = V1Component()
    component.to_dict = AsyncMock(spec=component.to_dict)
    assert snapshot_response(component) == original
    log = DiagnosticLog(tmp_path)
    event = _event()
    event.message_obj.message = [component]
    log.attach(event)
    trace_note(event, "delivery", "ready", output=component)
    trace_finish(event, "completed")
    trace = _trace(log, event)
    assert trace["items"][0]["details"]["input"]["messageChain"] == [original]
    assert trace["items"][1]["details"]["output"] == original
    assert str(original["id"]) in trace["items"][1]["detailsRaw"]
    assert trace["turn"]["captureComplete"] is True
    assert trace["turn"]["recordingError"] == ""
    component.to_dict.assert_not_called()
    log.close()


def test_real_pydantic_v1_dict_preserves_none_and_large_values(monkeypatch):
    pydantic_v1 = pytest.importorskip("pydantic.v1")

    class Component(pydantic_v1.BaseModel):
        type: str = "Plain"
        text: str
        id: int
        optional: str | None = None

        async def to_dict(self):
            raise AssertionError("Asynchronous serialization must not run")

    asynchronous = AsyncMock(spec=Component.to_dict)
    monkeypatch.setattr(Component, "to_dict", asynchronous)
    component = Component(text="完整原文\n" * 10000, id=900719925474099312345)
    assert snapshot_response(component) == {
        "type": "Plain",
        "text": component.text,
        "id": component.id,
        "optional": None,
    }
    asynchronous.assert_not_called()


def test_async_serializer_is_skipped_for_sync_fallback_or_reported_without_calling():
    class AsyncOnly:
        to_dict = AsyncMock()

    class SyncFallback(AsyncOnly):
        def toDict(self):
            return {"text": "原文\r\n", "optional": None}

    assert snapshot_response(SyncFallback()) == {"text": "原文\r\n", "optional": None}
    failure = snapshot_response(AsyncOnly())
    assert "to_dict" in failure["serializationError"]
    assert failure["type"].endswith(".AsyncOnly")
    AsyncOnly.to_dict.assert_not_called()


def test_snapshot_handles_sdk_values_and_reports_unserializable_capture_without_breaking_tool():
    @dataclass
    class Result:
        status: str
        items: tuple

    class SDKResult:
        def to_dict(self):
            return {"raw": " \r\noriginal", "number": 90071992547409931}

    class ModelResult:
        def model_dump(self, *, mode):
            assert mode == "json"
            return {"role": "assistant", "content": "original"}

    class Tools:
        def openai_schema(self):
            return [{"type": "function", "function": {"name": "original"}}]

    assert snapshot_response(Result("ready", (1, 2))) == {
        "status": "ready",
        "items": [1, 2],
    }
    assert snapshot_response(SDKResult())["raw"] == " \r\noriginal"
    assert snapshot_response(ModelResult())["content"] == "original"
    assert snapshot_response(Tools())[0]["function"]["name"] == "original"
    assert snapshot_response(asyncio.Event()) == {
        "type": "asyncio.Event",
        "isSet": False,
    }
    circular = {}
    circular["self"] = circular
    assert "serializationError" in snapshot_response(circular)["self"]

    log = DiagnosticLog(None)
    event = _tool_event()
    log.attach(event)
    unknown = object()

    @trace_tool
    async def remail_projects(self, event, search=""):
        """Preserved public tool schema."""
        return unknown

    assert str(inspect.signature(remail_projects)) == "(self, event, search='')"
    assert remail_projects.__doc__ == "Preserved public tool schema."
    assert asyncio.run(remail_projects(None, event, "test")) is unknown
    trace_finish(event, "completed")
    trace = _trace(log, event)
    assert (
        trace["turn"]["complete"] is True and trace["turn"]["captureComplete"] is False
    )
    assert trace["turn"]["recordingError"]
    assert "serializationError" in trace["items"][-2]["details"]["output"]
    log.close()


def test_deep_tool_result_string_and_broken_recorder_cannot_change_business_result(
    monkeypatch,
):
    log = DiagnosticLog(None)
    event = _tool_event()
    log.attach(event)
    result = "[" * 5000 + "0" + "]" * 5000

    @trace_tool
    async def remail_projects(self, event):
        return result

    assert asyncio.run(remail_projects(None, event)) == result
    assert _trace(log, event)["items"][-1]["details"]["output"] == result

    def broken(*args):
        raise OSError("record failure")

    monkeypatch.setattr(log, "record", broken)
    assert asyncio.run(remail_projects(None, event)) == result
    assert "record failure" in log.recording_error
    log.close()


def test_unattached_or_broken_trace_metadata_never_prevents_the_actual_operation():
    response = object()
    context = SimpleNamespace(llm_generate=AsyncMock(return_value=response))
    no_setter = SimpleNamespace(get_extra=lambda key, default=None: default)
    assert (
        asyncio.run(logged_llm_call(context, no_setter, "planner", prompt="exact"))
        is response
    )
    assert (
        asyncio.run(
            logged_operation(None, "api", "untraced", {}, context.llm_generate())
        )
        is response
    )
    log = DiagnosticLog(None)
    event = _event()
    log.attach(event)
    event.set_extra("_remail_diagnostic_attempts", object())
    assert (
        asyncio.run(logged_llm_call(context, event, "writer", prompt="exact"))
        is response
    )
    assert context.llm_generate.await_count == 3
    trace = _trace(log, event)
    assert trace["turn"]["captureComplete"] is False
    assert trace["items"][-1]["outcome"] == "capture_unavailable"
    trace_note(
        event,
        "react",
        "failed",
        reason="capture_unavailable",
        error="raw capture failure",
    )
    assert _trace(log, event)["turn"]["captureComplete"] is False
    log.close()


@pytest.mark.parametrize(
    "params",
    [
        {"limit": 0},
        {"limit": 201},
        {"limit": True},
        {"offset": -1},
        {"trace_id": "../secret"},
        {"session_id": "x" * 2049},
        {"view": "unknown"},
        {"view": "trace"},
        {"view": "export"},
        {"qq": 123},
    ],
)
def test_query_validation(params):
    log = DiagnosticLog(None)
    with pytest.raises(ValueError):
        log.snapshot(**params)
    log.close()


def test_page_handler_requires_dashboard_identity_and_bounded_read_only_query(
    monkeypatch,
):
    class Query:
        def __init__(self, pairs=()):
            self.pairs = list(pairs)

        def keys(self):
            return dict(self.pairs).keys()

        def get(self, key, default=""):
            return dict(self.pairs).get(key, default)

        def getlist(self, key):
            return [value for name, value in self.pairs if name == key]

    request = SimpleNamespace(
        username=None, plugin_name="astrbot_plugin_remail", query=Query()
    )
    module = ModuleType("astrbot.api.web")
    module.request = request
    module.error_response = lambda text, status_code: SimpleNamespace(
        status_code=status_code, payload={"message": text}
    )
    module.json_response = lambda payload, headers: SimpleNamespace(
        status_code=200, payload=payload, headers=headers
    )
    monkeypatch.setitem(sys.modules, "astrbot.api.web", module)
    workspace = ModuleType("astrbot.core.workspace")
    workspace.API_KEY_USERNAME_PREFIX = "api_key:"
    monkeypatch.setitem(sys.modules, "astrbot.core.workspace", workspace)
    tree = ast.parse(Path(__file__).with_name("main.py").read_text())
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "diagnostics_page"
    )
    resetter = AsyncMock()
    namespace = {"re": re, "asyncio": asyncio, "json": json, "reset_native_sessions": resetter}
    exec(
        compile(ast.Module(body=[handler], type_ignores=[]), "main.py", "exec"),
        namespace,
    )
    plugin = SimpleNamespace(diagnostics=DiagnosticLog(None))
    native_checks = []
    identity = {"username": "operator"}

    def validate(token, path):
        native_checks.append((token, path))
        return identity, ""

    dashboard = SimpleNamespace(
        _extract_dashboard_jwt=lambda raw: "dashboard-token",
        _validate_dashboard_token=validate,
    )
    request._request = SimpleNamespace(
        app=SimpleNamespace(
            state=SimpleNamespace(
                dashboard_app_adapter=SimpleNamespace(_dashboard_server=dashboard)
            )
        ),
        url=SimpleNamespace(path="/api/plugin/astrbot_plugin_remail/diagnostics"),
    )

    def call():
        return asyncio.run(namespace["diagnostics_page"](plugin))

    assert call().status_code == 403
    request.username = "api_key:example"
    assert call().status_code == 403
    request.username, request.plugin_name = "operator", "different_plugin"
    assert call().status_code == 403
    request.plugin_name = "astrbot_plugin_remail"
    identity = None  # Asset tokens must not read original diagnostic records.
    assert call().status_code == 403
    identity = {"username": "operator"}
    assert call().headers["Cache-Control"] == "no-store"
    for pairs in (
        (("limit", "201"),),
        (("limit", "100"), ("limit", "1")),
        (("offset", "99999"),),
        (("traceId", "../private"),),
        (("userId", "12345"),),
    ):
        request.query = Query(pairs)
        assert call().status_code == 400
    request.query = Query()

    first, second = _event(qq="111"), _event(qq="222")
    plugin.diagnostics.attach(first)
    plugin.diagnostics.attach(second)
    session_id = _trace(plugin.diagnostics, first)["turn"]["sessionId"]
    request.method = "POST"
    request.content_type = "text/plain"
    request.json = AsyncMock(return_value={"action": "clear", "all": True})
    assert call().status_code == 415
    request.json.assert_not_awaited()
    request.content_type = "application/json; charset=utf-8"
    raw_request = request._request
    del request._request
    assert call().status_code == 503  # Missing native dashboard validation.
    request.json.assert_not_awaited()
    request._request = raw_request
    identity = None
    assert call().status_code == 403  # Asset tokens or invalid dashboard tokens.
    identity = {"username": "different-operator"}
    assert call().status_code == 403
    request.json.assert_not_awaited()
    assert plugin.diagnostics.snapshot(view="turns")["total"] == 2
    identity = {"username": "operator"}
    request.query = Query((("view", "sessions"),))
    assert call().status_code == 400
    request.query = Query()
    for body in (
        [],
        {"action": "clear"},
        {"action": "other", "all": True},
        {"action": "clear", "all": 1},
        {"action": "clear", "sessionId": session_id, "all": True},
    ):
        request.json.return_value = body
        assert call().status_code == 400
    request.json.return_value = {"action": "clear", "sessionId": session_id}
    cleared = call()
    assert cleared.status_code == 200
    assert cleared.payload == {
        "deletedSessions": 1,
        "deletedTurns": 1,
        "deletedEvents": 1,
    }
    assert cleared.headers["Cache-Control"] == "no-store"
    assert native_checks[-1] == ("dashboard-token", request._request.url.path)
    assert _trace(plugin.diagnostics, first)["turn"] is None
    assert _trace(plugin.diagnostics, second)["turn"] is not None
    plugin.context = SimpleNamespace()
    scope = (
        second.get_platform_id(), second.get_platform_name(), second.get_self_id(),
        second.get_group_id(), second.get_sender_id(),
    )
    context_key = json.dumps([scope[0], scope[1], scope[2], scope[4]])
    other_key = json.dumps([scope[0], scope[1], scope[2], "333"])
    plugin.remail_intent_contexts = {context_key: "old", other_key: "keep"}
    request.json.return_value = {"action": "reset", "sessionId": _trace(plugin.diagnostics, second)["turn"]["sessionId"]}
    resetter.return_value = SimpleNamespace(status="busy", reset_conversations=0, reset_scopes=())
    assert call().status_code == 409
    assert plugin.remail_intent_contexts[context_key] == "old"
    resetter.return_value = SimpleNamespace(status="ready", reset_conversations=1, reset_scopes=(scope,))
    assert call().payload == {"resetConversations": 1}
    assert plugin.remail_intent_contexts == {other_key: "keep"}
    assert _trace(plugin.diagnostics, second)["turn"] is not None, "reset must preserve raw diagnostic evidence"
    request.json.return_value = {"action": "reset", "all": True}
    assert call().payload == {"resetConversations": 1}
    assert resetter.call_args.kwargs == {"all_sessions": True}
    assert plugin.remail_intent_contexts == {}
    request.json.return_value = {"action": "clear", "all": True}
    plugin.diagnostics.enabled = False
    assert call().status_code == 409
    plugin.diagnostics.enabled = True
    plugin.diagnostics._on_writer(
        lambda: plugin.diagnostics._db.execute(
            "CREATE TEMP TRIGGER reject_api_cleanup AFTER DELETE ON runs "
            "BEGIN SELECT RAISE(ABORT, 'injected cleanup failure'); END"
        )
    )
    assert call().status_code == 503
    assert plugin.diagnostics.snapshot(view="turns")["total"] == 1
    plugin.diagnostics._on_writer(
        lambda: plugin.diagnostics._db.execute("DROP TRIGGER reject_api_cleanup")
    )
    assert call().payload == {
        "deletedSessions": 1,
        "deletedTurns": 1,
        "deletedEvents": 1,
    }
    plugin.diagnostics.close()
    assert call().status_code == 503


def test_native_routes_require_authenticated_plugin_or_dashboard_scope():
    source = (
        Path(os.environ.get("ASTRBOT_SOURCE", "/tmp/astrbot-v4.27.5-review"))
        / "astrbot/dashboard/api/plugins.py"
    )
    if not source.is_file():
        pytest.skip("AstrBot framework source is not available")
    tree = ast.parse(source.read_text())
    functions = {
        node.name: ast.unparse(node)
        for node in tree.body
        if isinstance(node, ast.AsyncFunctionDef)
    }
    assert "Depends(require_plugin_scope)" in functions["get_plugin_extension_route"]
    assert (
        "Depends(require_dashboard_user)"
        in functions["dashboard_plugin_extension_route"]
    )


def test_diagnostics_initializes_after_mandatory_entry_guard():
    tree = ast.parse(Path(__file__).with_name("main.py").read_text())
    main = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    initialize = ast.unparse(
        next(
            node
            for node in main.body
            if isinstance(node, ast.AsyncFunctionDef) and node.name == "initialize"
        )
    )
    assert initialize.index("_install_early_entry_guard") < initialize.index(
        "DiagnosticLog("
    )
    assert initialize.index("DiagnosticLog(") < initialize.index("register_web_api(")
