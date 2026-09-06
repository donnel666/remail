"""Observe and bound provider calls on one runner without changing shared providers."""

from __future__ import annotations

import asyncio
import traceback
from contextlib import suppress
from time import monotonic
from uuid import uuid4

from . import diagnostics


class _ObservedProvider:
    def __init__(self, provider, event):
        self._provider = provider
        self._event = event

    def __getattr__(self, name):
        return getattr(self._provider, name)

    def _record(self, action_id, outcome, started, **details):
        try:
            diagnostics.trace_note(
                self._event,
                "react",
                outcome,
                actionId=action_id,
                durationMs=int((monotonic() - started) * 1000),
                **details,
            )
        except Exception:
            # Observation must not change provider results or error propagation.
            pass

    def _begin(self, args, kwargs):
        action_id, started = uuid4().hex, monotonic()
        try:
            attempt = self._event.get_extra("_remail_react_attempt", 0) + 1
            self._event.set_extra("_remail_react_attempt", attempt)
            self._event.set_extra("_remail_react_action_id", action_id)
            self._event.set_extra("_remail_react_tool_calls", [])
            metadata = {}
            parent_id = self._event.get_extra("_remail_agent_action_id", "")
            if parent_id:
                metadata["parentId"] = parent_id
            try:
                metadata["providerId"] = self._provider.meta().id
                metadata["model"] = kwargs.get("model") or self._provider.get_model()
            except (AttributeError, TypeError):
                pass
            self._record(
                action_id,
                "started",
                started,
                attempt=attempt,
                input={"args": args, "kwargs": kwargs} if args else kwargs,
                **metadata,
            )
        except Exception as exc:
            self._record(
                action_id,
                "failed",
                started,
                reason="capture_unavailable",
                error=str(exc),
            )
        return action_id, started

    def _response(self, action_id, started, response, *, chunk_index=None):
        try:
            is_chunk = bool(getattr(response, "is_chunk", False))
            self._record(
                action_id,
                "chunk"
                if is_chunk
                else (
                    "failed"
                    if getattr(response, "role", None) == "err"
                    else "completed"
                ),
                started,
                output=diagnostics.snapshot_response(response),
                **({"chunkIndex": chunk_index} if chunk_index is not None else {}),
            )
            if not is_chunk:
                self._event.set_extra(
                    "_remail_react_tool_calls",
                    diagnostics.snapshot_response(
                        [
                            {
                                "id": call_id,
                                "name": name,
                                "arguments": arguments,
                                "actionId": action_id,
                            }
                            for call_id, name, arguments in zip(
                                getattr(response, "tools_call_ids", ()) or (),
                                getattr(response, "tools_call_name", ()) or (),
                                getattr(response, "tools_call_args", ()) or (),
                            )
                        ]
                    ),
                )
        except Exception as exc:
            self._record(
                action_id,
                "failed",
                started,
                reason="capture_unavailable",
                error=str(exc),
            )

    def _error(self, action_id, started, exc):
        cancelled = isinstance(exc, (asyncio.CancelledError, GeneratorExit))
        self._record(
            action_id,
            "cancelled" if cancelled else "failed",
            started,
            error=str(exc),
            errorType=type(exc).__name__,
            traceback=traceback.format_exc(),
        )
        if cancelled:
            try:
                diagnostics.trace_finish(
                    self._event, "cancelled", reason="provider_cancelled"
                )
            except Exception:
                # A recording failure must not replace the provider's cancellation.
                pass

    def _timeout_response(self, action_id, started, exc):
        from astrbot.core.provider.entities import LLMResponse

        self._event.set_extra("_remail_llm_timeout_stage", "react")
        self._event.set_extra("_remail_react_tool_calls", [])
        self._record(
            action_id,
            "failed",
            started,
            reason="llm_timeout",
            errorType=type(exc).__name__,
            error=str(exc),
            timeoutSeconds=getattr(exc, "seconds", None),
        )
        # Native role='err' skips on_agent_done and its history/send gates.
        # This assistant result takes the normal gated completion path instead.
        return LLMResponse(
            role="assistant", completion_text=diagnostics.LLM_TIMEOUT_TEXT
        )

    async def text_chat(self, *args, **kwargs):
        action_id, started = self._begin(args, kwargs)
        try:
            budget = diagnostics.llm_time_budget(self._event, "react")
            if budget is None:
                response = await self._provider.text_chat(*args, **kwargs)
            else:
                try:
                    response = await asyncio.wait_for(
                        self._provider.text_chat(*args, **kwargs), timeout=budget
                    )
                except asyncio.TimeoutError as exc:
                    raise diagnostics.LLMDeadlineExceeded("react", budget) from exc
        except diagnostics.LLMDeadlineExceeded as exc:
            return self._timeout_response(action_id, started, exc)
        except (Exception, asyncio.CancelledError) as exc:
            self._error(action_id, started, exc)
            raise
        self._response(action_id, started, response)
        return response

    async def text_chat_stream(self, *args, **kwargs):
        action_id, started = self._begin(args, kwargs)
        stream = None
        finished = False
        index = 0
        try:
            budget = diagnostics.llm_time_budget(self._event, "react")
            call_deadline = monotonic() + budget if budget is not None else None
            stream = self._provider.text_chat_stream(*args, **kwargs)
            while True:
                try:
                    if call_deadline is None:
                        response = await anext(stream)
                    else:
                        remaining = call_deadline - monotonic()
                        if remaining <= 0:
                            raise diagnostics.LLMDeadlineExceeded("react", budget)
                        try:
                            response = await asyncio.wait_for(
                                anext(stream), timeout=remaining
                            )
                        except asyncio.TimeoutError as exc:
                            raise diagnostics.LLMDeadlineExceeded(
                                "react", budget
                            ) from exc
                except StopAsyncIteration:
                    break
                index += 1
                self._response(action_id, started, response, chunk_index=index)
                finished |= not bool(getattr(response, "is_chunk", False))
                yield response
                if finished:
                    return
            if not finished:
                self._record(
                    action_id, "completed", started, finalResponseAvailable=False
                )
        except diagnostics.LLMDeadlineExceeded as exc:
            finished = True
            yield self._timeout_response(action_id, started, exc)
        except (Exception, asyncio.CancelledError, GeneratorExit) as exc:
            if not (finished and isinstance(exc, GeneratorExit)):
                self._error(action_id, started, exc)
            raise
        finally:
            close = getattr(stream, "aclose", None)
            if callable(close):
                # Match the native runner's stream cleanup behavior.
                with suppress(asyncio.CancelledError, RuntimeError, StopAsyncIteration):
                    await close()


