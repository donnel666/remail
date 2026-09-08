"""Offline coroutine doubles for event-local waiting budgets; no real providers."""

import asyncio
from time import monotonic
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .diagnostics import DiagnosticLog, LLMDeadlineExceeded, logged_llm_call


def test_model_deadline_cancels_wait_and_is_independent_of_recording():
    async def scenario(enabled):
        extras = {"_remail_llm_timeout": 0.01, "_remail_llm_deadline": monotonic() + 1}
        event = SimpleNamespace(
            get_extra=lambda key, default=None: extras.get(key, default),
            set_extra=lambda key, value: extras.__setitem__(key, value),
            unified_msg_origin="qq:FriendMessage:123",
            get_platform_id=lambda: "qq",
            get_platform_name=lambda: "aiocqhttp",
            get_self_id=lambda: "999",
            get_group_id=lambda: "",
            get_sender_id=lambda: "123",
            get_message_type=lambda: SimpleNamespace(value="FriendMessage"),
            message_str="问题",
            message_obj=SimpleNamespace(),
        )
        log = DiagnosticLog(None, enabled=enabled)
        assert log.attach(event) is enabled
        cancelled = []

        async def model(**kwargs):
            try:
                await asyncio.Event().wait()
            finally:
                cancelled.append(True)

        with pytest.raises(LLMDeadlineExceeded) as raised:
            await logged_llm_call(
                SimpleNamespace(llm_generate=model), event, "critic", prompt="原文"
            )
        assert raised.value.stage == "critic" and cancelled == [True]
        assert extras["_remail_llm_timeout_stage"] == "critic"
        if enabled:
            assert log.entries[-1]["outcome"] == "failed"
            assert log.entries[-1]["details"]["errorType"] == "LLMDeadlineExceeded"
        log.close()

    for enabled in (False, True):
        asyncio.run(scenario(enabled))


def test_remaining_workflow_budget_is_not_reset_and_does_not_call_provider():
    extras = {"_remail_llm_timeout": 90, "_remail_llm_deadline": monotonic() - 1}
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    context = SimpleNamespace(llm_generate=AsyncMock())
    with pytest.raises(LLMDeadlineExceeded):
        asyncio.run(logged_llm_call(context, event, "writer", prompt="原文"))
    context.llm_generate.assert_not_awaited()
    assert extras["_remail_llm_timeout_stage"] == "writer"