def install_native_trace(event, run_context) -> bool:
    """Install per-run observation and deadlines from ``on_agent_begin``.

    A registry mismatch returns False so the caller can stop a workflow that
    requires a deadline instead of running it without the expected limit.
    """
    state = event.get_extra(diagnostics.TRACE_KEY, None)
    recording = (
        isinstance(state, tuple)
        and len(state) == 3
        and getattr(state[0], "enabled", False)
        and not getattr(state[0], "closed", True)
    )
    bounded = (
        event.get_extra("_remail_owned", False) is True
        and event.get_extra("_remail_llm_deadline", None) is not None
    )
    if not recording and not bounded:
        return False
    try:
        from astrbot.core.pipeline.process_stage.follow_up import _ACTIVE_AGENT_RUNNERS

        runner = _ACTIVE_AGENT_RUNNERS.get(event.unified_msg_origin)
        if runner is None:
            raise LookupError("No active native runner for this message origin")
        if (
            getattr(runner, "run_context", None) is not run_context
            or getattr(getattr(run_context, "context", None), "event", None)
            is not event
        ):
            raise ValueError("Active native runner belongs to a different event")
        providers = [runner.provider, *runner.fallback_providers]
        observed = []
        for provider in providers:
            if isinstance(provider, _ObservedProvider):
                if provider._event is not event:
                    raise ValueError("Provider observer belongs to a different event")
                observed.append(provider)
            else:
                if not callable(getattr(provider, "text_chat", None)):
                    raise TypeError("Native runner provider does not expose text_chat")
                observed.append(_ObservedProvider(provider, event))
        runner.provider = observed[0]
        runner.fallback_providers = observed[1:]
        if not recording:
            return True
        original_tools = getattr(runner, "_handle_function_tools", None)
        if not callable(original_tools):
            raise TypeError("Native runner does not expose its tool-call iterator")
        tool_event = getattr(original_tools, "_remail_trace_event", None)
        if tool_event is not None and tool_event is not event:
            raise ValueError("Native tool-call observer belongs to a different event")
        if tool_event is not event:

            async def observe_tools(*args, **kwargs):
                stream = None
                try:
                    stream = original_tools(*args, **kwargs)
                    async for result in stream:
                        try:
                            chain = getattr(result, "message_chain", None)
                            if getattr(chain, "type", None) == "tool_call":
                                payload = chain.chain[0].data
                                if not isinstance(payload, dict) or not (
                                    payload.get("id") and payload.get("name")
                                ):
                                    raise ValueError("Native tool-call identity is missing")
                                event.set_extra(
                                    "_remail_active_tool_call",
                                    diagnostics.snapshot_response({
                                        "id": payload["id"],
                                        "name": payload["name"],
                                        "arguments": payload.get("args"),
                                        "actionId": event.get_extra("_remail_react_action_id", ""),
                                    }),
                                )
                        except Exception as exc:
                            with suppress(Exception):
                                event.set_extra("_remail_active_tool_call", None)
                                diagnostics.trace_note(
                                    event, "tool", "capture_unavailable", error=str(exc)
                                )
                        yield result
                finally:
                    with suppress(Exception):
                        event.set_extra("_remail_active_tool_call", None)
                    close = getattr(stream, "aclose", None)
                    if callable(close):
                        with suppress(
                            asyncio.CancelledError, RuntimeError, StopAsyncIteration
                        ):
                            await close()

            observe_tools._remail_trace_event = event
            runner._handle_function_tools = observe_tools
        event.set_extra("_remail_native_tool_observer", True)
        return True
    except Exception as exc:
        diagnostics.trace_note(
            event,
            "react",
            "failed",
            reason="capture_unavailable",
            error=str(exc),
            errorType=type(exc).__name__,
            traceback=traceback.format_exc(),
        )
        return False
