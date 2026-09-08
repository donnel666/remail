import ast
import asyncio
import contextlib
import json
import re
import sys
import uuid
from copy import copy, deepcopy
from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal
from functools import partial
from pathlib import Path
from time import monotonic
from types import ModuleType, SimpleNamespace
from typing import Any
from unittest.mock import AsyncMock

import pytest

from .diagnosis import (
    DiagnosisFact,
    diagnosis_fact_payload,
    normalize_diagnosis_payload,
    render_diagnosis_fact,
    seal_diagnosis_fact,
)
from .feedback import sanitize_feedback_text, sanitize_report
from .group_context import load_group_context
from .knowledge import (
    INTERNAL_MODULE_KNOWLEDGE,
    PUBLIC_DISCLOSURE_RULES,
    trusted_public_rule,
)
from .diagnostics import (
    DiagnosticLog,
    LLM_TIMEOUT_TEXT,
    logged_llm_call,
    logged_operation,
    snapshot_response,
    trace_finish,
    trace_note,
    trace_tool,
)
from .sessions import (
    bind_native_request,
    ensure_native_session,
    get_session_reference,
    prepare_native_history,
    read_existing_history,
    reset_native_sessions,
    session_request_matches,
)
from .sources import (
    PublicAPIContract,
    SOURCE_RELIABILITY_RULES,
    STRONG_SOURCES,
    api_example_urls,
    evidence_block,
    evidence_text,
    public_api_contract,
    source_metadata,
    weak_time_metadata,
    within_weak_window,
)
from .persona import (
    CRITIC_SYSTEM_PROMPT,
    FACT_REPAIR_SYSTEM_PROMPT,
    PERSONA_SYSTEM_PROMPT,
    build_critic_payload,
    build_persona_payload,
    has_unsupported_concrete_facts,
    parse_critic_response,
    parse_critic_feedback,
    sanitize_model_text,
    restore_seals,
    unsupported_sensitive_states,
    validate_persona_response,
)
from .security import (
    adapter_channel,
    channel_system_keys,
    contains_credentials,
    has_disallowed_url,
    keyword_blacklist_match,
    normalize_adapter_identity,
    normalize_security_text,
    redact_credentials,
    redact_message_outline,
    redact_message_text,
    redact_personal_data,
    validated_base_url,
    websocket_url,
)
from .workflow import (
    API_SUPPORT_GUIDANCE,
    EVIDENCE_CLAIMS,
    INTENT_SYSTEM_PROMPT,
    PLANNER_SYSTEM_PROMPT,
    PUBLIC_BUSINESS_RULES,
    RECHARGE_PAYMENT_METHODS,
    FactPlan,
    FactRequest,
    model_json_text,
    parse_fact_plan,
    planner_payload,
    structured_response_text,
)


PLUGIN_DIR = Path(__file__).parent


_RESULT_TYPES = SimpleNamespace(
    GENERAL_RESULT="general", LLM_RESULT="llm", STREAMING_RESULT="streaming"
)


class _PlainStub(tuple):
    def __new__(cls, text):
        value = super().__new__(cls, ("plain", text))
        value.text = text
        return value


class _ResultStub(SimpleNamespace):
    def __init__(self, chain=None, *, result_content_type=None):
        super().__init__(
            chain=[] if chain is None else chain,
            result_content_type=result_content_type or _RESULT_TYPES.GENERAL_RESULT,
            result_type="CONTINUE",
        )

    def is_llm_result(self):
        return self.result_content_type == _RESULT_TYPES.LLM_RESULT

    def is_stopped(self):
        return self.result_type == "STOP"


def _support_result_api(event):
    """Give lightweight event fixtures one native result slot and persistent STOP state."""
    if not all(
        callable(getattr(event, name, None))
        for name in ("set_result", "get_result", "clear_result")
    ):
        previous_get = getattr(event, "get_result", None)
        event._test_result = previous_get() if callable(previous_get) else None

        def set_result(result):
            event._test_result = (
                _ResultStub([_PlainStub(result)]) if isinstance(result, str) else result
            )

        event.set_result = set_result
        event.get_result = lambda: event._test_result
        event.clear_result = lambda: setattr(event, "_test_result", None)
    if not callable(getattr(event, "is_stopped", None)):
        previous_stop = getattr(event, "stop_event", None)
        event._test_stopped = False

        def stop():
            event._test_stopped = True
            if callable(previous_stop):
                previous_stop()

        def is_stopped():
            result = event.get_result()
            return event._test_stopped or bool(
                result is not None
                and callable(getattr(result, "is_stopped", None))
                and result.is_stopped()
            )

        event.stop_event = stop
        event.is_stopped = is_stopped
    if not callable(getattr(event, "send", None)):
        event.send = AsyncMock()
    return event


class _ToolSetStub:
    def __init__(
        self,
        names: list[str] | None = None,
        module_path: str = "integrations.astrbot_plugin_remail.main",
        owner: Any = None,
    ) -> None:
        self.tools = [
            SimpleNamespace(
                name=name,
                handler_module_path=module_path,
                _wrapped=SimpleNamespace(handler=partial(lambda *_args: None, owner)),
            )
            for name in (names or [])
        ]

    def names(self) -> list[str]:
        return [tool.name for tool in self.tools]

    def remove_tool(self, name: str) -> None:
        self.tools = [tool for tool in self.tools if tool.name != name]


def _fact_plan(
    *,
    intents: tuple[str, ...] = ("social",),
    facts: tuple[dict[str, Any], ...] = (),
    answer_mode: str = "normal",
    privacy: str = "public",
    entities: dict[str, Any] | None = None,
    route: str = "remail",
) -> FactPlan:
    raw = json.dumps(
        {
            "route": route,
            "answer_mode": answer_mode,
            "privacy": privacy,
            "intents": list(intents),
            "entities": entities or {},
            "facts": list(facts),
        },
        ensure_ascii=False,
    )
    plan = parse_fact_plan(raw)
    assert not plan.failed, raw
    return plan


def _fact(
    fact_id: str,
    claim: str,
    *,
    params: dict[str, Any] | None = None,
    depends_on: tuple[str, ...] = (),
) -> dict[str, Any]:
    return {
        "id": fact_id,
        "claim": claim,
        "required": True,
        "params": params or {},
        "dependsOn": list(depends_on),
    }


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
    namespace = {
        "Any": Any,
        "re": re,
        "normalize_security_text": normalize_security_text,
        "redact_credentials": redact_credentials,
        "redact_personal_data": redact_personal_data,
    }
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
    namespace = {"json": json, "re": re}
    exec(compile(ast.Module(body=body, type_ignores=[]), "main.py", "exec"), namespace)
    return namespace


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


def _load_openapi_excerpt():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    method = next(
        node
        for node in main_class.body
        if isinstance(node, ast.FunctionDef) and node.name == "_openapi_excerpt"
    )
    method.decorator_list = []
    namespace = {
        "Any": Any,
        "json": json,
        "normalize_security_text": normalize_security_text,
        "re": re,
        "_is_public_api_path": lambda path: (
            path.startswith("/v1/open/")
            or path == "/v1/pickup"
            or path.startswith("/v1/pickup/")
        ),
    }
    exec(
        compile(
            ast.fix_missing_locations(ast.Module(body=[method], type_ignores=[])),
            "main.py",
            "exec",
        ),
        namespace,
    )
    return namespace["_openapi_excerpt"]


def _load_welcome_functions():
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    constant_names = {
        "_BIND_ARGUMENTS",
        "_REMAIL_COMMAND_PREFIX",
        "_REMAIL_INTENT_SYSTEM_PROMPT",
        "_REMAIL_ONLY_TEXT",
        "_REMAIL_INTENT_UNAVAILABLE_TEXT",
        "_REMAIL_TOOLSET_UNAVAILABLE_TEXT",
        "_REMAIL_SAFE_ERROR_TEXT",
        "_REMAIL_BINDING_GUIDANCE",
        "_REMAIL_CREDENTIAL_INPUT_TEXT",
        "_ALLOWED_REMAIL_TOOLS",
        "_REMAIL_TOOL_MODULE_SUFFIX",
        "_REMAIL_CORE_SYSTEM_PROMPT",
        "_REMAIL_PUBLIC_BILLING_SYSTEM_PROMPT",
        "_REMAIL_PUBLIC_SERVICE_SYSTEM_PROMPT",
        "_REMAIL_REACT_SYSTEM_PROMPT",
        "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT",
        "_PRODUCT_TYPE_ALIASES",
        "_PROJECT_PRICE_SUBJECT",
        "_MONEY_PAYMENT_QUERY",
        "_PROJECT_YUAN_PRICE",
        "_PRODUCT_LABELS",
        "_PROJECT_PRICE_QUERY",
        "_INVENTORY_QUERY",
        "_EXACT_INVENTORY_QUERY",
        "_FUTURE_QUERY",
        "_RECHARGE_CONFIG_QUERY",
        "_PROJECT_QUERY",
        "_PROJECT_STATE_QUERY",
        "_GENERIC_PROJECT_LIST_QUERY",
        "_GENERIC_PRICE_SCOPE_QUERY",
        "_API_CONTRACT_QUERY",
        "_PUBLIC_API_SUPPORT_QUERY",
        "_INTERNAL_API_IMPLEMENTATION_QUERY",
        "_PUBLIC_API_DETAIL_QUERY",
        "_CLIENT_IMPLEMENTATION_QUERY",
        "_USER_OWNED_IMPLEMENTATION_QUERY",
        "_CLIENT_LOCAL_IMPLEMENTATION_QUERY",
        "_INTERNAL_SYSTEM_CONTEXT",
        "_DYNAMIC_FACT_HINT",
        "_FAQ_QUERY",
        "_SERVICE_WINDOW_QUERY",
        "_ANNOUNCEMENT_QUERY",
        "_RANKING_QUERY",
        "_ELLIPTICAL_FOLLOWUP",
        "_PRICE_STOCK_SENTENCE",
        "_PRIVACY_TRADITIONAL_TRANS",
        "_DIAGNOSIS_QUERY",
        "_ORDER_DIAGNOSIS_PROBLEM",
        "_DIAGNOSIS_ASSERTION",
        "_DIAGNOSIS_NOT_VERIFIED_RESPONSE",
        "_GROUP_ORDER_VALUE",
        "_GROUP_OTP_VALUE",
        "_GROUP_ACCOUNT_VALUE",
        "_GROUP_PROFILE_VALUE",
        "_GROUP_EMAIL",
        "_GROUP_PLATFORM_ID_VALUE",
        "_GROUP_PRIVATE_MAIL_DETAIL",
        "_GROUP_MAIL_DISCLOSURE",
        "_GROUP_MAIL_CONTEXT",
        "_GROUP_PRIVATE_MAIL_REQUEST",
        "_PUBLIC_API_MAIL_FIELD_QUERY",
        "_GROUP_MAIL_INSTANCE_REQUEST",
        "_GROUP_MAIL_CODE_VALUE",
        "_GROUP_PRIVATE_MAIL_RESPONSE",
        "_PLANNER_PRIVATE_DETAIL",
        "_HARD_INTERNAL_EXPOSURE",
        "_INTERNAL_TECHNOLOGY_VALUE",
        "_CLIENT_CODE_EXPOSURE",
        "_INTERNAL_IMPLEMENTATION_EXPOSURE",
        "_BLACK_BOX_RESPONSE",
        "_CREDENTIAL_NAME",
        "_CREDENTIAL_REQUEST_CUE",
        "_CREDENTIAL_REQUEST_RESPONSE",
        "_REMAIL_EVENT_MARKER",
        "_REMAIL_AUTHORIZED_MARKER",
        "_REMAIL_EVIDENCE_KEY",
        "_REMAIL_INTENT_PLAN_KEY",
        "_REMAIL_INPUT_PREPARED_KEY",
        "_REMAIL_ORDER_EMAIL_KEY",
        "_REMAIL_CREDENTIAL_INPUT_KEY",
        "_REMAIL_CANONICAL_RESPONSE_KEY",
        "_REMAIL_MAIN_AGENT_READY_KEY",
        "_EVIDENCE_ORDER",
        "_OUTPUT_URL",
        "_DYNAMIC_OUTPUT_LITERAL",
        "_DYNAMIC_CHINESE_LITERAL",
        "_MIXED_PRICE_RECHARGE_QUERY",
        "_LOWER_PRIORITY_DYNAMIC_SENTENCE",
        "_UNPLANNED_DYNAMIC_RESPONSE",
        "_PUSH_EMAIL",
        "_PUSH_DATABASE_URL",
        "_PUSH_CREDENTIAL",
        "_PUSH_SYSTEM_KEY",
        "_PUSH_AUTHORIZATION",
    }
    constants = [
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id in constant_names
            for target in node.targets
        )
    ]
    helpers = [
        node
        for node in tree.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name
        in {
            "_safe_push_value",
            "_joined_group_members",
            "_qq_group_join_request",
            "_structured_strings",
            "_qq_moderation_text",
            "_remail_intent_label",
            "_remail_intent_decision",
            "_is_public_api_path",
            "_public_api_capability_summary",
            "_project_background_view",
            "_prepare_fae_context",
            "_start_order_prefetch",
            "_finish_order_prefetch",
            "_model_background",
            "_configured_personality",
            "_recent_intent_context",
            "_orders_view",
            "_render_orders_evidence",
            "_safe_llm_context_text",
            "_service_entry_requested",
            "_event_is_command",
            "_group_fae_trigger",
            "_event_scope",
            "_event_reply_target",
            "_capture_group_request",
            "_reply_components",
            "_send_scoped_text",
            "_suppress_untriggered_group_reply",
            "_install_early_entry_guard",
            "_generate_fact_plan",
            "_prepare_fae_workflow",
            "_prepare_owned_event_input",
            "_classify_api_consultation",
            "_is_remail_command",
            "_intent_context_key",
            "_event_is_private",
            "_event_is_owned",
            "_mark_event_owned",
            "_install_owned_send_guard",
            "_question_project_id",
            "_build_intent_plan",
            "_intent_plan",
            "_inventory_observation_is_fresh",
            "_evidence_is_valid",
            "_record_evidence",
            "_evidence_entries",
            "_entry_matches_plan",
            "_entry_matches_fact",
            "_fact_is_satisfied",
            "_evidence_claims",
            "_evidence_data",
            "_render_price_evidence",
            "_render_inventory_evidence",
            "_render_recharge_evidence",
            "_recharge_quote_view",
            "_render_recharge_quote_evidence",
            "_project_items_for_plan",
            "_render_projects_evidence",
            "_without_urls",
            "_contains_dynamic_literal",
            "_render_faq_evidence",
            "_render_announcement_evidence",
            "_render_group_evidence",
            "_render_api_evidence",
            "_render_ranking_evidence",
            "_render_evidence_claim",
            "_grounded_dynamic_answer",
            "_evidence_blocks",
            "_persona_evidence_packet",
            "_generate_persona_answer",
            "_complete_react_answer",
            "_request_is_remail",
            "_restrict_remail_tools",
            "_positive_platform_id",
            "_public_api_support_likely",
            "_integral_tool_int",
            "_configured_qq_management",
            "_normalize_product_types",
            "_single_product_type_query",
            "_project_price_source_is_valid",
            "_project_price_view",
            "_faq_view",
            "_announcement_view",
            "_recharge_config_view",
            "_enforce_project_price_units",
            "_asks_price_or_stock",
            "_enforce_group_privacy",
            "_enforce_black_box",
            "_requests_credentials",
            "_enforce_output_prohibitions",
            "_safe_egress_text",
            "_required_evidence",
            "_missing_evidence_response",
            "_scope_question",
            "_needs_order_diagnosis",
            "_enforce_diagnosis_fact",
            "_replace_response_text",
            "_safe_response_fallback",
            "_sync_final_agent_message",
            "_enforce_answer_scope",
            "_mentioned_qq_ids",
            "_mentions_bot",
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
            "handoff_group_manager_mentions",
            "classify_mentioned_group_question",
            "authorize_llm",
            "_ensure_openapi_spec",
            "_public_api_capability_context",
            "prepare_remail_llm_response",
            "require_bound_service_user",
            "_authorize_event",
            "remail_orders",
            "_reply",
            "enforce_redemption_channel_priority",
            "snapshot_safe_remail_response",
            "sync_safe_response_history",
            "finalize_safe_remail_result",
        }
    ]
    for handler in handlers:
        handler.decorator_list = []

    class ReMailError(RuntimeError):
        pass

    class TextPart:
        def __init__(self, text: str) -> None:
            self.text = text

    class At(tuple):
        def __new__(cls, **values):
            value = super().__new__(cls, ("at", values))
            value.qq = values.get("qq", "")
            value.name = values.get("name", "")
            return value

    namespace = {
        "AstrMessageEvent": object,
        "Any": Any,
        "asyncio": asyncio,
        "contextlib": contextlib,
        "copy": copy,
        "ProviderRequest": SimpleNamespace,
        "At": At,
        "Reply": lambda **values: ("reply", values),
        "has_disallowed_url": has_disallowed_url,
        "json": json,
        "keyword_blacklist_match": keyword_blacklist_match,
        "LLMResponse": object,
        "MessageChain": lambda items: items,
        "MessageType": SimpleNamespace(FRIEND_MESSAGE="friend", GROUP_MESSAGE="group"),
        "normalize_adapter_identity": normalize_adapter_identity,
        "normalize_security_text": normalize_security_text,
        "Plain": _PlainStub,
        "ResultContentType": _RESULT_TYPES,
        "re": re,
        "ReMailError": ReMailError,
        "redact_message_text": redact_message_text,
        "redact_credentials": redact_credentials,
        "redact_personal_data": redact_personal_data,
        "contains_credentials": contains_credentials,
        "_safe_user_error": lambda _exc: "当前会话未获授权。",
        "sanitize_feedback_text": sanitize_feedback_text,
        "sanitize_report": sanitize_report,
        "TextPart": TextPart,
        "logger": SimpleNamespace(warning=lambda *_args: None),
        "monotonic": monotonic,
        "datetime": datetime,
        "Decimal": Decimal,
        "dataclass": dataclass,
        "timezone": timezone,
        "uuid": uuid,
        "DiagnosisFact": DiagnosisFact,
        "diagnosis_fact_payload": diagnosis_fact_payload,
        "normalize_diagnosis_payload": normalize_diagnosis_payload,
        "render_diagnosis_fact": render_diagnosis_fact,
        "seal_diagnosis_fact": seal_diagnosis_fact,
        "CRITIC_SYSTEM_PROMPT": CRITIC_SYSTEM_PROMPT,
        "FACT_REPAIR_SYSTEM_PROMPT": FACT_REPAIR_SYSTEM_PROMPT,
        "PERSONA_SYSTEM_PROMPT": PERSONA_SYSTEM_PROMPT,
        "build_critic_payload": build_critic_payload,
        "build_persona_payload": build_persona_payload,
        "has_unsupported_concrete_facts": has_unsupported_concrete_facts,
        "parse_critic_response": parse_critic_response,
        "parse_critic_feedback": parse_critic_feedback,
        "sanitize_model_text": sanitize_model_text,
        "LLM_TIMEOUT_TEXT": LLM_TIMEOUT_TEXT,
        "EVIDENCE_CLAIMS": EVIDENCE_CLAIMS,
        "restore_seals": restore_seals,
        "unsupported_sensitive_states": unsupported_sensitive_states,
        "validate_persona_response": validate_persona_response,
        "PLANNER_SYSTEM_PROMPT": PLANNER_SYSTEM_PROMPT,
        "INTENT_SYSTEM_PROMPT": INTENT_SYSTEM_PROMPT,
        "INTERNAL_MODULE_KNOWLEDGE": INTERNAL_MODULE_KNOWLEDGE,
        "PUBLIC_DISCLOSURE_RULES": PUBLIC_DISCLOSURE_RULES,
        "API_SUPPORT_GUIDANCE": API_SUPPORT_GUIDANCE,
        "trusted_public_rule": trusted_public_rule,
        "PUBLIC_BUSINESS_RULES": PUBLIC_BUSINESS_RULES,
        "RECHARGE_PAYMENT_METHODS": RECHARGE_PAYMENT_METHODS,
        "SOURCE_RELIABILITY_RULES": SOURCE_RELIABILITY_RULES,
        "STRONG_SOURCES": STRONG_SOURCES,
        "evidence_block": evidence_block,
        "api_example_urls": api_example_urls,
        "public_api_contract": public_api_contract,
        "structured_response_text": structured_response_text,
        "source_metadata": source_metadata,
        "weak_time_metadata": weak_time_metadata,
        "within_weak_window": within_weak_window,
        "load_group_context": load_group_context,
        "DiagnosticLog": DiagnosticLog,
        "logged_llm_call": logged_llm_call,
        "logged_operation": logged_operation,
        "snapshot_response": snapshot_response,
        "trace_finish": trace_finish,
        "trace_note": trace_note,
        "trace_tool": trace_tool,
        "bind_native_request": bind_native_request,
        "ensure_native_session": ensure_native_session,
        "prepare_native_history": prepare_native_history,
        "reset_native_sessions": reset_native_sessions,
        "get_session_reference": get_session_reference,
        "read_existing_history": read_existing_history,
        "session_request_matches": session_request_matches,
        "FactPlan": FactPlan,
        "FactRequest": FactRequest,
        "IntentPlan": FactPlan,
        "parse_fact_plan": parse_fact_plan,
        "model_json_text": model_json_text,
        "planner_payload": planner_payload,
    }
    exec(
        compile(
            ast.fix_missing_locations(
                ast.Module(body=[*constants, *helpers, *handlers], type_ignores=[])
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )
    return namespace, ReMailError


class _NativeMessageKind(str):
    @property
    def value(self):
        return "FriendMessage" if self == "friend" else "GroupMessage"


class _ConversationManagerStub:
    """In-memory transport for the real sessions.py owner/reference validation."""

    def __init__(self):
        self.current, self.conversations = {}, {}

    async def get_curr_conversation_id(self, owner):
        return self.current.get(owner)

    async def get_conversation(self, _owner, cid):
        return self.conversations.get(cid)

    async def new_conversation(self, owner, *, platform_id):
        cid = str(uuid.uuid4())
        self.current[owner] = cid
        self.conversations[cid] = SimpleNamespace(
            cid=cid,
            user_id=owner,
            platform_id=platform_id,
            history="[]",
            persona_id=None,
        )
        return cid


def _support_session(event, context, request=None):
    """Supply native event fields; only explicit post-admission fixtures pin a request."""
    umo = getattr(event, "unified_msg_origin", "bot:FriendMessage:123456789")
    platform, kind, session = umo.split(":", 2)
    private = kind == "FriendMessage"
    for method, value in (
        ("get_platform_id", platform),
        ("get_platform_name", "aiocqhttp"),
        ("get_self_id", "999999999"),
        ("get_group_id", "" if private else session.rsplit("_", 1)[-1]),
        ("get_sender_id", session.split("_", 1)[0]),
    ):
        if not callable(getattr(event, method, None)):
            setattr(event, method, lambda value=value: value)
    event.unified_msg_origin = umo
    event.get_message_type = lambda: _NativeMessageKind(
        "friend" if private else "group"
    )
    _support_result_api(event)
    event.set_extra("_remail_binding_state", "bound")
    if context is None:
        return
    if not hasattr(context, "conversation_manager"):
        context.conversation_manager = _ConversationManagerStub()
    if not callable(getattr(context, "get_config", None)):
        context.get_config = lambda _umo=None: {
            "provider_settings": {
                "show_tool_use_status": False,
                "show_tool_call_result": False,
            }
        }
    if request is not None:
        incoming_contexts = getattr(request, "contexts", None)
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
        result = asyncio.run(ensure_native_session(context, event, scope=scope))
        assert result.status == "ready"
        assert bind_native_request(event, request, scope=scope)
        assert session_request_matches(event, request, scope=scope)
        if incoming_contexts is not None:
            # Simulate another hook adding history after session admission; authorize
            # still has to remove it rather than passing this check vacuously.
            request.contexts = incoming_contexts


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


def test_credentials_redaction_covers_codes_before_any_output() -> None:
    redacted = redact_credentials(
        '验证码是 654321；{"verification_code":"778899"}; OTP: 112233'
    )
    for secret in ("654321", "778899", "112233"):
        assert secret not in redacted

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


def test_fact_planner_llm_requires_one_valid_structured_plan() -> None:
    functions, _ = _load_welcome_functions()
    expected = _fact_plan(intents=("social",))
    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(expected.to_dict(), ensure_ascii=False),
            )
        ),
    )
    event = SimpleNamespace(
        unified_msg_origin="bot:FriendMessage:1",
        get_message_type=lambda: "friend",
        get_extra=lambda _key, default=None: default,
    )
    plan = asyncio.run(
        functions["_generate_fact_plan"](context, event, "你好，介绍一下 ReMail")
    )
    assert plan == expected
    call = context.llm_generate.await_args.kwargs
    assert call["tools"] is None and call["contexts"] is None
    assert call["system_prompt"].startswith(PLANNER_SYSTEM_PROMPT)
    assert json.loads(call["prompt"])["workflowPhase"] == "planner"
    assert json.loads(call["prompt"])["untrustedQuestion"] == normalize_security_text(
        "你好，介绍一下 ReMail"
    )

    private_question = (
        "另一个项目 Genspark，邮件标题是 Welcome aboard，"
        "发件人是 OtherCorp，正文是 hello world"
    )
    asyncio.run(functions["_generate_fact_plan"](context, event, private_question))
    private_payload = context.llm_generate.await_args.kwargs["prompt"]
    for private_value in ("Genspark", "Welcome aboard", "OtherCorp", "hello world"):
        assert private_value not in private_payload
    assert "邮件详情已隐藏" in private_payload

    recent_private = (
        "邮件标题是 Previous welcome，发件人是 PreviousCorp，"
        "正文是 previous private body"
    )
    asyncio.run(
        functions["_generate_fact_plan"](context, event, "那现在呢？", recent_private)
    )
    recent_payload = context.llm_generate.await_args.kwargs["prompt"]
    for private_value in (
        "Previous welcome",
        "PreviousCorp",
        "previous private body",
    ):
        assert private_value not in recent_payload
    assert "邮件详情已隐藏" in recent_payload

    context.llm_generate.return_value = SimpleNamespace(
        role="assistant", completion_text="REMAIL"
    )
    assert asyncio.run(
        functions["_generate_fact_plan"](context, event, "不是 JSON")
    ).failed

    is_command = functions["_is_remail_command"]
    for value in (
        "/项目 github",
        "!help",
        "！诊断 foo bar",
        "/绑定状态",
        "帮助 我看看天气",
        "项目 管理",
    ):
        assert is_command(value)
    for value in (
        "/weather",
        "!今天天气",
        "普通聊天",
        "",
    ):
        assert not is_command(value)


def test_dynamic_recharge_answer_is_personalized_by_second_llm() -> None:
    functions, _ = _load_welcome_functions()
    enforce_scope = functions["_enforce_answer_scope"]
    original = "当前兑换码购买地址：https://pay.example.test/cards。"
    scoped = enforce_scope("怎么买积分兑换码？", original)
    assert "https://pay.example.test/cards" in scoped
    recharge_data = {
        "enabled": True,
        "paymentMethods": ["alipay"],
        "minPoints": "100",
        "feeRate": "0.01",
        "feeCapPoints": "5",
        "redemptionCodePurchaseUrl": "https://pay.example.test/cards",
    }
    grounded = functions["_render_recharge_evidence"](recharge_data)
    plan = _fact_plan(
        intents=("recharge",),
        facts=(_fact("recharge", "recharge_config"),),
    )

    response = SimpleNamespace(role="assistant", completion_text=original)
    response_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": plan,
        "_remail_evidence_v1": {
            "recharge_config": {
                "observedAt": "2026-09-04T00:00:00Z",
                "params": {},
                "data": recharge_data,
                "valid": True,
                "history": [],
            }
        },
    }
    response_event = SimpleNamespace(
        message_str="怎么买积分兑换码？",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: response_extras.get(key, default),
        set_extra=lambda key, value: response_extras.__setitem__(key, value),
    )

    async def output_gate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            if payload["reviewMode"] == "facts":
                assert payload["candidateAnswer"] == normalize_security_text(scoped)
            else:
                assert payload["candidateAnswer"].startswith("我把当前配置理清了")
                assert payload["approvedAnswer"] == normalize_security_text(scoped)
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve",
                        "supportedEvidence": ["recharge"],
                        "violations": [],
                    }
                ),
            )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": "我把当前配置理清了：\n" + payload["authoritativeAnswer"],
                    "usedEvidence": ["recharge"],
                    "seals": [],
                },
                ensure_ascii=False,
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=output_gate),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), response_event, response
        )
    )
    assert response.completion_text == normalize_security_text(
        "我把当前配置理清了：\n" + scoped
    )
    assert response.completion_text != normalize_security_text(
        "我把当前配置理清了：\n" + grounded
    )
    assert context.get_current_chat_provider_id.await_count == 2
    assert all(
        call.args == (response_event.unified_msg_origin,)
        for call in context.get_current_chat_provider_id.await_args_list
    )
    assert context.llm_generate.await_count == 3
    facts_call = context.llm_generate.await_args_list[0].kwargs
    assert json.loads(facts_call["prompt"])["reviewMode"] == "facts"
    persona_call = context.llm_generate.await_args_list[1].kwargs
    assert persona_call["tools"] is None and persona_call["contexts"] is None
    assert persona_call["system_prompt"] == PERSONA_SYSTEM_PROMPT
    assert "agentDraft" not in json.loads(persona_call["prompt"])
    assert "evidence" not in json.loads(persona_call["prompt"])
    critic_call = context.llm_generate.await_args_list[2].kwargs
    assert critic_call["tools"] is None and critic_call["contexts"] is None
    assert critic_call["system_prompt"] == CRITIC_SYSTEM_PROMPT

    run_context = SimpleNamespace(
        messages=[
            SimpleNamespace(role="user", content="question", tool_calls=None),
            SimpleNamespace(role="assistant", content=original, tool_calls=None),
        ]
    )
    asyncio.run(
        functions["snapshot_safe_remail_response"](object(), response_event, response)
    )
    asyncio.run(
        functions["sync_safe_response_history"](
            object(), response_event, run_context, response
        )
    )
    assert run_context.messages[-1].content == response.completion_text

    final_extras = {"_remail_owned": True}
    final_response = SimpleNamespace(role="assistant", completion_text="安全答复。")
    functions["_replace_response_text"](final_response, "安全答复。")
    final_result = SimpleNamespace(chain=[("plain", "其他插件改写的 unsafe 文本")])
    final_event = SimpleNamespace(
        message_str="怎么使用？",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: final_extras.get(key, default),
        set_extra=lambda key, value: final_extras.__setitem__(key, value),
        get_result=lambda: final_result,
    )
    _support_result_api(final_event)
    asyncio.run(
        functions["snapshot_safe_remail_response"](
            object(), final_event, final_response
        )
    )
    final_response.completion_text = "后续 response hook 再次篡改。"
    final_extras["_llm_reasoning_content"] = "private chain of thought"
    asyncio.run(functions["finalize_safe_remail_result"](object(), final_event))
    final_event._remail_original_send.assert_awaited_once_with(
        [("plain", normalize_security_text("安全答复。"))]
    )
    assert final_event.get_result() is None and not final_event.is_stopped()
    asyncio.run(functions["finalize_safe_remail_result"](object(), final_event))
    assert final_event._remail_original_send.await_count == 1
    assert final_extras["_llm_reasoning_content"] == ""

    missing_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": _fact_plan(intents=("social",)),
    }
    missing_result = SimpleNamespace(chain=[("plain", "UNSAFE OTHER PROJECT MAIL")])
    missing_event = SimpleNamespace(
        message_str="普通问题",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: missing_extras.get(key, default),
        set_extra=lambda key, value: missing_extras.__setitem__(key, value),
        get_result=lambda: missing_result,
    )
    _support_result_api(missing_event)
    asyncio.run(functions["finalize_safe_remail_result"](object(), missing_event))
    missing_event._remail_original_send.assert_awaited_once_with(
        [("plain", normalize_security_text(functions["_REMAIL_SAFE_ERROR_TEXT"]))]
    )
    assert missing_event.get_result() is None and not missing_event.is_stopped()
    failed_response = SimpleNamespace(
        role="assistant",
        completion_text="那份内容确已抵达，只是你订购的方向并非这一类。",
    )
    asyncio.run(
        functions["snapshot_safe_remail_response"](
            object(), missing_event, failed_response
        )
    )
    assert failed_response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )

    extras = {}
    event = SimpleNamespace(
        is_at_or_wake_command=True,
        get_extra=lambda key, default=None: True if key == "_remail_owned" else default,
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    asyncio.run(functions["prepare_remail_llm_response"](object(), event))
    # A generic group wake flag is not a verified mention of the bot / management.
    assert extras["_remail_group_llm_allowed"] is False
    assert "_remail_input_prepared" not in extras
    assert event.is_at_or_wake_command is False

    private_extras = {}
    private_event = SimpleNamespace(
        is_at_or_wake_command=False,
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: True if key == "_remail_owned" else default,
        set_extra=lambda key, value: private_extras.__setitem__(key, value),
    )
    asyncio.run(functions["prepare_remail_llm_response"](object(), private_event))
    assert private_extras == {
        "_remail_input_prepared": True,
        "enable_streaming": False,
        "persona_custom_error_message": functions["_REMAIL_SAFE_ERROR_TEXT"],
        "_llm_error_message": functions["_REMAIL_SAFE_ERROR_TEXT"],
    }

    leaked_response = SimpleNamespace(
        role="assistant",
        completion_text=(
            "邮件主题是 Genspark account email verification，发件人来自 Microsoft，"
            "验证码是 768071。"
        ),
    )
    leaked_draft = leaked_response.completion_text
    public_plan = _fact_plan(intents=("social",))
    leaked_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": public_plan,
    }
    leaked_event = SimpleNamespace(
        message_str="帮我看看这封邮件",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: leaked_extras.get(key, default),
        set_extra=lambda key, value: leaked_extras.__setitem__(key, value),
    )

    async def reject_private_mail(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            assert payload["reviewMode"] == "facts"
            assert "768071" not in payload["candidateAnswer"]
            result = {
                "decision": "reject",
                "supportedEvidence": [],
                "violations": ["privacy_exposure"],
            }
        else:
            assert kwargs["system_prompt"] == FACT_REPAIR_SYSTEM_PROMPT
            assert payload["reviewFeedback"]["violations"] == ["privacy_exposure"]
            result = {
                "answer": sanitize_model_text(leaked_draft),
                "usedEvidence": [],
                "seals": [],
            }
        return SimpleNamespace(
            role="assistant", completion_text=json.dumps(result, ensure_ascii=False)
        )

    blocked_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=reject_private_mail),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=blocked_context), leaked_event, leaked_response
        )
    )
    assert leaked_response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )
    assert [
        call.kwargs["system_prompt"]
        for call in blocked_context.llm_generate.await_args_list
    ] == [CRITIC_SYSTEM_PROMPT, FACT_REPAIR_SYSTEM_PROMPT, CRITIC_SYSTEM_PROMPT]
    for private_value in ("Genspark", "Microsoft", "768071"):
        assert private_value not in leaked_response.completion_text
    assert leaked_response.result_chain == [("plain", leaked_response.completion_text)]

    unverified_response = SimpleNamespace(
        role="assistant", completion_text="这是 iCloud 项目，换 Outlook 邮箱即可。"
    )
    diagnosis_plan = _fact_plan(
        intents=("diagnosis",),
        facts=(
            _fact(
                "diagnosis",
                "code_diagnosis",
                params={"hasOrderEmail": True},
            ),
        ),
        answer_mode="diagnosis",
        privacy="group_sensitive",
    )
    unverified_event = SimpleNamespace(
        message_str="peptide_tech.3k@icloud.com 接不到码",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: (
            True
            if key == "_remail_owned"
            else diagnosis_plan
            if key == "_remail_intent_plan_v1"
            else default
        ),
    )
    unverified_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(),
        llm_generate=AsyncMock(),
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=unverified_context),
            unverified_event,
            unverified_response,
        )
    )
    assert unverified_response.completion_text == normalize_security_text(
        functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
    )
    unverified_context.llm_generate.assert_not_awaited()

    locked_response = SimpleNamespace(
        role="assistant",
        completion_text=(
            "该订单对应 ChatGPT 项目，但真正应该使用 Genspark 项目，验证码是 768071。"
        ),
    )
    locked_fact = DiagnosisFact(
        diagnosis_code="cause_not_confirmed",
        safe_message="暂未发现明确异常。",
        purchased_project_id=2,
        purchased_project_name="ChatGPT",
    )
    locked_event = SimpleNamespace(
        message_str="peptide_tech.3k@icloud.com 接不到码",
        unified_msg_origin="bot:GroupMessage:529642597",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: (
            True
            if key == "_remail_owned"
            else diagnosis_plan
            if key == "_remail_intent_plan_v1"
            else locked_fact
            if key == "_remail_code_diagnosis_fact"
            else {
                "code_diagnosis": {
                    "observedAt": "2026-09-04T00:00:00Z",
                    "params": {},
                    "data": locked_fact,
                    "valid": True,
                    "history": [],
                }
            }
            if key == "_remail_evidence_v1"
            else default
        ),
    )
    locked_runtime_extras = {}
    locked_original_get = locked_event.get_extra
    locked_event.get_extra = lambda key, default=None: locked_runtime_extras.get(
        key, locked_original_get(key, default)
    )
    locked_event.set_extra = lambda key, value: locked_runtime_extras.__setitem__(
        key, value
    )
    locked_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(), llm_generate=AsyncMock()
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=locked_context), locked_event, locked_response
        )
    )
    assert locked_response.completion_text == (
        "该订单对应的是 ChatGPT 项目。 暂未发现明确异常。"
    )
    assert "Genspark" not in locked_response.completion_text
    assert "768071" not in locked_response.completion_text
    assert locked_response.result_chain == [("plain", locked_response.completion_text)]
    locked_context.llm_generate.assert_awaited_once()


def test_owned_send_guard_and_history_fail_closed_without_canonical() -> None:
    functions, _ = _load_welcome_functions()
    delivered = []
    stopped = []
    extras = {"_remail_owned": True}

    async def raw_send(message, *_args, **_kwargs):
        delivered.append(message)

    event = SimpleNamespace(
        message_str="ReMail 怎么用？",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
        send=raw_send,
        stop_event=lambda: stopped.append(True),
    )
    _support_result_api(event)
    assert asyncio.run(functions["_install_owned_send_guard"](event)) is True
    asyncio.run(event.send([("plain", "Provider API base `https://secret.internal`")]))
    safe_error = normalize_security_text(functions["_REMAIL_SAFE_ERROR_TEXT"])
    assert delivered == [[("plain", safe_error)]]
    assert extras["_remail_canonical_response"] == safe_error
    assert stopped == [True]
    asyncio.run(event.send([("plain", "second unsafe direct send")]))
    assert len(delivered) == 1

    intermediate_delivered = []
    intermediate_extras = {
        "_remail_owned": True,
        "_remail_main_agent_ready": True,
    }

    async def intermediate_send(message, *_args, **_kwargs):
        intermediate_delivered.append(message)

    intermediate_event = SimpleNamespace(
        message_str="ReMail 怎么用？",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: intermediate_extras.get(key, default),
        set_extra=lambda key, value: intermediate_extras.__setitem__(key, value),
        send=intermediate_send,
        stop_event=lambda: pytest.fail("intermediate send must be suppressed"),
    )
    _support_result_api(intermediate_event)
    assert (
        asyncio.run(functions["_install_owned_send_guard"](intermediate_event)) is True
    )
    asyncio.run(intermediate_event.send([("plain", "skills-like intermediate")]))
    assert intermediate_delivered == []
    intermediate_extras["_remail_canonical_response"] = "最终安全答复。"
    asyncio.run(intermediate_event.send([("plain", "ignored final chain")]))
    assert intermediate_delivered == [[("plain", "最终安全答复。")]]

    terminal_delivered = []
    terminal_stopped = []
    terminal_extras = {
        "_remail_owned": True,
        "_remail_main_agent_ready": True,
    }

    async def terminal_send(message, *_args, **_kwargs):
        terminal_delivered.append(message)

    terminal_event = SimpleNamespace(
        message_str="ReMail 怎么用？",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: terminal_extras.get(key, default),
        set_extra=lambda key, value: terminal_extras.__setitem__(key, value),
        send=terminal_send,
        stop_event=lambda: terminal_stopped.append(True),
    )
    _support_result_api(terminal_event)
    assert asyncio.run(functions["_install_owned_send_guard"](terminal_event)) is True
    safe_framework_error = SimpleNamespace(
        chain=[SimpleNamespace(text=functions["_REMAIL_SAFE_ERROR_TEXT"])]
    )
    asyncio.run(terminal_event.send(safe_framework_error))
    assert terminal_delivered == [[("plain", safe_error)]]
    assert terminal_stopped == [True]

    canonical_delivered = []
    canonical_extras = {
        "_remail_owned": True,
        "_remail_canonical_response": "已通过门禁的答复。",
    }

    async def canonical_send(message, *_args, **_kwargs):
        canonical_delivered.append(message)

    canonical_event = SimpleNamespace(
        message_str="ReMail 怎么用？",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: canonical_extras.get(key, default),
        set_extra=lambda key, value: canonical_extras.__setitem__(key, value),
        send=canonical_send,
        stop_event=lambda: pytest.fail("canonical send must continue"),
    )
    _support_result_api(canonical_event)
    assert asyncio.run(functions["_install_owned_send_guard"](canonical_event)) is True
    asyncio.run(canonical_event.send([("plain", "unsafe replacement")]))
    assert canonical_delivered == [[("plain", "已通过门禁的答复。")]]

    history_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": _fact_plan(intents=("social",)),
    }
    history_event = SimpleNamespace(
        message_str="普通问题",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: history_extras.get(key, default),
        set_extra=lambda key, value: history_extras.__setitem__(key, value),
    )
    history_response = SimpleNamespace(
        role="assistant", completion_text="UNSAFE OTHER PROJECT MAIL"
    )
    run_context = SimpleNamespace(
        messages=[
            SimpleNamespace(
                role="assistant",
                content="UNSAFE OTHER PROJECT MAIL",
                tool_calls=None,
            )
        ]
    )
    asyncio.run(
        functions["sync_safe_response_history"](
            object(), history_event, run_context, history_response
        )
    )
    expected = functions["_REMAIL_SAFE_ERROR_TEXT"]
    assert history_extras["_remail_canonical_response"] == expected
    assert history_response.completion_text == expected
    assert run_context.messages[-1].content == expected


def test_owned_result_guard_preserves_stop_and_does_not_republish_review_rejection():
    functions, _ = _load_welcome_functions()
    extras = {"_remail_owned": True, "_remail_main_agent_ready": True}
    event = _support_result_api(
        SimpleNamespace(
            message_str="ReMail 怎么用？",
            get_message_type=lambda: "friend",
            get_extra=lambda key, default=None: extras.get(key, default),
            set_extra=lambda key, value: extras.__setitem__(key, value),
        )
    )
    assert asyncio.run(functions["_install_owned_send_guard"](event))
    original_chain = [functions["Plain"]("未经门禁的中间草稿")]
    intermediate = _ResultStub(
        original_chain, result_content_type=_RESULT_TYPES.LLM_RESULT
    )
    event.set_result(intermediate)
    assert intermediate.chain == [] and len(original_chain) == 1
    assert not event.is_stopped()
    terminal = _ResultStub([functions["Plain"](functions["_REMAIL_SAFE_ERROR_TEXT"])])
    event.set_result(terminal)
    assert (
        terminal.chain
        and terminal.chain[0].text == functions["_REMAIL_SAFE_ERROR_TEXT"]
    )
    extras["_remail_canonical_response"] = "已通过门禁的最终答复。"
    final = _ResultStub(
        [functions["Plain"]("其他装饰器的替换")],
        result_content_type=_RESULT_TYPES.LLM_RESULT,
    )
    event.set_result(final)
    assert final.chain == [functions["Plain"](extras["_remail_canonical_response"])]
    rejection = _ResultStub([functions["Plain"]("宿主内容审核已拒绝此回复")])
    event.set_result(rejection)
    assert rejection.chain == []
    event.stop_event()
    asyncio.run(functions["finalize_safe_remail_result"](object(), event))
    assert event.get_result() is None and event.is_stopped()
    asyncio.run(event.send([functions["Plain"]("停止后不能继续发出答案")]))
    event._remail_original_send.assert_not_awaited()


def test_agent_draft_is_primary_and_semantic_critic_fails_closed() -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("price",),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
            ),
        ),
        privacy="private",
        entities={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
    )
    extras = {"_remail_owned": True, "_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str="ChatGPT 的 iCloud 接码现在多少钱？",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "projectName": "ChatGPT",
                    "targetPlatform": "ChatGPT",
                    "productType": "icloud",
                    "productLabel": "iCloud",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                    "purchaseEnabled": False,
                }
            ],
        },
        {"productTypes": ["icloud"]},
    )
    agent_draft = "ChatGPT 的 iCloud 接码现在是 20 积分。"

    async def approve_gate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            assert agent_draft in payload["candidateAnswer"]
            assert payload["requiredEvidence"] == (
                [] if payload["reviewMode"] == "facts" else ["price"]
            )
            assert payload["factPlan"]["entities"] == {
                "projectQuery": "ChatGPT",
                "productTypes": ["icloud"],
            }
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve",
                        "supportedEvidence": ["price"],
                        "violations": [],
                    }
                ),
            )
        assert "agentDraft" not in payload and "evidence" not in payload
        assert payload["authoritativeAnswer"] == normalize_security_text(agent_draft)
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": "红夜直接说：" + payload["authoritativeAnswer"],
                    "usedEvidence": ["price"],
                    "seals": [],
                },
                ensure_ascii=False,
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=approve_gate),
    )
    response = SimpleNamespace(role="assistant", completion_text=agent_draft)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert response.completion_text == normalize_security_text(
        "红夜直接说：" + agent_draft
    )
    assert "当前项目价格(单位" not in response.completion_text
    assert context.llm_generate.await_count == 3

    dangerous = "那份东西已经送达，只是你购买的业务方向不对应。"

    async def reject_gate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            assert dangerous in payload["candidateAnswer"]
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "reject",
                        "supportedEvidence": [],
                        "violations": ["diagnosis_without_evidence"],
                    }
                ),
            )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {"answer": dangerous, "usedEvidence": [], "seals": []},
                ensure_ascii=False,
            ),
        )

    social_plan = _fact_plan(intents=("social",), privacy="private")
    social_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": social_plan,
    }
    social_event = SimpleNamespace(
        message_str="这个收件地址怎么一点动静都没有",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: social_extras.get(key, default),
        set_extra=lambda key, value: social_extras.__setitem__(key, value),
    )
    reject_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=reject_gate),
    )
    rejected = SimpleNamespace(role="assistant", completion_text=dangerous)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=reject_context), social_event, rejected
        )
    )
    assert rejected.completion_text == normalize_security_text(
        functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
    )
    assert "送达" not in rejected.completion_text
    reject_context.llm_generate.assert_not_awaited()

    social_event.message_str = "ReMail 怎么使用？"

    async def redact_gate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            if payload["reviewMode"] == "facts":
                assert payload["candidateAnswer"] == "普通草稿。"
            else:
                assert payload["reviewMode"] == "delivery"
                assert "Welcome aboard" in payload["candidateAnswer"]
                assert payload["approvedAnswer"] == "普通草稿。"
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve"
                        if payload["reviewMode"] == "facts"
                        else "reject",
                        "supportedEvidence": [],
                        "violations": []
                        if payload["reviewMode"] == "facts"
                        else ["unsupported_claim"],
                    }
                ),
            )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": "邮件主题是 Welcome aboard。",
                    "usedEvidence": [],
                    "seals": [],
                },
                ensure_ascii=False,
            ),
        )

    redact_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=redact_gate),
    )
    redacted = SimpleNamespace(role="assistant", completion_text="普通草稿。")
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=redact_context), social_event, redacted
        )
    )
    assert "Welcome aboard" not in redacted.completion_text
    assert redacted.completion_text == "普通草稿。"

    leaked_values = (
        "隔壁业务叫 Genspark，编号为 9，筛选式为 other.test；"
        "抬头 Welcome aboard；寄出方 OtherCorp；内容 hello world；"
        "那串六位数字是 768071。"
    )

    async def malicious_approval(**kwargs):
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            payload = json.loads(kwargs["prompt"])
            facts_review = payload["reviewMode"] == "facts"
            if facts_review:
                assert payload["candidateAnswer"] == "普通草稿。"
            else:
                assert payload["reviewMode"] == "delivery"
                assert payload["candidateAnswer"] == normalize_security_text(
                    leaked_values
                )
                assert payload["approvedAnswer"] == "普通草稿。"
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve" if facts_review else "reject",
                        "supportedEvidence": [],
                        "violations": [] if facts_review else ["unsupported_claim"],
                    }
                ),
            )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {"answer": leaked_values, "usedEvidence": [], "seals": []},
                ensure_ascii=False,
            ),
        )

    malicious_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=malicious_approval),
    )
    malicious = SimpleNamespace(role="assistant", completion_text="普通草稿。")
    social_extras.pop("_remail_persona_attempts", None)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=malicious_context), social_event, malicious
        )
    )
    assert malicious.completion_text == "普通草稿。"
    for private_value in (
        "Genspark",
        "other.test",
        "Welcome aboard",
        "OtherCorp",
        "hello world",
        "768071",
    ):
        assert private_value not in malicious.completion_text
    assert malicious_context.llm_generate.await_count == 5


@pytest.mark.parametrize(
    ("question", "candidate", "source", "text", "needs_inference"),
    [
        (
            "这两项差多少积分？",
            "两项相差 15 积分。",
            "project_prices",
            "两项当前价格分别为 20 积分、35 积分。",
            True,
        ),
        (
            "接码能用几次？",
            "接码只接收 1 次。",
            "policy.business",
            "接码是短期单次服务。",
            True,
        ),
        (
            "我有 27 积分，可以用吗？",
            "你提到有 27 积分，需要结合当前项目价格判断。",
            "policy.business",
            "服务使用 ReMail 积分余额支付。",
            False,
        ),
        (
            "接码窗口多久？",
            "当前窗口为 1 小时。",
            "projects",
            "当前项目的接码窗口为 60 分钟。",
            False,
        ),
    ],
)
@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_approved_numeric_writer_output_gets_fidelity_review_without_recomputing(
    question, candidate, source, text, needs_inference, decision
) -> None:
    functions, _ = _load_welcome_functions()
    extras = {}
    event = SimpleNamespace(
        unified_msg_origin="bot:FriendMessage:123456789",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        assert kwargs["tools"] is None and kwargs["contexts"] is None
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {"answer": candidate, "usedEvidence": ["checked"], "seals": []},
                    ensure_ascii=False,
                ),
            )
        assert kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT
        assert payload["candidateAnswer"] == normalize_security_text(candidate)
        assert payload["approvedAnswer"] == normalize_security_text(candidate)
        assert payload["reviewMode"] == "delivery"
        assert payload["factPlan"]["verificationHints"] == {
            "numericInferenceNeeded": False
        }
        assert payload["requiredEvidence"] == ["checked"]
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "decision": decision,
                    "supportedEvidence": ["checked"] if decision == "approve" else [],
                    "violations": []
                    if decision == "approve"
                    else ["unsupported_claim"],
                }
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    actual = asyncio.run(
        functions["_generate_persona_answer"](
            context,
            event,
            question=question,
            agent_draft=candidate,
            authoritative_answer=candidate,
            evidence={"checked": evidence_block(source, text)},
            required_evidence_ids=("checked",),
            fact_plan={"answer_mode": "normal"},
        )
    )
    assert context.llm_generate.await_count == 2
    assert actual == (
        normalize_security_text(candidate) if decision == "approve" else ""
    )


@pytest.mark.parametrize(
    "candidate",
    [
        "银河项目价格 10 积分；星火项目价格 20 积分。",
        "星云项目价格 10 积分。",
    ],
)
def test_unquoted_chinese_project_mismatches_require_semantic_rejection(
    candidate,
) -> None:
    functions, _ = _load_welcome_functions()
    extras = {}
    authoritative = "星火项目价格 10 积分；银河项目价格 20 积分。"

    async def generate(**kwargs):
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {"answer": candidate, "usedEvidence": ["prices"], "seals": []},
                    ensure_ascii=False,
                ),
            )
        assert kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT
        payload = json.loads(kwargs["prompt"])
        assert payload["candidateAnswer"] == normalize_security_text(candidate)
        assert (
            normalize_security_text(authoritative) in payload["evidence"][0]["summary"]
        )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "decision": "reject",
                    "supportedEvidence": [],
                    "violations": ["reversed_relation"],
                }
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    actual = asyncio.run(
        functions["_generate_persona_answer"](
            context,
            SimpleNamespace(
                unified_msg_origin="bot:FriendMessage:123456789",
                get_extra=lambda key, default=None: extras.get(key, default),
                set_extra=lambda key, value: extras.__setitem__(key, value),
            ),
            question="星火和银河分别多少钱？",
            agent_draft=authoritative,
            authoritative_answer=authoritative,
            evidence={"prices": evidence_block("project_prices", authoritative)},
            required_evidence_ids=("prices",),
            fact_plan={"answer_mode": "normal"},
        )
    )
    assert context.llm_generate.await_count == 2
    assert actual == ""


@pytest.mark.parametrize(
    "candidate",
    [
        "访问 https://evil.example/v1/open/orders，等待 15 秒。",
        "使用 `UNEXPECTED_TOKEN`，等待 15 秒。",
        "Genspark 的接码价格为 15 积分。",
        "password=sentinel-password",
    ],
)
def test_numeric_writer_rejects_unknown_urls_tokens_projects_and_credentials(
    candidate,
) -> None:
    functions, _ = _load_welcome_functions()
    extras = {}

    async def reject_unproved_answer(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            assert not contains_credentials(candidate)
            assert payload["reviewMode"] == "delivery"
            assert payload["candidateAnswer"] == normalize_security_text(candidate)
            assert payload["approvedAnswer"] == "ChatGPT 接码当前为 20 积分。"
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "reject",
                        "supportedEvidence": [],
                        "violations": ["unsupported_claim"],
                    }
                ),
            )
        assert kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {"answer": candidate, "usedEvidence": [], "seals": []},
                ensure_ascii=False,
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=reject_unproved_answer),
    )
    actual = asyncio.run(
        functions["_generate_persona_answer"](
            context,
            SimpleNamespace(
                unified_msg_origin="bot:FriendMessage:123456789",
                get_extra=lambda key, default=None: extras.get(key, default),
                set_extra=lambda key, value: extras.__setitem__(key, value),
            ),
            question="ChatGPT 的接码价格是多少？",
            agent_draft="ChatGPT 接码当前为 20 积分。",
            authoritative_answer="ChatGPT 接码当前为 20 积分。",
            evidence={
                "price": evidence_block(
                    "project_prices", "ChatGPT 接码当前为 20 积分。"
                )
            },
            fact_plan={"answer_mode": "normal"},
        )
    )
    assert actual == ""
    assert context.llm_generate.await_count == (
        1 if contains_credentials(candidate) else 2
    )


@pytest.mark.parametrize(
    "critic_failure", ["reject", "unsupported_approval", "malformed", "exception"]
)
def test_rejected_numeric_writer_recovery_requires_critic_approval(
    critic_failure,
) -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("price",),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
            ),
        ),
        privacy="private",
        entities={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
    )
    extras = {"_remail_owned": True, "_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str="ChatGPT 的 iCloud 接码现在多少钱？",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "projectName": "ChatGPT",
                    "productType": "icloud",
                    "productLabel": "iCloud",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                    "purchaseEnabled": False,
                }
            ],
        },
        {"productTypes": ["icloud"]},
    )
    functions["_record_evidence"](
        event,
        "faqs",
        {
            "sourceValid": True,
            "enabled": True,
            "items": [{"question": "价格", "answer": "ChatGPT 接码价格为 999 积分。"}],
        },
        {"background": True},
    )
    approved = "ChatGPT 的 iCloud 接码当前为 20 积分。"
    candidate = "ChatGPT 的 iCloud 接码已经确定为 20 积分。"

    async def generate(**kwargs):
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {"answer": candidate, "usedEvidence": ["price"], "seals": []},
                    ensure_ascii=False,
                ),
            )
        assert kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT
        payload = json.loads(kwargs["prompt"])
        if payload["reviewMode"] == "facts":
            assert payload["candidateAnswer"] == normalize_security_text(approved)
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve",
                        "supportedEvidence": ["price"],
                        "violations": [],
                    }
                ),
            )
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        assert not payload["factPlan"]["verificationHints"]["numericInferenceNeeded"]
        if critic_failure == "exception":
            raise RuntimeError("untrusted provider detail")
        raw = (
            "invalid JSON"
            if critic_failure == "malformed"
            else json.dumps(
                {
                    "decision": "approve"
                    if critic_failure == "unsupported_approval"
                    else "reject",
                    "supportedEvidence": [],
                    "violations": []
                    if critic_failure == "unsupported_approval"
                    else ["unsupported_claim"],
                }
            )
        )
        return SimpleNamespace(role="assistant", completion_text=raw)

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    response = SimpleNamespace(role="assistant", completion_text=approved)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert context.llm_generate.await_count == 5
    assert re.search(
        r"20\s*积分", functions["_grounded_dynamic_answer"](event, event.message_str)
    )
    assert response.completion_text == normalize_security_text(approved)
    assert all(
        value not in response.completion_text
        for value in ("15", "999", "untrusted provider detail")
    )


def test_public_project_evidence_cannot_authorize_mail_ownership_claim() -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("project",),
        facts=(_fact("project", "projects", params={"projectQuery": "Genspark"}),),
        entities={"projectQuery": "Genspark"},
    )
    extras = {"_remail_owned": True, "_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str="介绍一下 Genspark 项目",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "projects",
        {
            "items": [
                {
                    "id": 9,
                    "name": "Genspark",
                    "targetPlatform": "Genspark",
                    "products": [],
                }
            ],
            "total": 1,
        },
        {"search": "Genspark", "offset": 0},
    )
    fabricated = (
        "那份内容确已抵达，归在 #9 Genspark 这一类；只是你订购的方向并非这一类。"
    )
    assert functions["_DIAGNOSIS_ASSERTION"].search(normalize_security_text(fabricated))

    async def malicious_gate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            assert payload["candidateAnswer"] == normalize_security_text(
                "当前可见项目是 #9 Genspark。"
                if payload["reviewMode"] == "facts"
                else fabricated
            )
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "approve",
                        "supportedEvidence": ["project"],
                        "violations": [],
                    }
                ),
            )
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": fabricated,
                    "usedEvidence": ["project"],
                    "seals": [],
                },
                ensure_ascii=False,
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=malicious_gate),
    )
    response = SimpleNamespace(
        role="assistant", completion_text="当前可见项目是 #9 Genspark。"
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert context.llm_generate.await_count == 3
    assert response.completion_text == normalize_security_text(
        functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
    )
    for unsupported_relation in ("抵达", "归在", "订购的方向"):
        assert unsupported_relation not in response.completion_text


@pytest.mark.parametrize(
    ("extra_intent", "extra_fact", "entities", "claim", "data", "params"),
    [
        (
            "project",
            _fact("other", "projects", params={"projectQuery": "Genspark"}),
            {"projectQuery": "Genspark"},
            "projects",
            {
                "items": [
                    {
                        "id": 9,
                        "name": "Genspark",
                        "targetPlatform": "Genspark",
                        "products": [],
                    }
                ],
                "total": 1,
            },
            {"search": "Genspark", "offset": 0},
        ),
        (
            "price",
            _fact(
                "other",
                "project_prices",
                params={"projectQuery": "Genspark", "productTypes": ["icloud"]},
            ),
            {"projectQuery": "Genspark", "productTypes": ["icloud"]},
            "project_prices",
            {
                "sourceValid": True,
                "matched": True,
                "prices": [
                    {
                        "projectId": 9,
                        "projectName": "Genspark",
                        "targetPlatform": "Genspark",
                        "productType": "icloud",
                        "productLabel": "iCloud",
                        "codeEnabled": True,
                        "codePricePoints": "999",
                        "purchaseEnabled": False,
                    }
                ],
            },
            {"productTypes": ["icloud"]},
        ),
    ],
)
def test_diagnosis_seal_excludes_other_planned_business_facts(
    extra_intent: str,
    extra_fact: dict[str, Any],
    entities: dict[str, Any],
    claim: str,
    data: dict[str, Any],
    params: dict[str, Any],
) -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("diagnosis", extra_intent),
        facts=(
            _fact(
                "diagnosis",
                "code_diagnosis",
                params={"hasOrderEmail": True},
            ),
            extra_fact,
        ),
        answer_mode="diagnosis",
        privacy="private",
        entities=entities,
    )
    diagnosis = DiagnosisFact(
        diagnosis_code="project_mismatch",
        safe_message="邮箱实际已经收到邮件，但该邮件不匹配你购买的项目。",
        purchased_project_id=2,
        purchased_project_name="ChatGPT",
    )
    extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": plan,
        "_remail_code_diagnosis_fact": diagnosis,
    }
    event = SimpleNamespace(
        message_str="这个订单邮箱一直没信",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](event, "code_diagnosis", diagnosis, {})
    functions["_record_evidence"](event, claim, data, params)

    async def writer(**kwargs):
        payload = json.loads(kwargs["prompt"])
        assert kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT
        assert "evidence" not in payload and "agentDraft" not in payload
        assert payload["requiredEvidence"] == ["diagnosis"]
        assert "Genspark" not in payload["authoritativeAnswer"]
        assert "999" not in payload["authoritativeAnswer"]
        token = payload["immutableSeals"][0]
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": token,
                    "usedEvidence": ["diagnosis"],
                    "seals": [token],
                }
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=writer),
    )
    response = SimpleNamespace(
        role="assistant",
        completion_text="Genspark #9 当前价格 999 积分。",
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    context.llm_generate.assert_awaited_once()
    assert response.completion_text == render_diagnosis_fact(diagnosis)
    for other_project_value in ("Genspark", "#9", "999"):
        assert other_project_value not in response.completion_text


def test_diagnosis_fact_is_required_and_terminal() -> None:
    functions, _ = _load_welcome_functions()

    def run(
        question: str,
        draft: str,
        *,
        group: bool,
        extras: dict | None = None,
        approve: bool = False,
    ):
        values = {
            "_remail_owned": True,
            "_remail_intent_plan_v1": _fact_plan(intents=("social",)),
            **(extras or {}),
        }
        event = SimpleNamespace(
            message_str=question,
            unified_msg_origin="bot:FriendMessage:123456789",
            get_message_type=lambda: "group" if group else "friend",
            get_extra=lambda key, default=None: values.get(key, default),
            set_extra=lambda key, value: values.__setitem__(key, value),
        )
        response = SimpleNamespace(role="assistant", completion_text=draft)
        plugin = SimpleNamespace()
        if approve:

            async def output_gate(**kwargs):
                payload = json.loads(kwargs["prompt"])
                if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
                    return SimpleNamespace(
                        role="assistant",
                        completion_text=json.dumps(
                            {
                                "decision": "approve",
                                "supportedEvidence": [],
                                "violations": [],
                            }
                        ),
                    )
                return SimpleNamespace(
                    role="assistant",
                    completion_text=json.dumps(
                        {
                            "answer": payload["authoritativeAnswer"],
                            "usedEvidence": [],
                            "seals": [],
                        },
                        ensure_ascii=False,
                    ),
                )

            plugin.context = SimpleNamespace(
                get_current_chat_provider_id=AsyncMock(return_value="provider"),
                llm_generate=AsyncMock(side_effect=output_gate),
            )
        asyncio.run(
            functions["enforce_redemption_channel_priority"](plugin, event, response)
        )
        return event, response.completion_text

    for question, draft, group in (
        (
            "这个订单邮箱一直没信",
            "邮箱实际已经收到 Genspark 的邮件，你买错项目了。",
            False,
        ),
        (
            "邮件迟迟不来",
            "实际已到件，属于 Genspark，你买错项目了。",
            True,
        ),
        (
            "这个订单邮箱一直没信",
            "邮箱已经到件，属于 Genspark，你买错项目了。",
            True,
        ),
        ("帮我看看", "邮件属于 Genspark 项目，已经到件。", False),
        ("查一下 order@example.com", "它属于 Genspark。", False),
        ("怎么一直等不到邮件", "其实来信了，只是服务选错了。", False),
        ("邮箱怎么一直空的", "信已经进来了，是业务选岔了。", True),
        (
            "为何还没进收件箱",
            "那封东西其实早就投递成功，只是下单时选偏了。",
            False,
        ),
        ("怎么还没落到里面", "内容已妥投，订购的套餐不对路。", False),
        ("为何看不到那一封", "东西在里面，所选业务走岔了。", True),
    ):
        _, result = run(question, draft, group=group)
        assert result == normalize_security_text(
            functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
        )
        assert "Genspark" not in result

    _, follow_up = run(
        "那现在呢？",
        "已经到件，是另一个项目。",
        group=False,
        extras={"_remail_same_sender_context": "这个订单邮箱一直没信"},
    )
    assert follow_up == normalize_security_text(
        functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"]
    )
    _, ordinary_receipt = run(
        "反馈提交了吗？", "已经收到你的反馈。", group=False, approve=True
    )
    assert ordinary_receipt == normalize_security_text("已经收到你的反馈。")
    _, ordinary_information = run(
        "处理完成了吗？", "我相信已经收到信息。", group=False, approve=True
    )
    assert ordinary_information == normalize_security_text("我相信已经收到信息。")

    mismatch_fact = DiagnosisFact(
        diagnosis_code="project_mismatch",
        safe_message="",
        purchased_project_id=2,
        purchased_project_name="ChatGPT",
    )
    diagnosis_plan = _fact_plan(
        intents=("diagnosis",),
        facts=(
            _fact(
                "diagnosis",
                "code_diagnosis",
                params={"hasOrderEmail": True},
            ),
        ),
        answer_mode="diagnosis",
        privacy="private",
    )
    diagnosis_evidence = {
        "code_diagnosis": {
            "observedAt": "2026-09-04T00:00:00Z",
            "params": {},
            "data": mismatch_fact,
            "valid": True,
            "history": [],
        }
    }
    _, empty_result = run(
        "这个订单邮箱一直没信",
        "",
        group=False,
        extras={
            "_remail_intent_plan_v1": diagnosis_plan,
            "_remail_code_diagnosis_fact": mismatch_fact,
            "_remail_evidence_v1": diagnosis_evidence,
        },
    )
    assert "ChatGPT 项目" in empty_result
    assert "实际已经收到邮件" in empty_result
    assert "项目买错了" in empty_result
    assert "unsafe" not in empty_result

    mixed_question = "这个订单没收到码，ChatGPT 当前价格多少？"
    mixed_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": _fact_plan(
            intents=("diagnosis", "price"),
            facts=(
                _fact(
                    "diagnosis",
                    "code_diagnosis",
                    params={"hasOrderEmail": True},
                ),
                _fact(
                    "price",
                    "project_prices",
                    params={"projectQuery": "ChatGPT", "productTypes": []},
                ),
            ),
            answer_mode="diagnosis",
            privacy="private",
            entities={"projectQuery": "ChatGPT"},
        ),
        "_remail_code_diagnosis_fact": mismatch_fact,
        "_remail_evidence_v1": diagnosis_evidence,
    }
    mixed_event = SimpleNamespace(
        message_str=mixed_question,
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: mixed_extras.get(key, default),
        set_extra=lambda key, value: mixed_extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        mixed_event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "projectName": "ChatGPT",
                    "productType": "microsoft",
                    "productLabel": "Outlook",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                    "purchaseEnabled": False,
                }
            ],
        },
        {"productTypes": []},
    )
    mixed_response = SimpleNamespace(role="assistant", completion_text="wrong draft")
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(), mixed_event, mixed_response
        )
    )
    assert "ChatGPT 项目" in mixed_response.completion_text
    assert "项目买错了" in mixed_response.completion_text
    assert "接码 20 积分" not in mixed_response.completion_text
    assert "unsafe" not in mixed_response.completion_text

    non_mismatch = DiagnosisFact(
        diagnosis_code="cause_not_confirmed",
        safe_message="暂未发现明确异常。",
        purchased_project_id=2,
        purchased_project_name="ChatGPT",
    )
    mixed_extras["_remail_code_diagnosis_fact"] = non_mismatch
    mixed_extras["_remail_evidence_v1"]["code_diagnosis"]["data"] = non_mismatch

    async def malicious_persona(**kwargs):
        payload = json.loads(kwargs["prompt"])
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": payload["authoritativeAnswer"]
                    + "\n邮件已经进来了，类别选岔了。",
                    "usedEvidence": payload["requiredEvidence"],
                    "seals": payload["immutableSeals"],
                },
                ensure_ascii=False,
            ),
        )

    persona_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=malicious_persona),
    )
    malicious_response = SimpleNamespace(role="assistant", completion_text="unsafe")
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=persona_context), mixed_event, malicious_response
        )
    )
    assert "暂未发现明确异常" in malicious_response.completion_text
    assert "接码 20 积分" not in malicious_response.completion_text
    assert "进来了" not in malicious_response.completion_text
    assert "选岔" not in malicious_response.completion_text


def test_conflicting_fact_sources_fall_back_only_to_strong_facts() -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("price", "announcement"),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
            ),
            _fact("notice", "announcements"),
        ),
        privacy="private",
        entities={"projectQuery": "ChatGPT", "productTypes": ["icloud"]},
    )
    extras = {"_remail_owned": True, "_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str="ChatGPT iCloud 当前价格和公告怎么说？",
        unified_msg_origin="bot:FriendMessage:1",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "projectName": "ChatGPT",
                    "productType": "icloud",
                    "productLabel": "iCloud",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                    "purchaseEnabled": False,
                }
            ],
        },
        {"productTypes": ["icloud"]},
    )
    functions["_record_evidence"](
        event,
        "announcements",
        {
            "sourceValid": True,
            "notice": "",
            "announcements": [{"title": "旧公告", "content": "旧公告写着 99 积分。"}],
            "truncated": False,
        },
        {},
    )

    approved = "ChatGPT 的 iCloud 接码当前为 20 积分，旧公告不能证明现价。"
    original = "当前接码价格以旧公告的 99 积分为准。"
    fact_checks = 0

    async def malicious_persona(**kwargs):
        nonlocal fact_checks
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
            if payload["reviewMode"] == "delivery":
                assert payload["candidateAnswer"] == normalize_security_text(
                    approved + "实际现价为 99 积分。"
                )
                assert payload["approvedAnswer"] == normalize_security_text(approved)
                return SimpleNamespace(
                    role="assistant",
                    completion_text=json.dumps(
                        {
                            "decision": "reject",
                            "supportedEvidence": [],
                            "violations": ["provenance_error"],
                        }
                    ),
                )
            assert payload["reviewMode"] == "facts"
            fact_checks += 1
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "reject" if fact_checks == 1 else "approve",
                        "supportedEvidence": []
                        if fact_checks == 1
                        else ["price", "notice"],
                        "violations": ["provenance_error"] if fact_checks == 1 else [],
                    }
                ),
            )
        if kwargs["system_prompt"] == FACT_REPAIR_SYSTEM_PROMPT:
            assert payload["authoritativeAnswer"] == normalize_security_text(original)
            assert payload["reviewFeedback"]["violations"] == ["provenance_error"]
            answer = approved
        else:
            assert kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT
            assert "evidence" not in payload and "agentDraft" not in payload
            assert payload["authoritativeAnswer"] == normalize_security_text(approved)
            answer = approved + "实际现价为 99 积分。"
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "answer": answer,
                    "usedEvidence": ["price", "notice"],
                    "seals": [],
                },
                ensure_ascii=False,
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=malicious_persona),
    )
    response = SimpleNamespace(role="assistant", completion_text=original)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert fact_checks == 2
    assert context.llm_generate.await_count == 7
    assert response.completion_text == normalize_security_text(approved)
    assert "99" not in response.completion_text


def test_dynamic_answer_without_tool_evidence_is_blocked() -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("price",),
        facts=(_fact("price", "project_prices", params={"productTypes": ["icloud"]}),),
        entities={"productTypes": ["icloud"], "projectQuery": "iCloud"},
    )
    response = SimpleNamespace(
        role="assistant", completion_text="iCloud 当前价格是 99 积分。"
    )
    extras = {"_remail_owned": True, "_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str="iCloud 当前价格多少？",
        unified_msg_origin="bot:GroupMessage:1",
        get_message_type=lambda: "group",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant", completion_text="invalid JSON"
            )
        ),
    )

    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )

    assert response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )
    assert "99" not in response.completion_text
    assert context.llm_generate.await_count == 1


def test_intent_plan_binds_and_renders_combined_system_facts() -> None:
    functions, _ = _load_welcome_functions()
    question = "ChatGPT 当前价格和项目 ID 2 的 Outlook 后缀精确库存是多少？"
    extras = {"_remail_owned": True}
    event = SimpleNamespace(
        message_str=question,
        unified_msg_origin="bot:FriendMessage:1",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    plan = _fact_plan(
        intents=("price", "inventory"),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "ChatGPT", "productTypes": ["microsoft"]},
            ),
            _fact(
                "project",
                "projects",
                params={"projectQuery": "ChatGPT"},
            ),
            _fact(
                "stock",
                "project_inventory",
                params={"projectId": 2, "productTypes": ["microsoft"]},
                depends_on=("project",),
            ),
        ),
        privacy="private",
        entities={
            "projectQuery": "ChatGPT",
            "productTypes": ["microsoft"],
            "projectId": 2,
        },
    )
    extras["_remail_intent_plan_v1"] = plan
    assert set(plan.required) == {
        "project_prices",
        "projects",
        "project_inventory",
    }
    assert plan.product_types == ("microsoft",)

    functions["_record_evidence"](
        event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "projectName": "ChatGPT",
                    "productType": "microsoft",
                    "productLabel": "Outlook",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                    "purchaseEnabled": False,
                }
            ],
        },
        {"productTypes": ["microsoft"]},
    )
    functions["_record_evidence"](
        event,
        "projects",
        {"items": [{"id": 2, "name": "ChatGPT", "products": []}], "total": 1},
        {"search": "ChatGPT", "offset": 0},
    )
    functions["_record_evidence"](
        event,
        "project_inventory",
        {
            "projectId": 2,
            "observedAt": datetime.now(timezone.utc).isoformat(),
            "totalAvailable": 9,
            "products": [
                {
                    "productType": "microsoft",
                    "totalAvailable": 9,
                    "publicAvailable": 8,
                    "codeAvailable": 7,
                    "codePublicAvailable": 6,
                    "purchaseAvailable": 2,
                    "purchasePublicAvailable": 2,
                    "suffixes": [
                        {
                            "suffix": "outlook.com",
                            "totalAvailable": 4,
                            "publicAvailable": 3,
                        }
                    ],
                }
            ],
        },
        {"projectId": 2},
    )
    response = SimpleNamespace(
        role="assistant", completion_text="价格 999 积分，库存 999。"
    )
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(), event, response
        )
    )
    recovery = normalize_security_text(
        functions["_grounded_dynamic_answer"](event, event.message_str)
    )
    assert recovery.startswith("当前项目价格")
    assert "接码 20 积分" in recovery
    assert "outlook.com:总 4,公共 3" in recovery
    assert response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )
    assert "999" not in response.completion_text
    scoped_inventory = functions["_render_inventory_evidence"](
        {
            "projectId": 2,
            "observedAt": datetime.now(timezone.utc).isoformat(),
            "totalAvailable": 99,
            "products": [
                {
                    "productType": "icloud",
                    "totalAvailable": 99,
                    "publicAvailable": 98,
                    "suffixes": [
                        {
                            "suffix": "private-other.example",
                            "totalAvailable": 99,
                            "publicAvailable": 98,
                        }
                    ],
                }
            ],
        },
        ("microsoft",),
    )
    assert "没有查询到以下类型：Outlook" in scoped_inventory
    assert "iCloud" not in scoped_inventory
    assert "private-other.example" not in scoped_inventory
    assert "总库存：99" not in scoped_inventory


def test_validated_fact_plan_drives_scoped_evidence_rendering() -> None:
    functions, _ = _load_welcome_functions()
    project = _fact_plan(
        intents=("project",),
        facts=(
            _fact(
                "project",
                "projects",
                params={"projectQuery": "ChatGPT"},
            ),
        ),
        entities={"projectQuery": "ChatGPT"},
    )
    assert project.required == ("projects",)
    duration = project
    assert duration.required == ("projects",)
    duration_text = functions["_render_projects_evidence"](
        {
            "items": [
                {
                    "id": 2,
                    "name": "ChatGPT",
                    "products": [
                        {
                            "type": "icloud",
                            "status": "enabled",
                            "codeEnabled": True,
                            "purchaseEnabled": True,
                            "codeWindowMinutes": 10,
                            "activationWindowMinutes": 30,
                            "warrantyMinutes": 60,
                        }
                    ],
                }
            ]
        },
        duration,
    )
    assert "接码窗口 10 分钟" in duration_text
    assert "激活窗口 30 分钟" in duration_text
    assert "质保 60 分钟" in duration_text
    faq_text = functions["_render_faq_evidence"](
        {
            "items": [
                {
                    "question": "购买邮箱是什么？",
                    "answer": "购买邮箱可持续收件。旧质保 24 小时。",
                }
            ]
        },
        duration,
    )
    assert "可持续收件" in faq_text
    assert (
        "24 小时" in faq_text
    )  # Reference text is retained; its weak source cannot override projects.

    wrong_project_entry = {
        "valid": True,
        "params": {"search": "Claude"},
        "data": {
            "items": [{"id": 3, "name": "Claude", "products": []}],
            "total": 1,
        },
    }
    assert not functions["_entry_matches_plan"](
        wrong_project_entry, "projects", project
    )
    disabled = functions["_render_projects_evidence"](
        {
            "items": [
                {
                    "id": 2,
                    "name": "ChatGPT",
                    "products": [
                        {
                            "type": "icloud",
                            "status": "disabled",
                            "codeEnabled": True,
                            "purchaseEnabled": True,
                            "codeWindowMinutes": 10,
                            "warrantyMinutes": 60,
                        }
                    ],
                }
            ]
        },
        project,
    )
    assert "接码 关闭" in disabled and "购买 关闭" in disabled
    assert "接码窗口" not in disabled and "质保 60" not in disabled

    disabled_recharge = functions["_render_recharge_evidence"](
        {
            "enabled": False,
            "paymentMethods": [],
            "tiers": [],
            "redemptionCodePurchaseUrl": "https://current.example/cards",
        }
    )
    assert "在线充值未开放" in disabled_recharge
    assert "https://current.example/cards" in disabled_recharge

    stock = _fact_plan(
        intents=("inventory",),
        facts=(
            _fact("project", "projects", params={"productTypes": ["icloud"]}),
            _fact(
                "stock",
                "project_inventory",
                params={"projectId": 2, "productTypes": ["icloud"]},
                depends_on=("project",),
            ),
        ),
        entities={"productTypes": ["icloud"], "projectId": 2},
    )
    rendered = functions["_render_projects_evidence"](
        {
            "items": [
                {
                    "id": 2,
                    "name": "ChatGPT",
                    "products": [
                        {
                            "type": "icloud",
                            "status": "enabled",
                            "codeEnabled": True,
                            "purchaseEnabled": True,
                            "publicAvailable": 0,
                        },
                        {
                            "type": "microsoft",
                            "status": "enabled",
                            "codeEnabled": True,
                            "purchaseEnabled": True,
                            "publicAvailable": 99,
                        },
                    ],
                }
            ]
        },
        stock,
    )
    assert "iCloud" in rendered
    assert "公共库存 0" in rendered
    assert "Outlook" not in rendered
    assert functions["_contains_dynamic_literal"]("每单八分。")
    assert functions["_contains_dynamic_literal"]("现在可以买。")
    assert "20 积分" in functions["_enforce_answer_scope"](
        "ChatGPT 一单几分？", "ChatGPT 接码 20 积分。"
    )
    assert "公共库存 5" in functions["_enforce_answer_scope"](
        "iCloud 还剩几份？", "iCloud 公共库存 5。"
    )


def test_inventory_tool_can_query_another_verified_project_without_closing_original_fact() -> (
    None
):
    functions, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    tool = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_project_inventory"
    )
    tool.decorator_list = []
    namespace = {
        "AstrMessageEvent": object,
        "FactPlan": FactPlan,
        "_REMAIL_INTENT_PLAN_KEY": "_remail_intent_plan_v1",
        "_evidence_entries": functions["_evidence_entries"],
        "_project_items_for_plan": functions["_project_items_for_plan"],
        "_inventory_observation_is_fresh": functions["_inventory_observation_is_fresh"],
        "_record_evidence": functions["_record_evidence"],
        "_safe_push_value": functions["_safe_push_value"],
        "json": json,
    }
    exec(
        compile(ast.Module(body=[tool], type_ignores=[]), "main.py", "exec"), namespace
    )

    plan = _fact_plan(
        intents=("inventory",),
        facts=(
            _fact("project", "projects", params={"projectQuery": "Project A"}),
            _fact(
                "stock",
                "project_inventory",
                params={"projectId": 2},
                depends_on=("project",),
            ),
        ),
        entities={"projectQuery": "Project A", "projectId": 2},
    )
    extras = {
        "_remail_intent_plan_v1": plan,
        "_remail_evidence_v1": {
            "projects": {
                "valid": True,
                "params": {},
                "data": {
                    "items": [
                        {"id": 2, "name": "Project A"},
                        {"id": 3, "name": "Project B"},
                    ],
                    "total": 2,
                },
                "history": [],
            }
        },
    }
    event = SimpleNamespace(
        message_str="Project A 后缀精确库存",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    plugin = SimpleNamespace(
        _request=AsyncMock(
            return_value={
                "projectId": 3,
                "observedAt": datetime.now(timezone.utc).isoformat(),
                "products": [],
            }
        )
    )
    result = asyncio.run(namespace["remail_project_inventory"](plugin, event, 3))
    assert json.loads(result)["projectId"] == 3
    plugin._request.assert_awaited_once()
    assert not functions["_fact_is_satisfied"](event, plan.facts[1], plan)


def test_truncated_api_fact_requires_and_exposes_react_supplement() -> None:
    functions, _ = _load_welcome_functions()
    question = "如何下单并查询订单？"
    plan = _fact_plan(
        intents=("api",),
        facts=(_fact("api", "api_documentation", params={"query": question}),),
        answer_mode="public_api",
        privacy="private",
    )
    extras = {"_remail_intent_plan_v1": plan}
    event = SimpleNamespace(
        message_str=question,
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "api_documentation",
        {
            "sourceValid": True,
            "matched": True,
            "truncated": True,
            "operations": [{"method": "POST", "path": "/v1/open/orders"}],
        },
        {"query": question},
    )
    assert "公开 API 契约" in functions["_missing_evidence_response"](event, question)
    functions["_record_evidence"](
        event,
        "api_documentation",
        {
            "sourceValid": True,
            "matched": True,
            "truncated": False,
            "operations": [{"method": "GET", "path": "/v1/open/orders/{orderNo}"}],
        },
        {"query": "GetOrder schema"},
    )
    assert functions["_missing_evidence_response"](event, question) == ""
    packet = functions["_persona_evidence_packet"](event, plan)
    assert isinstance(packet["api"], PublicAPIContract)
    initial_contract = json.loads(evidence_text(packet["api"]))
    assert initial_contract["operations"] == [
        {"method": "POST", "path": "/v1/open/orders"}
    ]
    assert initial_contract["truncated"] is True
    supplements = [
        value
        for key, value in packet.items()
        if key.startswith("tool.api_documentation.")
    ]
    assert len(supplements) == 1
    assert isinstance(supplements[0], PublicAPIContract)
    supplement = json.loads(evidence_text(supplements[0]))
    assert supplement["operations"] == [
        {"method": "GET", "path": "/v1/open/orders/{orderNo}"}
    ]
    assert supplement["truncated"] is False


def test_evidence_and_output_gate_reject_unproved_dynamic_literals() -> None:
    functions, _ = _load_welcome_functions()

    def reject_draft(event, response, violation):
        draft = response.completion_text
        stages = []

        async def review(**kwargs):
            assert kwargs["tools"] is None and kwargs["contexts"] is None
            payload = json.loads(kwargs["prompt"])
            if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
                assert payload["reviewMode"] == "facts"
                stages.append("facts")
                result = {
                    "decision": "reject",
                    "supportedEvidence": [],
                    "violations": [violation],
                }
            else:
                assert kwargs["system_prompt"] == FACT_REPAIR_SYSTEM_PROMPT
                assert payload["reviewFeedback"]["violations"] == [violation]
                stages.append("fact_repair")
                result = {"answer": draft, "usedEvidence": [], "seals": []}
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(result, ensure_ascii=False),
            )

        context = SimpleNamespace(
            get_current_chat_provider_id=AsyncMock(return_value="provider"),
            llm_generate=AsyncMock(side_effect=review),
        )
        asyncio.run(
            functions["enforce_redemption_channel_priority"](
                SimpleNamespace(context=context), event, response
            )
        )
        assert stages == ["facts", "fact_repair", "facts"]
        assert context.llm_generate.await_count == 3
        assert response.completion_text == normalize_security_text(
            functions["_REMAIL_SAFE_ERROR_TEXT"]
        )

    api_question = "API 怎么控制宇宙飞船？"
    api_plan = _fact_plan(
        intents=("api",),
        facts=(
            _fact(
                "api",
                "api_documentation",
                params={"query": api_question},
            ),
        ),
        answer_mode="public_api",
        privacy="private",
    )
    extras = {
        "_remail_owned": True,
        "_remail_api_consultation": True,
        "_remail_intent_plan_v1": api_plan,
    }
    event = SimpleNamespace(
        message_str=api_question,
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        event,
        "api_documentation",
        {"sourceValid": True, "matched": False, "operations": []},
        {"query": event.message_str},
    )
    assert functions["_missing_evidence_response"](event, event.message_str) == ""
    assert json.loads(functions["_grounded_dynamic_answer"](
        event, event.message_str
    )) == {"sourceValid": True, "matched": False, "operations": []}
    functions["_record_evidence"](
        event,
        "api_documentation",
        {
            "sourceValid": True,
            "matched": True,
            "operations": [{"method": "GET", "path": "/v1/open/wallet"}],
        },
        {"query": "wallet"},
    )
    scoped_api = functions["_grounded_dynamic_answer"](event, event.message_str)
    assert json.loads(scoped_api) == {
        "sourceValid": True, "matched": False, "operations": []
    }
    assert "/v1/open/wallet" not in scoped_api
    assert not functions["_evidence_is_valid"](
        "project_inventory",
        {
            "projectId": 3,
            "observedAt": "2000-01-01T00:00:00Z",
            "products": [],
        },
        {"projectId": 2},
    )

    recharge_question = "卡网链接是什么，如何兑换？"
    recharge_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": _fact_plan(
            intents=("recharge", "faq"),
            facts=(
                _fact("recharge", "recharge_config"),
                _fact("faq", "faqs"),
            ),
            privacy="private",
        ),
    }
    recharge_event = SimpleNamespace(
        message_str=recharge_question,
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: recharge_extras.get(key, default),
        set_extra=lambda key, value: recharge_extras.__setitem__(key, value),
    )
    functions["_record_evidence"](
        recharge_event,
        "recharge_config",
        {
            "sourceValid": True,
            "enabled": True,
            "paymentMethods": ["alipay"],
            "redemptionCodePurchaseUrl": "https://current.example/cards",
        },
        {},
    )
    functions["_record_evidence"](
        recharge_event,
        "faqs",
        {
            "sourceValid": True,
            "enabled": True,
            "items": [
                {"question": "如何兑换", "answer": "访问 www.evil.test/cards 后兑换"}
            ],
            "truncated": False,
        },
        {},
    )
    response = SimpleNamespace(
        role="assistant", completion_text="访问 www.evil.test/cards 购买。"
    )
    reject_draft(recharge_event, response, "provenance_error")
    assert "https://current.example/cards" in functions["_grounded_dynamic_answer"](
        recharge_event, recharge_event.message_str
    )
    assert response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )
    assert "evil.test" not in response.completion_text

    # The unified egress gate is disabled. Keep its normalization contract
    # separate from the dedicated helpers and the active semantic review below.
    safe = functions["_safe_egress_text"]
    privacy = functions["_enforce_group_privacy"]
    black_box = functions["_enforce_black_box"]
    requests_credentials = functions["_requests_credentials"]
    tutorial = "调用公开 API 时，请在 Bearer 请求头提供 API Key。"
    assert safe(tutorial, is_group=False) == normalize_security_text(tutorial)
    client_storage = "客户端可以将业务数据保存在 PostgreSQL。"
    assert safe(
        client_storage, is_group=False, question="API 客户端如何保存业务数据？"
    ) == normalize_security_text(client_storage)
    client_cache = "客户端可以用 Redis 缓存公开 API 响应。"
    assert safe(
        client_cache, is_group=False, question="API 客户端可以缓存响应吗？"
    ) == normalize_security_text(client_cache)
    assert safe(
        "请按公开订单查询流程操作。", is_group=False, question="你怎么查询订单？"
    ) == normalize_security_text("请按公开订单查询流程操作。")
    assert safe(
        "充值方式以当前配置为准。", is_group=False, question="你怎么处理充值？"
    ) == normalize_security_text("充值方式以当前配置为准。")
    assert (
        black_box(
            "实现方式是先执行 SQL JOIN orders 与 messages。",
            question="你们后台如何匹配邮件？",
        )
        == functions["_BLACK_BOX_RESPONSE"]
    )
    assert (
        privacy("邮件标题「Secret launch」来自客服。")
        == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    )
    assert (
        privacy("这封邮件来自 Microsoft，里面写着 768071。")
        == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    )
    for mail_detail in (
        "Microsoft 给这个邮箱发了一封验证码邮件，六位码为 768071。",
        "Genspark account verification 是这封信的标题。",
        "刚到的信由 Microsoft 发出，校验数字为 768071。",
        "收到一封来自 other-project.example 的验证信，码为 768071。",
        "微软发的信里有 768071。",
        "寄信方为 Microsoft，验证数字是 768071。",
        "寄件者是 Microsoft。",
        "信上写的是 768071。",
        "邮件抬头是 Genspark account verification。",
        "Microsoft 寄来的数字是 768071。",
        "微软那边给的是 768071。",
        "Microsoft 寄了个 768071。",
        "发送方 Microsoft，内容 768071。",
        "from Microsoft, code 768071",
        "微软发来：768071。",
        "Microsoft -> 768071",
        "Microsoft -> ABC123",
    ):
        assert (
            privacy(mail_detail, question="这封邮件是什么？")
            == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
        )
    for question, answer in (
        ("驗證碼是多少？", "ABC123"),
        ("郵件主旨是什麼？", "Welcome aboard"),
        ("寄件者是誰？", "微軟"),
        ("內文寫了什麼？", "Verify your account"),
    ):
        assert (
            privacy(answer, question=question)
            == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
        )
    assert redact_personal_data("联系电话 13800138000") == "联系电话 [手机号已隐藏]"
    assert redact_personal_data("身份证 110101199001011234") == (
        "身份证 [身份证号已隐藏]"
    )
    for internal in ("意图识别", "ReAct", "输出门禁"):
        assert (
            black_box(f"内部采用{internal}处理。")
            == functions["_BLACK_BOX_RESPONSE"]
        )
    for internal in (
        "请求会先经过 /v1/bot/context，再读取 Redis 缓存和队列。",
        "代码放在 core/service 模块，先读 redis cache 再进入 job queue。",
        "使用 BotDiagnosisService 处理，缓存键叫 diagnosis_v1。",
        "Thought 我需要查询；Action call_project；Observation 已返回。",
        "数据落在 PostgreSQL，任务交给 Kafka，处理器是 DiagnosisController。",
        "请求先走 /internal/bot/context，再查 Memcached。",
        "thought 先查项目；action call_project；observation done。",
        "使用 DiagnosisManager 处理，再写入 NATS stream。",
        "ReMail 后台用 PostgreSQL 保存订单。",
        "ReMail 依赖 Postgres 持久化数据。",
        "消息交给 Pulsar topic，再由 Worker 消费。",
        "请求进入 /private/bot/context 后读取 KeyDB。",
        "处理器是 DiagnosisProcessor，数据存在 CockroachDB。",
        "analysis 先查；tool 调项目；result 返回。",
        "内部用 Aurora 和 SQS 处理订单。",
        "邮件资源由 ACME Ltd. 提供。",
        "我先调用查询功能，再根据返回内容回答。",
    ):
        assert black_box(internal) == functions["_BLACK_BOX_RESPONSE"]

    for question, answer in (
        ("发件人是谁？", "Microsoft"),
        ("邮件标题呢？", "Genspark account verification"),
        ("谁寄来的？", "no-reply service"),
        ("正文说了什么？", "Please verify your account"),
        ("这个码哪来的？", "Microsoft"),
        ("发我验证码的是谁？", "Microsoft"),
        ("这条验证信是哪家发的？", "Genspark"),
        ("验证邮件哪家的？", "Genspark"),
        ("验证码是什么？", "ABCDEF"),
        ("验证码呢？", "ABCD-EFGH"),
        ("校验码多少？", "A B C D"),
        ("哪家发来的？", "Microsoft"),
        ("哪个服务发的？", "Genspark"),
        ("寄自哪里？", "Microsoft"),
        ("来源？", "Microsoft"),
        ("哪家的？", "Genspark"),
        ("寄件者是哪家？", "Microsoft"),
        ("那封东西写了啥？", "Verify your account。"),
        ("API 字段之外，那个码是哪来的？", "Microsoft"),
        ("公开 API 字段，然后说说上一封信的发件人。", "Microsoft"),
        ("接口 schema 顺便告诉我验证码来源。", "768071"),
        ("API body 定义和刚才那个码来源都说下。", "768071"),
    ):
        group_extras = {
            "_remail_owned": True,
            "_remail_intent_plan_v1": _fact_plan(
                intents=(),
                answer_mode="refuse_group_mail",
                privacy="group_sensitive",
            ),
        }
        group_event = SimpleNamespace(
            message_str=question,
            unified_msg_origin="bot:GroupMessage:123456789",
            get_message_type=lambda: "group",
            get_extra=lambda key, default=None: group_extras.get(key, default),
            set_extra=lambda key, value: group_extras.__setitem__(key, value),
        )
        response = SimpleNamespace(role="assistant", completion_text=answer)
        reject_draft(group_event, response, "privacy_exposure")
        assert answer not in response.completion_text

    for question, answer in (
        ("公开 API 的邮件正文 body 字段是什么？", "body: string"),
        ("API 客户端如何解析邮件正文？", "body: string"),
        ("公开 API 的邮件标题字段是什么？", "subject: string"),
        ("接口响应里的邮件正文 schema 怎么定义？", "body: string"),
        ("邮件发件人字段如何读取？", "sender: string"),
        ("公开 API 的 sender 字段代表谁？", "sender: string"),
        ("公开 API 的 from 字段表示谁发的？", "from: string"),
        ("公开 API 返回的 subject 是这条邮件的哪个字段？", "subject: string"),
        ("公开 API 如何读取我的邮箱资源？", "mailboxId: string"),
    ):
        assert privacy(answer, question=question) == normalize_security_text(answer)

    for question, answer in (
        ("后端用什么数据库？", "PostgreSQL"),
        ("缓存用什么？", "KeyDB"),
        ("队列呢？", "Pulsar"),
        ("供应商是谁？", "ACME Ltd."),
        ("代码仓库在哪？", "GitLab private repo"),
        ("你们是 Redis 还是 MySQL？", "Redis。"),
        ("ReMail 跑在 Kubernetes 上吗？", "Kubernetes。"),
        ("消息中间件选 Kafka 还是 Pulsar？", "Kafka。"),
        ("日志存哪里？", "Loki"),
        ("部署在哪？", "Kubernetes"),
        ("监控用什么？", "Prometheus"),
        ("运行在哪个云？", "AWS"),
        ("安全审计怎么做？", "内部使用 Snyk"),
        ("你们的客户端用 Redis 还是 MySQL？", "Redis。"),
        ("ReMail 客户端用什么缓存？", "Redis。"),
        ("红夜的客户端跑在 Kubernetes 吗？", "Kubernetes。"),
        ("服务器 IP 是什么？", "10.0.0.12"),
        ("机器部署地址？", "cn-shanghai-prod-01"),
        ("你们跟谁合作？", "ACME Ltd."),
        ("邮件资源哪来的？", "Provider X"),
        ("合作的是哪家公司？", "ACME Ltd."),
    ):
        private_extras = {
            "_remail_owned": True,
            "_remail_intent_plan_v1": _fact_plan(
                intents=(), answer_mode="refuse_internal", privacy="private"
            ),
        }
        private_event = SimpleNamespace(
            message_str=question,
            unified_msg_origin="bot:FriendMessage:123456789",
            get_message_type=lambda: "friend",
            get_extra=lambda key, default=None: private_extras.get(key, default),
            set_extra=lambda key, value: private_extras.__setitem__(key, value),
        )
        response = SimpleNamespace(role="assistant", completion_text=answer)
        reject_draft(private_event, response, "internal_exposure")
        assert answer not in response.completion_text

    for question, answer in (
        ("公开 API 的 supplier 供应商字段是什么？", "supplier: string"),
        ("公开 API 的 Cache-Control 缓存字段是什么？", "Cache-Control: string"),
        ("客户端缓存用什么？", "Redis。"),
        ("公开 API 客户端代码如何使用 Redis 缓存响应？", "客户端可使用 Redis。"),
        (
            "我的客户端架构怎么用 PostgreSQL 存储 API 结果？",
            "客户端可使用 PostgreSQL。",
        ),
        ("我的后端客户端如何用 PostgreSQL 缓存 API 响应？", "PostgreSQL。"),
        ("SDK 缓存用什么？", "Redis。"),
        ("调用方缓存怎么做？", "可使用本地缓存。"),
        ("前端缓存选哪个？", "可使用浏览器缓存。"),
        ("我的客户端怎么查数据？", "可用 ORM 或 SQL SELECT 查询本地数据。"),
    ):
        assert black_box(answer, question=question) == normalize_security_text(answer)

    for question, draft in (
        (
            "公开 API 客户端代码如何使用 Redis 缓存响应？",
            "客户端可使用 Redis 缓存公开响应。",
        ),
        (
            "我的客户端架构怎么用 PostgreSQL 存储 API 结果？",
            "我的客户端可用 ORM 或 SQL SELECT 存储本地 API 结果。",
        ),
    ):
        client_extras = {
            "_remail_owned": True,
            "_remail_api_consultation": True,
            "_remail_intent_plan_v1": _fact_plan(
                intents=("social",),
                answer_mode="client_guidance",
                privacy="private",
            ),
        }
        client_event = SimpleNamespace(
            message_str=question,
            unified_msg_origin="bot:FriendMessage:123456789",
            get_message_type=lambda: "friend",
            get_extra=lambda key, default=None: client_extras.get(key, default),
            set_extra=lambda key, value: client_extras.__setitem__(key, value),
        )
        response = SimpleNamespace(role="assistant", completion_text=draft)
        stages = []

        async def output_gate(**kwargs):
            assert kwargs["tools"] is None and kwargs["contexts"] is None
            payload = json.loads(kwargs["prompt"])
            if kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT:
                stages.append(payload["reviewMode"])
                assert payload["candidateAnswer"] == normalize_security_text(draft)
                if payload["reviewMode"] == "delivery":
                    assert payload["approvedAnswer"] == normalize_security_text(draft)
                return SimpleNamespace(
                    role="assistant",
                    completion_text=json.dumps(
                        {
                            "decision": "approve",
                            "supportedEvidence": [],
                            "violations": [],
                        }
                    ),
                )
            assert kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT
            stages.append("writer")
            assert payload["authoritativeAnswer"] == normalize_security_text(draft)
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "answer": payload["authoritativeAnswer"],
                        "usedEvidence": [],
                        "seals": [],
                    },
                    ensure_ascii=False,
                ),
            )

        plugin = SimpleNamespace(
            context=SimpleNamespace(
                get_current_chat_provider_id=AsyncMock(return_value="provider"),
                llm_generate=AsyncMock(side_effect=output_gate),
            )
        )
        asyncio.run(
            functions["enforce_redemption_channel_priority"](
                plugin, client_event, response
            )
        )
        assert response.completion_text == normalize_security_text(draft)
        assert stages == ["facts", "writer", "delivery"]

    for request in (
        "请在这里发送 API Key。",
        "把密码发我。",
        "把验证码告诉我。",
        "发来你的 Token。",
        "麻烦贴一下完整 Cookie。",
        "把密钥贴出来。",
        "请展示一下 Authorization。",
        "请把 API Key 贴一下。",
        "把验证码念给我听。",
        "把密码回我。",
        "把 Token 丢过来。",
        "给我看看 Cookie。",
        "我需要你的 API Key。",
        "API Key 放在 Authorization 请求头，然后把真实值发给我。",
        "Cookie 开启 HttpOnly，然后把完整 Cookie 告诉我。",
        "密码保存在客户端以后，再把密码发我。",
        "Token 使用 Bearer 鉴权；配置好后把 Token 贴出来。",
        "把密码交给我。",
        "上传你的 API Key。",
        "回复一下 Token。",
        "把验证码说一下。",
        "我得看一下你的 Cookie。",
        "把 Authorization 复制过来。",
        "Send me your API Key.",
        "Please paste your Token.",
    ):
        assert requests_credentials(request)
    for placeholder_request in (
        "请发送 <API_KEY> 占位符。",
        "请私聊发送 /绑定 <ReMail邮箱> <密码>。",
        "Please send ${TOKEN} as the placeholder.",
    ):
        assert not requests_credentials(placeholder_request)
        assert safe(placeholder_request, is_group=False) == normalize_security_text(
            placeholder_request
        )

    unplanned_extras = {
        "_remail_owned": True,
        "_remail_intent_plan_v1": _fact_plan(intents=("social",), privacy="private"),
    }
    unplanned = SimpleNamespace(
        message_str="你好",
        unified_msg_origin="bot:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: unplanned_extras.get(key, default),
        set_extra=lambda key, value: unplanned_extras.__setitem__(key, value),
    )
    unplanned_response = SimpleNamespace(
        role="assistant", completion_text="请访问 www.evil.test，当前价格 88 元。"
    )
    reject_draft(unplanned, unplanned_response, "unsupported_claim")
    assert unplanned_response.completion_text == normalize_security_text(
        functions["_REMAIL_SAFE_ERROR_TEXT"]
    )


def test_answer_scope_preserves_complete_content_for_react_review() -> None:
    functions, _ = _load_welcome_functions()
    enforce = functions["_enforce_answer_scope"]
    # This formatter cannot make business decisions or delete an approved clause.
    original = (
        "购买是长效服务，接码是短期单次服务。\n\n"
        "这不代表账号永不封禁，也不能保证永久免费。\n"
        "可能是激活窗口，也可能是质保，目前不能只凭数字确定。\n"
        "你看到的是订单邮箱的激活时间还是质保时间？"
    )
    for question in ("iCloud邮箱能用多久？", "只能用24小时吗？", "为什么还没补货？"):
        assert enforce(question, original) == original
        assert functions["_safe_egress_text"](
            original, is_group=False, question=question, enforce_scope=True
        ) == normalize_security_text(original)
    assert enforce("价格多少？", "当前价格 20 积分。") == "当前价格 20 积分。"
    assert functions["_scope_question"]("谢谢", "iCloud现在价格多少？") == "谢谢"
    assert functions["_scope_question"]("Outlook 呢？", "iCloud现在价格多少？") == (
        "iCloud现在价格多少？\nOutlook 呢？"
    )


def test_group_privacy_and_output_gate_are_deterministic() -> None:
    functions, _ = _load_welcome_functions()
    privacy = functions["_enforce_group_privacy"]

    exposed = (
        "联系邮箱：user@example.com\n订单号 ORD_12345\n验证码：654321\n"
        "API Key: sk_example_secret"
    )
    hidden = privacy(exposed)
    for secret in ("user@example.com", "ORD_12345", "654321", "sk_example_secret"):
        assert secret not in hidden
    assert "[邮箱已隐藏]" in hidden
    assert "[订单信息已隐藏]" in hidden
    assert "[敏感信息已隐藏]" in hidden
    hardened = privacy(
        '{"password":"hunter2","api_key":"rm_live_secret"}\n'
        "密码是 中文密码；订单号是 ORD_12345；邮箱 user@exa\u200bmple.com\n"
        "余额：888.88 积分；账号分组：VIP 9；角色：管理员"
    )
    for secret in (
        "hunter2",
        "rm_live_secret",
        "中文密码",
        "ORD_12345",
        "user@example.com",
        "888.88",
        "VIP 9",
    ):
        assert secret not in hardened
    placeholders = privacy(
        "Authorization: Bearer <API_KEY>\napi_key: ${API_KEY}\npassword: <PASSWORD>"
    )
    assert "Bearer <API_KEY>" in placeholders
    assert "${API_KEY}" in placeholders
    assert "<PASSWORD>" in placeholders

    management = functions["_enforce_answer_scope"](
        "这个问题怎么办？",
        "你可以私聊群主 1362626064 继续跟进。管理员QQ号：9845248。",
    )
    assert "私聊群主" in management
    assert "1362626064" not in privacy(management)
    assert "9845248" not in privacy("管理员QQ号：9845248")
    handoff = functions["_enforce_answer_scope"](
        "接码怎么用？", "ReMail 相关问题直接找红夜，非必要不要打扰群主。"
    )
    assert "找红夜" in handoff and "不要打扰群主" in handoff

    leaked_mail = (
        "你好，邮件已经收到。主题是 Genspark account email verification，"
        "发件人来自 Microsoft on behalf of Genspark，验证码是 768071。"
        "邮箱 peptide_tech.3k@icloud.com。"
    )
    assert privacy(leaked_mail) == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    for secret in (
        "Genspark account email verification",
        "Microsoft on behalf of Genspark",
        "768071",
        "peptide_tech.3k@icloud.com",
    ):
        assert secret not in privacy(leaked_mail)

    black_box = functions["_enforce_black_box"]
    assert (
        black_box("数据库显示上游返回了供应商字段。")
        == functions["_BLACK_BOX_RESPONSE"]
    )
    assert black_box("相关内部实现不对外提供。") == functions["_BLACK_BOX_RESPONSE"]
    assert (
        black_box("内部实现不对外，但数据库和上游信息如下。")
        == functions["_BLACK_BOX_RESPONSE"]
    )
    assert black_box("公开枚举值是 supplier。") == "公开枚举值是 supplier。"
    assert (
        privacy("邮件标题叫 Secret，发送方 Example，代码 ABC123。")
        == functions["_GROUP_PRIVATE_MAIL_RESPONSE"]
    )

    diagnosis = functions["_enforce_diagnosis_fact"]
    fact = {
        "diagnosisCode": "cause_not_confirmed",
        "projectId": 2,
        "projectName": "ChatGPT",
        "message": "另一个项目 Genspark；邮件主题 Welcome；正文 PRIVATE_BODY",
    }
    corrected = diagnosis("这是 iCloud 项目，截图属于 Genspark 项目。", fact)
    assert "ChatGPT 项目" in corrected
    assert "iCloud 项目" not in corrected
    assert "Genspark 项目" not in corrected
    assert (
        diagnosis("该订单对应 ChatGPT 项目，但真正应该使用 Genspark 项目。", fact)
        == corrected
    )
    mismatch = diagnosis(
        "泄漏另一个项目和邮件内容",
        {
            "diagnosisCode": "project_mismatch",
            "result": "project_mismatch",
            "projectId": 2,
            "projectName": "Purchased Project",
            "message": "unsafe",
            "mailReceived": True,
            "projectMismatch": True,
        },
    )
    assert "你购买的是 Purchased Project 项目" in mismatch
    assert "项目买错了" in mismatch
    assert "实际已经收到邮件" in mismatch
    assert "另一个项目" not in mismatch
    assert "unsafe" not in mismatch


def test_project_price_tool_supports_multiple_types_and_uses_point_units() -> None:
    functions, _ = _load_welcome_functions()
    normalize = functions["_normalize_product_types"]
    project_view = functions["_project_price_view"]
    enforce_units = functions["_enforce_project_price_units"]
    assert project_view(None, ()) == {}
    assert project_view({"items": [{"id": 2, "name": "ChatGPT"}], "total": 1}, ()) == {}
    requested = normalize("iCloud / 微软邮箱 / 域名邮箱")
    assert set(requested) == {"icloud", "microsoft", "domain"}

    payload = {
        "total": 2,
        "items": [
            {
                "id": 7,
                "name": "OpenAI",
                "targetPlatform": "openai",
                "products": [
                    {
                        "type": "icloud",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": True,
                        "codePrice": "12",
                        "purchasePrice": "15",
                        "effectiveCodePrice": "10",
                        "effectivePurchasePrice": "13",
                        "publicAvailable": 43000,
                    },
                    {
                        "type": "microsoft",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": False,
                        "effectiveCodePrice": "8",
                        "publicAvailable": 1000,
                    },
                ],
            },
            {
                "id": 8,
                "name": "Cloudflare",
                "targetPlatform": "cloudflare",
                "products": [
                    {
                        "type": "domain",
                        "status": "enabled",
                        "codeEnabled": True,
                        "purchaseEnabled": True,
                        "effectiveCodePrice": "4",
                        "effectivePurchasePrice": "9",
                        "publicAvailable": 2000,
                    }
                ],
            },
        ],
    }
    view = project_view(payload, requested)
    assert view["unit"] == "ReMail积分"
    assert view["matched"] is True
    assert {item["productType"] for item in view["prices"]} == {
        "icloud",
        "microsoft",
        "domain",
    }
    icloud = next(item for item in view["prices"] if item["productType"] == "icloud")
    assert icloud["codePricePoints"] == "10"
    assert icloud["purchasePricePoints"] == "13"
    grounded = functions["_render_price_evidence"](
        view, "iCloud、Outlook 和域名邮箱价格"
    )
    assert "OpenAI / iCloud：接码 10 积分；购买邮箱 13 积分" in grounded
    assert "OpenAI / Outlook：接码 8 积分" in grounded
    targeted_missing = functions["_render_price_evidence"](
        view, "MissingProject iCloud", "MissingProject"
    )
    assert "没有查询到匹配" in targeted_missing
    assert "OpenAI" not in targeted_missing
    assert "Cloudflare" not in targeted_missing
    similar_names = {
        "prices": [
            {
                "projectId": 1,
                "projectName": "GPT",
                "productType": "icloud",
                "productLabel": "iCloud",
                "codeEnabled": True,
                "codePricePoints": "10",
            },
            {
                "projectId": 2,
                "projectName": "ChatGPT",
                "productType": "icloud",
                "productLabel": "iCloud",
                "codeEnabled": True,
                "codePricePoints": "20",
            },
            {
                "projectId": 3,
                "projectName": "Discord",
                "productType": "icloud",
                "productLabel": "iCloud",
                "codeEnabled": True,
                "codePricePoints": "30",
            },
        ]
    }
    exact_name = functions["_render_price_evidence"](
        similar_names, "ChatGPT iCloud", "ChatGPT"
    )
    assert "ChatGPT / iCloud" in exact_name
    assert "GPT / iCloud" not in exact_name.replace("ChatGPT / iCloud", "")
    assert "Discord" not in exact_name
    exact_id = functions["_render_price_evidence"](similar_names, "iCloud", "", 2)
    assert "ChatGPT / iCloud" in exact_id
    assert "Discord" not in exact_id
    redundant_product_plan = _fact_plan(
        intents=("price",),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "iCloud", "productTypes": ["icloud"]},
            ),
        ),
        entities={"projectQuery": "iCloud", "productTypes": ["icloud"]},
    )
    product_only = functions["_render_evidence_claim"](
        "project_prices", view, redundant_product_plan
    )
    assert "OpenAI / iCloud" in product_only
    assert "结果不完整" in functions["_render_price_evidence"](
        {
            "prices": [
                {
                    "projectName": "Claude",
                    "productType": "icloud",
                    "productLabel": "iCloud",
                    "codeEnabled": True,
                    "codePricePoints": "99",
                }
            ],
            "truncated": True,
        },
        "ChatGPT",
    )
    assert (
        enforce_units(
            "iCloud 邮箱目前价格多少？", "iCloud 接码价格 10元/个，购买价格 13 元。"
        )
        == "当前项目价格单位应为 ReMail 积分，但本轮答复的单位不一致，因此不展示该数值。请稍后重新查询。"
    )
    assert (
        enforce_units("充值 100 元怎么买兑换码？", "支付 100 元。") == "支付 100 元。"
    )

    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    tool = next(
        node
        for node in main_class.body
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_project_prices"
    )
    tool.decorator_list = []
    namespace = {
        "Any": Any,
        "AstrMessageEvent": object,
        "json": json,
        "_integral_tool_int": functions["_integral_tool_int"],
        "_normalize_product_types": normalize,
        "_single_product_type_query": functions["_single_product_type_query"],
        "redact_credentials": redact_credentials,
        "normalize_security_text": normalize_security_text,
        "_project_price_view": project_view,
        "_record_evidence": lambda *_args: None,
    }
    exec(
        compile(ast.Module(body=[tool], type_ignores=[]), "main.py", "exec"), namespace
    )
    plugin = SimpleNamespace(_request=AsyncMock(return_value=payload))
    result = asyncio.run(
        namespace["remail_project_prices"](plugin, object(), "icloud,microsoft,domain")
    )
    assert json.loads(result)["unit"] == "ReMail积分"
    assert plugin._request.await_args.kwargs["params"] == {
        "scope": "visible",
        "offset": 0,
        "limit": 100,
    }
    plugin._request.reset_mock()
    invalid = asyncio.run(
        namespace["remail_project_prices"](plugin, object(), "not-a-product")
    )
    assert json.loads(invalid)["ok"] is False
    plugin._request.assert_not_awaited()
    doc = ast.get_docstring(tool) or ""
    assert "同范围背景" in doc
    assert "codePricePoints" in doc


def test_runtime_system_prompt_documents_every_remail_tool_contract() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    assignment = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    prompt = ast.literal_eval(assignment.value)
    functions, _ = _load_welcome_functions()
    for tool in functions["_ALLOWED_REMAIL_TOOLS"]:
        assert tool in prompt
    assert "参数名称、类型、范围和返回含义以工具定义为准" in prompt
    assert "同参数、同范围的背景结果可以直接复用" in prompt


def test_card_marketplace_alias_is_remail_billing_intent() -> None:
    functions, _ = _load_welcome_functions()
    intent_prompt = PLANNER_SYSTEM_PROMPT
    billing_prompt = functions["_REMAIL_PUBLIC_BILLING_SYSTEM_PROMPT"]
    routing_prompt = functions["_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"]

    for prompt in (intent_prompt, billing_prompt):
        assert "卡网" in prompt
        assert "发卡网" in prompt
    assert "remail_recharge_config" in billing_prompt
    assert "remail_recharge_config" in routing_prompt
    for prompt in (billing_prompt, routing_prompt):
        assert "https://catfk.com/shop/aishop6" not in prompt
        assert "https://wzyp.cn/shop/aishop6" not in prompt
        assert "手续费更低" not in prompt
    assert "当前充值开关" in billing_prompt
    assert "卡密商城" in intent_prompt


def test_api_routing_matches_capability_not_keywords() -> None:
    functions, _ = _load_welcome_functions()
    intent_prompt = PLANNER_SYSTEM_PROMPT
    service_prompt = functions["_REMAIL_PUBLIC_SERVICE_SYSTEM_PROMPT"]
    routing_prompt = functions["_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"]
    assert "按目标理解而非按单个词触发" in intent_prompt
    assert "公开字段使用问题" in intent_prompt
    assert "公开接口、字段、后缀和客户端对接" in routing_prompt
    assert "公开 API 技术支持" in service_prompt
    assert "Gmail 变种邮箱后缀" in intent_prompt
    assert "即使没说 API" in intent_prompt
    assert "remail_api_documentation" in routing_prompt

    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    api_tool = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_api_documentation"
    )
    api_doc = ast.get_docstring(api_tool) or ""
    assert "由用户目标是否落在公开 API 能力范围决定" in api_doc
    assert "Gmail 变种邮箱后缀应该填什么" in api_doc


def test_openapi_search_keeps_referenced_fields_and_public_boundary() -> None:
    excerpt = _load_openapi_excerpt()
    spec = json.loads(
        (PLUGIN_DIR.parents[1] / "web/public/openapi.json").read_text(encoding="utf-8")
    )

    result = excerpt(spec, "emailSuffix")
    encoded = json.dumps(result, ensure_ascii=False)
    assert "emailSuffix" in encoded
    assert result["servers"][0]["url"] == "https://remail.aishop6.com"
    assert result["info"]["version"]
    assert len(encoded) <= 11_000
    assert all(
        operation["path"].startswith("/v1/open/")
        or operation["path"].startswith("/v1/pickup")
        for operation in result["operations"]
    )

    injected = json.loads(json.dumps(spec))
    injected["paths"]["/v1/admin/users"] = {
        "get": {"operationId": "adminUsers", "summary": "emailSuffix admin"}
    }
    assert "/v1/admin/users" not in json.dumps(
        excerpt(injected, "emailSuffix"), ensure_ascii=False
    )

    order = excerpt(
        spec,
        "公开 API 下单 emailSuffix Gmail 变种后缀应该填什么，返回合法值、字段含义和请求示例",
    )
    assert order["operations"][0]["operationId"] == "createOrder"
    flow = excerpt(spec, "完整下单到取件流程")
    operation_ids = {item["operationId"] for item in flow["operations"]}
    required_operations = {
        "createOrder",
        "getOrder",
        "pickupMessages",
        "getPickupMessage",
    }
    assert {"createOrder", "pickupMessages"} <= operation_ids
    # Preserve complete referenced schemas within the excerpt budget; fetch
    # omitted operations by ID instead of requiring the whole flow in 11k.
    missing_operations = required_operations - operation_ids
    if missing_operations:
        assert flow["truncated"] is True
    for operation_id in sorted(required_operations):
        supplement = excerpt(spec, f" {operation_id.upper()} ")
        assert supplement["sourceValid"] is True
        assert supplement["operations"][0]["operationId"] == operation_id
    pickup = excerpt(spec, "验证码如何取件")
    assert pickup["operations"][0]["operationId"] == "pickupMessages"
    assert excerpt(spec, "")["matched"] is False
    broken = excerpt({}, "统一下单")
    assert broken["sourceValid"] is False

    functions, _ = _load_welcome_functions()
    assert not functions["_evidence_is_valid"](
        "api_documentation", broken, {"query": "统一下单"}
    )
    contract = excerpt(spec, "统一下单完整流程")
    tutorial = functions["_render_api_evidence"](contract)
    sanitized_contract = deepcopy(contract)
    properties = sanitized_contract["components"]["schemas"]["Order"]["properties"]
    for field in ("serviceToken", "verificationCode"):
        assert str(properties[field]["example"]) not in tutorial
        properties[field]["example"] = "[敏感信息已隐藏]"
    assert json.loads(tutorial) == sanitized_contract


def test_recharge_config_view_is_dynamic_and_allowlisted() -> None:
    functions, _ = _load_welcome_functions()
    assert functions["_recharge_config_view"](None) == {}
    payload = {
        "enabled": True,
        "paymentMethods": ["alipay"],
        "minPoints": "100",
        "feeRate": "0.01",
        "feeCapPoints": "5",
        "tiers": [
            {
                "points": "100",
                "bonusPoints": "1",
                "feePoints": "1",
                "creditedPoints": "100",
                "merchantKey": "secret",
            }
        ],
        "redemptionCodePurchaseUrl": "https://current.example/cards",
        "merchantKey": "secret",
    }
    view = functions["_recharge_config_view"](payload)
    encoded = json.dumps(view, ensure_ascii=False)
    assert view["redemptionCodePurchaseUrl"] == "https://current.example/cards"
    assert view["paymentMethods"] == ["alipay"]
    assert "merchantKey" not in encoded
    assert "secret" not in encoded

    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    tool = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_recharge_config"
    )
    tool.decorator_list = []
    namespace = {
        "AstrMessageEvent": object,
        "json": json,
        "_recharge_config_view": functions["_recharge_config_view"],
        "_record_evidence": functions["_record_evidence"],
    }
    exec(
        compile(ast.Module(body=[tool], type_ignores=[]), "main.py", "exec"), namespace
    )
    extras = {}
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    plugin = SimpleNamespace(_request=AsyncMock(return_value=payload))
    result = asyncio.run(namespace["remail_recharge_config"](plugin, event))
    assert json.loads(result)["redemptionCodePurchaseUrl"] == (
        "https://current.example/cards"
    )
    plugin._request.assert_awaited_once_with(
        "GET", "/v1/bot/recharges/config", event=event
    )
    assert "recharge_config" in extras["_remail_evidence_v1"]


def test_announcement_tool_view_is_flat_bounded_and_redacted() -> None:
    functions, _ = _load_welcome_functions()
    assert functions["_announcement_view"](None, None) == {}
    assert functions["_faq_view"](None) == {}
    view = functions["_announcement_view"](
        {"notice": "维护通知 password=hunter2"},
        {
            "announcements": [
                {
                    "id": index,
                    "title": f"公告 {index}",
                    "content": "公开内容" * 1000,
                    "enabled": True,
                }
                for index in range(20)
            ]
        },
    )
    encoded = json.dumps(view, ensure_ascii=False)
    assert isinstance(view["notice"], str)
    assert isinstance(view["announcements"], list)
    assert view["truncated"] is True
    assert len(encoded) <= 12_000
    assert "hunter2" not in encoded


def test_required_dynamic_evidence_is_recorded_per_event() -> None:
    functions, _ = _load_welcome_functions()
    question = "iCloud 当前价格，项目 ID 2 的精确库存，API 怎么查询？"
    plan = _fact_plan(
        intents=("price", "inventory", "api"),
        facts=(
            _fact("price", "project_prices", params={"productTypes": ["icloud"]}),
            _fact("project", "projects", params={"productTypes": ["icloud"]}),
            _fact(
                "stock",
                "project_inventory",
                params={"projectId": 2, "productTypes": ["icloud"]},
                depends_on=("project",),
            ),
            _fact("api", "api_documentation", params={"query": question}),
        ),
        answer_mode="public_api",
        privacy="private",
        entities={"productTypes": ["icloud"], "projectId": 2},
    )
    extras = {
        "_remail_api_consultation": True,
        "_remail_intent_plan_v1": plan,
    }
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    required = functions["_required_evidence"](event, question)
    assert required == {
        "api_documentation",
        "project_prices",
        "projects",
        "project_inventory",
    }
    assert "当前没有取得完整" in functions["_missing_evidence_response"](
        event, question
    )
    observed_at = datetime.now(timezone.utc).isoformat()
    functions["_record_evidence"](
        event,
        "project_prices",
        {
            "sourceValid": True,
            "matched": True,
            "prices": [
                {
                    "projectId": 2,
                    "productType": "icloud",
                    "codeEnabled": True,
                    "codePricePoints": "20",
                }
            ],
        },
        {"productTypes": ["icloud"]},
    )
    functions["_record_evidence"](
        event,
        "projects",
        {"items": [{"id": 2}], "total": 1},
        {"search": "", "offset": 0},
    )
    functions["_record_evidence"](
        event,
        "project_inventory",
        {"projectId": 2, "observedAt": observed_at, "products": []},
        {"projectId": 2},
    )
    functions["_record_evidence"](
        event,
        "api_documentation",
        {
            "sourceValid": True,
            "matched": True,
            "operations": [{"method": "GET", "path": "/v1/open/projects"}],
        },
        {"query": question},
    )
    assert functions["_missing_evidence_response"](event, question) == ""


def test_event_authorization_is_reused_within_one_turn() -> None:
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main_class = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    method = next(
        node
        for node in main_class.body
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "_authorize_event"
    )
    namespace = {
        "AstrMessageEvent": object,
        "_REMAIL_AUTHORIZED_MARKER": "_remail_authorized",
    }
    exec(
        compile(ast.Module(body=[method], type_ignores=[]), "main.py", "exec"),
        namespace,
    )
    extras = {}
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    plugin = SimpleNamespace(
        _request=AsyncMock(
            return_value={"authorized": True, "bound": True, "accountAvailable": True}
        )
    )

    asyncio.run(namespace["_authorize_event"](plugin, event))
    asyncio.run(namespace["_authorize_event"](plugin, event))

    plugin._request.assert_awaited_once_with("GET", "/v1/bot/context", event=event)
    assert extras["_remail_authorized"] is True


def test_api_intent_planner_injects_plan_without_prefetch() -> None:
    functions, _ = _load_welcome_functions()
    generate_plan = functions["_generate_fact_plan"]
    authorize = functions["authorize_llm"]
    summarize = functions["_public_api_capability_summary"]

    capability_context = summarize(
        {
            "paths": {
                "/v1/open/orders": {
                    "post": {
                        "operationId": "createOrder",
                        "summary": "统一下单",
                        "tags": ["Core"],
                    }
                },
                "/v1/admin/users": {
                    "get": {
                        "operationId": "adminUsers",
                        "summary": "管理员用户列表",
                    }
                },
                "/v1/bot/profile": {
                    "get": {
                        "operationId": "botProfile",
                        "summary": "机器人资料",
                    }
                },
            }
        }
    )
    assert "createOrder" in capability_context
    assert "/v1/open/orders" in capability_context
    assert "adminUsers" not in capability_context
    assert "botProfile" not in capability_context
    assert len(capability_context) <= 12000
    assert json.loads(capability_context)["operations"][0]["method"] == "POST"

    question = "下单时，Gmail 变种邮箱后缀应该填什么"
    api_plan = _fact_plan(
        intents=("api",),
        facts=(_fact("api", "api_documentation", params={"query": question}),),
        answer_mode="public_api",
        privacy="private",
    )
    classifier_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(api_plan.to_dict(), ensure_ascii=False),
            )
        ),
    )
    classifier_event = SimpleNamespace(
        message_str=question,
        unified_msg_origin="qq:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda _key, default=None: default,
    )
    assert (
        asyncio.run(
            generate_plan(
                classifier_context,
                classifier_event,
                question,
                "",
                capability_context,
            )
        )
        == api_plan
    )
    classifier_context.llm_generate.assert_awaited_once()
    classifier_payload = json.loads(
        classifier_context.llm_generate.await_args.kwargs["prompt"]
    )
    assert classifier_payload["publicApiCapabilities"] == capability_context

    extras = {}
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        get_message_type=lambda: "friend",
        message_str=question,
        unified_msg_origin="qq:FriendMessage:123456789",
        set_extra=lambda key, value: extras.__setitem__(key, value),
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized API request must continue"),
    )
    context = SimpleNamespace(
        get_config=lambda: {
            "provider_settings": {
                "show_tool_use_status": False,
                "show_tool_call_result": False,
            }
        },
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(api_plan.to_dict(), ensure_ascii=False),
            )
        ),
    )
    plugin = SimpleNamespace(
        _authorize_event=AsyncMock(),
        _public_api_capability_context=AsyncMock(return_value=capability_context),
        context=context,
        remail_api_documentation=AsyncMock(),
    )
    request = SimpleNamespace(
        system_prompt="你是“红夜”，ReMail 官方 FAE。",
        extra_user_content_parts=[],
        func_tool=_ToolSetStub(),
    )
    _support_session(event, context)
    asyncio.run(authorize(plugin, event, request))

    plugin._authorize_event.assert_awaited_once_with(event)
    plugin._public_api_capability_context.assert_awaited_once_with(event)
    assert context.llm_generate.await_count == 2
    assert [
        json.loads(call.kwargs["prompt"])["workflowPhase"]
        for call in context.llm_generate.await_args_list
    ] == ["intent", "planner"]
    authorize_payload = json.loads(context.llm_generate.await_args.kwargs["prompt"])
    assert authorize_payload["publicApiCapabilities"] == capability_context
    plugin.remail_api_documentation.assert_not_awaited()
    assert len(request.extra_user_content_parts) == 3
    background = json.loads(
        request.extra_user_content_parts[0].text.split("\n", 1)[1]
    )
    assert background["publicApiCapabilities"] == capability_context
    assert "ownOrders" not in background
    assert "本轮答复渠道：QQ" in request.extra_user_content_parts[1].text
    plan_context = request.extra_user_content_parts[-1].text
    assert "独立规划模型" in plan_context
    plan_payload = json.loads(plan_context.split("\n", 1)[1])
    assert plan_payload["kind"] == "validated_remail_fact_plan"
    assert plan_payload["plan"] == api_plan.to_dict()


def test_authorize_injects_plan_and_leaves_tool_execution_to_main_agent() -> None:
    functions, _ = _load_welcome_functions()
    extras = {}
    plan = _fact_plan(
        intents=("recharge", "price"),
        facts=(
            _fact("recharge", "recharge_config"),
            _fact("price", "project_prices", params={"productTypes": ["icloud"]}),
        ),
        privacy="private",
        entities={"productTypes": ["icloud"]},
    )
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
        get_message_type=lambda: "friend",
        message_str="卡网地址和 iCloud 当前价格",
        unified_msg_origin="qq:FriendMessage:123456789",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized request must continue"),
    )
    context = SimpleNamespace(
        get_config=lambda: {
            "provider_settings": {
                "show_tool_use_status": False,
                "show_tool_call_result": False,
            }
        },
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(plan.to_dict(), ensure_ascii=False),
            )
        ),
    )
    plugin = SimpleNamespace(
        _authorize_event=AsyncMock(),
        _public_api_capability_context=AsyncMock(return_value=""),
        context=context,
        remail_recharge_config=AsyncMock(
            return_value='{"enabled":true,"redemptionCodePurchaseUrl":"https://current.example/cards"}'
        ),
        remail_project_prices=AsyncMock(
            return_value='{"unit":"ReMail积分","prices":[]}'
        ),
    )

    tools = _ToolSetStub(
        [
            "remail_recharge_config",
            "remail_project_prices",
            "fake_delete_files",
            "remail_fake_destructive_tool",
        ],
        owner=plugin,
    )
    tools.tools.append(
        SimpleNamespace(
            name="remail_projects",
            handler_module_path="evil_plugin.main",
            _wrapped=SimpleNamespace(handler=partial(lambda *_args: None, plugin)),
        )
    )
    tools.tools.append(
        SimpleNamespace(
            name="remail_faqs",
            handler_module_path="evil.astrbot_plugin_remail.main",
            _wrapped=SimpleNamespace(handler=partial(lambda *_args: None, object())),
        )
    )
    request = SimpleNamespace(
        system_prompt="<remail_fae_system_v1>",
        extra_user_content_parts=[],
        func_tool=tools,
    )

    _support_session(event, context)
    asyncio.run(functions["authorize_llm"](plugin, event, request))

    plugin._authorize_event.assert_awaited_once_with(event)
    assert context.llm_generate.await_count == 2
    assert [
        json.loads(call.kwargs["prompt"])["workflowPhase"]
        for call in context.llm_generate.await_args_list
    ] == ["intent", "planner"]
    plugin.remail_recharge_config.assert_not_awaited()
    plugin.remail_project_prices.assert_not_awaited()
    payload = request.extra_user_content_parts[-1].text
    assert "validated_remail_fact_plan" in payload
    assert "recharge_config" in payload and "project_prices" in payload
    assert tools.names() == ["remail_recharge_config", "remail_project_prices"]
    assert (
        functions["_restrict_remail_tools"](SimpleNamespace(func_tool=None), plugin)
        is False
    )
    assert (
        functions["_restrict_remail_tools"](SimpleNamespace(func_tool=object()), plugin)
        is False
    )


def test_private_planner_removes_attachments_before_main_agent_build() -> None:
    functions, _ = _load_welcome_functions()
    plan = _fact_plan(
        intents=("diagnosis",),
        facts=(
            _fact(
                "diagnosis",
                "code_diagnosis",
                params={"hasOrderEmail": True},
            ),
        ),
        answer_mode="diagnosis",
        privacy="private",
    )
    raw = (
        "order@example.com 一直没收到，邮件标题是 Other Project Welcome，"
        "发件人是 OtherCorp"
    )
    extras = {}
    message_obj = SimpleNamespace(message=[object(), object()], message_str=raw)
    event = SimpleNamespace(
        message_str=raw,
        message_obj=message_obj,
        unified_msg_origin="qq:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            return_value=SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(plan.to_dict(), ensure_ascii=False),
            )
        ),
    )
    plugin = SimpleNamespace(
        context=context,
        _public_api_capability_context=AsyncMock(return_value=""),
        _authorize_event=AsyncMock(),
        _reply=AsyncMock(),
    )

    _support_session(event, context)
    asyncio.run(functions["prepare_remail_llm_response"](plugin, event))

    assert extras["_remail_owned"] is True
    assert extras["_remail_intent_plan_v1"] == plan
    assert extras["_remail_order_email"] == "order@example.com"
    assert extras["_remail_input_prepared"] is True
    assert extras["enable_streaming"] is False
    assert message_obj.message == []
    assert message_obj.message_str == event.message_str
    for private_value in (
        "order@example.com",
        "Other Project Welcome",
        "OtherCorp",
    ):
        assert private_value not in event.message_str
        assert private_value not in context.llm_generate.await_args.kwargs["prompt"]
    planner_call = context.llm_generate.await_args.kwargs
    assert planner_call["tools"] is None and planner_call["contexts"] is None
    assert context.llm_generate.await_count == 2
    assert [
        json.loads(call.kwargs["prompt"])["workflowPhase"]
        for call in context.llm_generate.await_args_list
    ] == ["intent", "planner"]

    ignored = _fact_plan(intents=(), route="ignore", privacy="private")
    context.llm_generate.return_value = SimpleNamespace(
        role="assistant",
        completion_text=json.dumps(ignored.to_dict(), ensure_ascii=False),
    )
    ignored_extras = {}
    ignored_obj = SimpleNamespace(message=[object()], message_str="帮我看看")
    ignored_event = SimpleNamespace(
        message_str="帮我看看",
        message_obj=ignored_obj,
        unified_msg_origin="qq:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: ignored_extras.get(key, default),
        set_extra=lambda key, value: ignored_extras.__setitem__(key, value),
    )
    _support_session(ignored_event, context)
    context.llm_generate.reset_mock()
    existing_conversations = len(context.conversation_manager.conversations)
    asyncio.run(functions["prepare_remail_llm_response"](plugin, ignored_event))
    assert ignored_obj.message == []
    assert ignored_extras["_remail_input_prepared"] is True
    assert ignored_extras.get("_remail_owned") is not True
    context.llm_generate.assert_awaited_once()
    assert ignored_extras.get("provider_request") is None
    assert len(context.conversation_manager.conversations) == existing_conversations
    plugin._reply.assert_awaited_with(ignored_event, functions["_REMAIL_ONLY_TEXT"])

    attachment_extras = {}
    attachment_obj = SimpleNamespace(message=[object()], message_str="")
    attachment_event = SimpleNamespace(
        message_str="",
        message_obj=attachment_obj,
        unified_msg_origin="qq:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: attachment_extras.get(key, default),
        set_extra=lambda key, value: attachment_extras.__setitem__(key, value),
    )
    _support_session(attachment_event, context)
    asyncio.run(functions["prepare_remail_llm_response"](plugin, attachment_event))
    assert attachment_obj.message == []
    assert attachment_event.message_str == "ReMail 请求"
    assert attachment_extras["_remail_input_prepared"] is True
    plugin._reply.assert_awaited_with(
        attachment_event, functions["_REMAIL_INTENT_UNAVAILABLE_TEXT"]
    )

    cancelled_extras = {}
    cancelled_obj = SimpleNamespace(message=[object()], message_str="帮我看附件")
    cancelled_event = SimpleNamespace(
        message_str="帮我看附件",
        message_obj=cancelled_obj,
        unified_msg_origin="qq:FriendMessage:123456789",
        get_message_type=lambda: "friend",
        get_extra=lambda key, default=None: cancelled_extras.get(key, default),
        set_extra=lambda key, value: cancelled_extras.__setitem__(key, value),
    )
    cancelled_context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=asyncio.CancelledError()),
    )
    cancelled_plugin = SimpleNamespace(
        context=cancelled_context,
        _public_api_capability_context=AsyncMock(return_value=""),
        _authorize_event=AsyncMock(),
        _reply=AsyncMock(),
    )
    _support_session(cancelled_event, cancelled_context)
    asyncio.run(
        functions["prepare_remail_llm_response"](cancelled_plugin, cancelled_event)
    )
    assert cancelled_obj.message == []
    assert cancelled_extras["_remail_input_prepared"] is True
    assert cancelled_extras["enable_streaming"] is False
    cancelled_plugin._reply.assert_awaited_with(
        cancelled_event, functions["_REMAIL_INTENT_UNAVAILABLE_TEXT"]
    )


def test_configured_qq_management_and_telegram_bot_mentions() -> None:
    functions, _ = _load_welcome_functions()
    configured = functions["_configured_qq_management"]
    assert configured(
        {
            "qq_group_owner_id": "9845248",
            "qq_group_admin_ids": ["12345678", 87654321, "9845248", "bad", "0"],
        }
    ) == ("9845248", {"12345678", "87654321"})
    assert configured({"qq_group_owner_id": "not-a-qq"}) == ("", set())
    group_config = {
        "qq_group_owner_id": "9845248",
        "qq_group_admin_ids": ["12345678"],
        "qq_group_management": [
            "529642597|11111111|22222222,33333333",
            "650384960|44444444|55555555",
        ],
    }
    assert configured(group_config, "529642597") == (
        "11111111",
        {"22222222", "33333333"},
    )
    assert configured(group_config, "999999999") == (
        "9845248",
        {"12345678"},
    )

    mention = functions["At"](qq="@HongYeBot", name="HongYeBot")
    telegram_event = SimpleNamespace(
        get_platform_name=lambda: "telegram",
        get_self_id=lambda: "hongyebot",
        get_messages=lambda: [mention],
        message_obj=SimpleNamespace(raw_message=None),
    )
    assert functions["_mentions_bot"](telegram_event)
    assert functions["_normalize_product_types"]("Gmail 变种") == ("gmail_variant",)
    assert functions["_normalize_product_types"]("iCloud多少钱") == ("icloud",)


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
    assert json.loads(str(error(503, "ReMail WebSocket response lost"))) == {
        "ok": False,
        "kind": "unavailable",
        "retryable": True,
        "retryAfter": None,
    }
    assert safe(error(429, "rate limit exceeded")) == "请求过于频繁，请稍后再试。"
    assert safe(error(429, "rate limit exceeded", retry_after="3")) == (
        "请求过于频繁，请 3 秒后再试。"
    )
    assert (
        safe(error(503, "ReMail WebSocket response lost"))
        == "服务暂时不可用，请稍后重试。"
    )
    assert safe(error(422, "Account or password is incorrect."), binding=True) == (
        "ReMail 账号或密码错误。"
    )
    assert safe(error(422, "账号或密码不正确。"), binding=True) == (
        "ReMail 账号或密码错误。"
    )
    assert "mysql://" not in safe(
        error(422, "数据库 mysql://root:secret@db/internal"), binding=True
    )


def test_llm_request_requires_remail_event_authorization() -> None:
    runtime, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    handler = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "authorize_llm"
    )
    handler.decorator_list = []
    core_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_CORE_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    billing_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_PUBLIC_BILLING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    service_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_PUBLIC_SERVICE_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    react_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_REACT_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    routing_prompt = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id == "_REMAIL_TOOL_ROUTING_SYSTEM_PROMPT"
            for target in node.targets
        )
    )
    command_pattern = next(
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name) and target.id == "_REMAIL_COMMAND_PREFIX"
            for target in node.targets
        )
    )
    is_command = next(
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef) and node.name == "_is_remail_command"
    )
    helpers = _load_user_error_helpers()

    class TextPart:
        def __init__(self, text: str) -> None:
            self.text = text

    namespace = {
        "AstrMessageEvent": object,
        "Any": Any,
        "asyncio": asyncio,
        "ProviderRequest": object,
        "MessageChain": lambda items: items,
        "MessageType": SimpleNamespace(FRIEND_MESSAGE="friend", GROUP_MESSAGE="group"),
        "Plain": lambda text: text,
        "ReMailError": helpers["ReMailError"],
        "TextPart": TextPart,
        "_safe_user_error": helpers["_safe_user_error"],
        "_event_is_private": runtime["_event_is_private"],
        "_event_is_owned": runtime["_event_is_owned"],
        "_mark_event_owned": runtime["_mark_event_owned"],
        "_install_owned_send_guard": runtime["_install_owned_send_guard"],
        "_request_is_remail": runtime["_request_is_remail"],
        "_capture_group_request": runtime["_capture_group_request"],
        "_event_scope": runtime["_event_scope"],
        "_prepare_fae_workflow": runtime["_prepare_fae_workflow"],
        "session_request_matches": session_request_matches,
        "trace_note": runtime["trace_note"],
        "trace_finish": trace_finish,
        "snapshot_response": snapshot_response,
        "uuid": uuid,
        "monotonic": monotonic,
        "_restrict_remail_tools": runtime["_restrict_remail_tools"],
        "_scope_question": runtime["_scope_question"],
        "_safe_llm_context_text": runtime["_safe_llm_context_text"],
        "_prepare_fae_context": runtime["_prepare_fae_context"],
        "_model_background": runtime["_model_background"],
        "INTERNAL_MODULE_KNOWLEDGE": INTERNAL_MODULE_KNOWLEDGE,
        "PUBLIC_DISCLOSURE_RULES": PUBLIC_DISCLOSURE_RULES,
        "_configured_personality": runtime["_configured_personality"],
        "_recent_intent_context": runtime["_recent_intent_context"],
        "PUBLIC_BUSINESS_RULES": PUBLIC_BUSINESS_RULES,
        "SOURCE_RELIABILITY_RULES": SOURCE_RELIABILITY_RULES,
        "API_SUPPORT_GUIDANCE": API_SUPPORT_GUIDANCE,
        "_generate_fact_plan": runtime["_generate_fact_plan"],
        "FactPlan": FactPlan,
        "_enforce_black_box": runtime["_enforce_black_box"],
        "_enforce_group_privacy": runtime["_enforce_group_privacy"],
        "_BLACK_BOX_RESPONSE": runtime["_BLACK_BOX_RESPONSE"],
        "_GROUP_PRIVATE_MAIL_RESPONSE": runtime["_GROUP_PRIVATE_MAIL_RESPONSE"],
        "_REMAIL_ONLY_TEXT": runtime["_REMAIL_ONLY_TEXT"],
        "_REMAIL_INTENT_UNAVAILABLE_TEXT": runtime["_REMAIL_INTENT_UNAVAILABLE_TEXT"],
        "_REMAIL_TOOLSET_UNAVAILABLE_TEXT": runtime["_REMAIL_TOOLSET_UNAVAILABLE_TEXT"],
        "_REMAIL_SAFE_ERROR_TEXT": runtime["_REMAIL_SAFE_ERROR_TEXT"],
        "_REMAIL_CREDENTIAL_INPUT_TEXT": runtime["_REMAIL_CREDENTIAL_INPUT_TEXT"],
        "_REMAIL_INTENT_PLAN_KEY": runtime["_REMAIL_INTENT_PLAN_KEY"],
        "_REMAIL_CREDENTIAL_INPUT_KEY": runtime["_REMAIL_CREDENTIAL_INPUT_KEY"],
        "_REMAIL_CANONICAL_RESPONSE_KEY": runtime["_REMAIL_CANONICAL_RESPONSE_KEY"],
        "_REMAIL_MAIN_AGENT_READY_KEY": runtime["_REMAIL_MAIN_AGENT_READY_KEY"],
        "_BIND_ARGUMENTS": runtime["_BIND_ARGUMENTS"],
        "contains_credentials": contains_credentials,
        "json": json,
        "re": re,
    }

    exec(
        compile(
            ast.Module(
                body=[
                    core_prompt,
                    billing_prompt,
                    service_prompt,
                    react_prompt,
                    routing_prompt,
                    command_pattern,
                    is_command,
                    handler,
                ],
                type_ignores=[],
            ),
            "main.py",
            "exec",
        ),
        namespace,
    )

    class Plugin:
        async def _authorize_event(self, _event):
            raise helpers["ReMailError"](401, "Authentication is required.")

        async def _reply(self, event, text):
            event.set_extra(runtime["_REMAIL_CANONICAL_RESPONSE_KEY"], text)
            await event.send([text])
            event.stop_event()

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    async def reply(target_event, text):
        target_event.set_extra(runtime["_REMAIL_CANONICAL_RESPONSE_KEY"], text)
        await target_event.send([text])
        target_event.stop_event()

    event_extras = {}
    event = SimpleNamespace(
        get_extra=lambda key, default="": event_extras.get(key, default),
        set_extra=lambda key, value: event_extras.__setitem__(key, value),
        get_message_type=lambda: "friend",
        message_str="ReMail 怎么用？",
        send=send,
        stop_event=lambda: stopped.append(True),
    )
    request = SimpleNamespace(
        system_prompt="你是“红夜”，ReMail 官方 FAE。", extra_user_content_parts=[]
    )
    denied_plugin = Plugin()
    denied_plugin.context = SimpleNamespace(
        llm_generate=AsyncMock(), conversation_manager=_ConversationManagerStub()
    )
    _support_result_api(event)
    asyncio.run(namespace["authorize_llm"](denied_plugin, event, request))
    denied_plugin.context.llm_generate.assert_not_awaited()
    assert denied_plugin.context.conversation_manager.conversations == {}
    assert sent == [[("plain", "当前会话未获授权。")]]
    assert stopped == [True]
    assert not request.extra_user_content_parts
    assert "<remail_public_billing_rules>" not in request.system_prompt

    handoff_extras = {
        "_remail_admin_handoff_role": "群主",
        "_remail_group_trigger_verified": True,
        "_remail_intent_plan_v1": _fact_plan(intents=("social",)),
    }
    authorized = SimpleNamespace(
        _authorize_event=AsyncMock(),
        config={"qq_group_owner_id": "888888888"},
        context=SimpleNamespace(),
    )
    handoff_event = SimpleNamespace(
        get_extra=lambda key, default="": handoff_extras.get(key, default),
        set_extra=lambda key, value: handoff_extras.__setitem__(key, value),
        get_message_type=lambda: "group",
        get_platform_name=lambda: "aiocqhttp",
        get_platform_id=lambda: "bot",
        get_sender_id=lambda: "123456789",
        get_self_id=lambda: "999999999",
        get_group_id=lambda: "529642597",
        unified_msg_origin="bot:GroupMessage:123456789_529642597",
        message_obj=SimpleNamespace(
            message_id="101",
            raw_message={"message": [{"type": "at", "data": {"qq": "888888888"}}]},
        ),
        message_str="联系群主",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized handoff must continue"),
    )
    handoff_request = SimpleNamespace(
        system_prompt="",
        contexts=[],
        image_urls=[],
        audio_urls=[],
        extra_user_content_parts=[],
        func_tool=_ToolSetStub(),
    )
    _support_session(handoff_event, authorized.context, handoff_request)
    asyncio.run(namespace["authorize_llm"](authorized, handoff_event, handoff_request))
    authorized._authorize_event.assert_not_awaited()
    assert len(handoff_request.extra_user_content_parts) == 2
    context = handoff_request.extra_user_content_parts[-1].text
    assert "红夜应主动代接" in context
    assert "非必要不要打扰群主" in context
    assert "QQ" not in context
    assert "接码订单和购买邮箱订单都使用 ReMail 消费积分余额支付" in (
        handoff_request.system_prompt
    )
    assert "无需充值" in handoff_request.system_prompt
    assert "remail_recharge_config" in handoff_request.system_prompt
    assert "https://catfk.com/shop/aishop6" not in handoff_request.system_prompt
    assert "具体接码窗口" in handoff_request.system_prompt
    assert "质保时长以本轮项目字段为准" in handoff_request.system_prompt
    assert "不得输出 TG群" in handoff_request.system_prompt
    assert "遵循 AstrBot 当前的 provider_settings.max_agent_step 配置" in (
        handoff_request.system_prompt
    )
    assert "不得用注册风控、需求大小、资源稀缺" in handoff_request.system_prompt
    assert "当前积分价格：remail_project_prices" in handoff_request.system_prompt
    assert "库存明细：取得项目编号后调用 remail_project_inventory" in (
        handoff_request.system_prompt
    )

    ordinary_extras = {
        "_remail_intent_plan_v1": _fact_plan(intents=("social",), privacy="private"),
        "_remail_same_sender_context": (
            "邮件标题是 Previous welcome，发件人是 PreviousCorp，"
            "正文是 previous private body"
        ),
    }
    ordinary = SimpleNamespace(_authorize_event=AsyncMock(), context=SimpleNamespace())
    ordinary_event = SimpleNamespace(
        get_extra=lambda key, default="": ordinary_extras.get(key, default),
        set_extra=lambda key, value: ordinary_extras.__setitem__(key, value),
        get_message_type=lambda: "friend",
        message_str="这个收件地址怎么一点动静都没有",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("authorized request must continue"),
    )
    ordinary_request = SimpleNamespace(
        system_prompt="你是“红夜”，ReMail 官方 FAE。",
        contexts=[{"role": "user", "content": "不应进入主 Agent 的旧上下文"}],
        image_urls=["other-project-mail.png"],
        audio_urls=["mail.wav"],
        extra_user_content_parts=[TextPart("不应进入主 Agent 的旧知识")],
        func_tool=_ToolSetStub(),
    )
    _support_session(ordinary_event, ordinary.context, ordinary_request)
    asyncio.run(namespace["authorize_llm"](ordinary, ordinary_event, ordinary_request))
    ordinary._authorize_event.assert_awaited_once_with(ordinary_event)
    assert ordinary_request.contexts == []
    assert ordinary_request.image_urls == []
    assert ordinary_request.audio_urls == []
    assert len(ordinary_request.extra_user_content_parts) == 2
    ordinary_parts = "\n".join(
        part.text for part in ordinary_request.extra_user_content_parts
    )
    for private_value in (
        "Previous welcome",
        "PreviousCorp",
        "previous private body",
        "不应进入主 Agent 的旧上下文",
        "other-project-mail.png",
        "mail.wav",
        "不应进入主 Agent 的旧知识",
    ):
        assert private_value not in ordinary_parts
    assert "邮件详情已隐藏" in ordinary_parts
    assert (
        "validated_remail_fact_plan"
        in ordinary_request.extra_user_content_parts[-1].text
    )
    assert ordinary_request.system_prompt.count("<remail_public_billing_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_public_service_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_react_rules>") == 1
    assert ordinary_request.system_prompt.count("<remail_tool_routing_rules>") == 1

    diagnosis_extras = {
        "_remail_intent_plan_v1": _fact_plan(
            intents=("diagnosis",),
            facts=(
                _fact(
                    "diagnosis",
                    "code_diagnosis",
                    params={"hasOrderEmail": True},
                ),
            ),
            answer_mode="diagnosis",
            privacy="private",
        )
    }
    diagnosis_plugin = SimpleNamespace(
        _authorize_event=AsyncMock(), context=SimpleNamespace()
    )
    diagnosis_event = SimpleNamespace(
        get_extra=lambda key, default="": diagnosis_extras.get(key, default),
        set_extra=lambda key, value: diagnosis_extras.__setitem__(key, value),
        get_message_type=lambda: "friend",
        message_str="order@example.com 没收到邮件",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("diagnosis request must continue"),
    )
    diagnosis_request = SimpleNamespace(
        system_prompt="你是“红夜”，ReMail 官方 FAE。",
        contexts=[{"role": "user", "content": "旧会话"}],
        image_urls=["mail-screen.png"],
        audio_urls=["mail.wav"],
        extra_user_content_parts=[TextPart("旧知识")],
        func_tool=_ToolSetStub(),
    )
    _support_session(diagnosis_event, diagnosis_plugin.context, diagnosis_request)
    asyncio.run(
        namespace["authorize_llm"](diagnosis_plugin, diagnosis_event, diagnosis_request)
    )
    assert diagnosis_request.contexts == []
    assert diagnosis_request.image_urls == []
    assert diagnosis_request.audio_urls == []
    assert len(diagnosis_request.extra_user_content_parts) == 1
    assert (
        "validated_remail_fact_plan"
        in diagnosis_request.extra_user_content_parts[0].text
    )

    credential_extras = {}
    credential_plugin = SimpleNamespace(_authorize_event=AsyncMock(), _reply=reply)
    credential_event = SimpleNamespace(
        get_extra=lambda key, default="": credential_extras.get(key, default),
        set_extra=lambda key, value: credential_extras.__setitem__(key, value),
        get_message_type=lambda: "friend",
        message_str='{"password":"hunter2"}',
        send=AsyncMock(),
        stop_event=lambda: None,
    )
    credential_request = SimpleNamespace(
        system_prompt="你是“红夜”，ReMail 官方 FAE。", extra_user_content_parts=[]
    )
    _support_result_api(credential_event)
    asyncio.run(
        namespace["authorize_llm"](
            credential_plugin, credential_event, credential_request
        )
    )
    credential_plugin._authorize_event.assert_not_awaited()
    credential_event._remail_original_send.assert_awaited_once()

    unrelated = SimpleNamespace(_authorize_event=AsyncMock())
    unrelated_event = SimpleNamespace(
        get_extra=lambda _key, default="": default,
        get_message_type=lambda: "friend",
        message_str="今天天气如何？",
    )
    unrelated_request = SimpleNamespace(
        system_prompt="你是天气助手。", extra_user_content_parts=[]
    )
    asyncio.run(
        namespace["authorize_llm"](unrelated, unrelated_event, unrelated_request)
    )
    unrelated._authorize_event.assert_not_awaited()
    assert unrelated_request.system_prompt == "你是天气助手。"

    group_stopped = []
    group = SimpleNamespace(_authorize_event=AsyncMock())
    group_event = SimpleNamespace(
        get_extra=lambda _key, default="": default,
        get_message_type=lambda: "group",
        message_str="没有艾特红夜的普通群消息",
        send=AsyncMock(),
        stop_event=lambda: group_stopped.append(True),
    )
    group_request = SimpleNamespace(extra_user_content_parts=[])
    asyncio.run(namespace["authorize_llm"](group, group_event, group_request))
    group._authorize_event.assert_not_awaited()
    assert group_stopped == []
    assert not hasattr(group_request, "system_prompt")

    verified = SimpleNamespace(
        _authorize_event=AsyncMock(),
        context=SimpleNamespace(
            get_config=lambda _umo=None: {
                "provider_settings": {
                    "show_tool_use_status": False,
                    "show_tool_call_result": False,
                }
            }
        ),
    )
    verified_extras = {
        "action_type": "live",
        "_remail_group_trigger_verified": True,
        "_remail_same_sender_context": "上一条同一用户问题",
        "_remail_api_consultation": False,
        "_remail_intent_plan_v1": _fact_plan(intents=("social",)),
    }
    verified_event = SimpleNamespace(
        unified_msg_origin="bot:GroupMessage:529642597",
        get_extra=lambda key, default="": verified_extras.get(key, default),
        set_extra=lambda key, value: verified_extras.__setitem__(key, value),
        get_message_type=lambda: "group",
        get_platform_name=lambda: "aiocqhttp",
        get_platform_id=lambda: "bot",
        get_sender_id=lambda: "123456789",
        get_self_id=lambda: "999999999",
        get_group_id=lambda: "529642597",
        message_obj=SimpleNamespace(
            message_id="102",
            raw_message={"message": [{"type": "at", "data": {"qq": "999999999"}}]},
        ),
        message_str="已经通过艾特和意图识别的 ReMail 问题",
        send=AsyncMock(),
        stop_event=lambda: pytest.fail("verified group request must continue"),
    )
    verified_request = SimpleNamespace(
        system_prompt="",
        contexts=[{"role": "user", "content": "其他群员历史"}],
        image_urls=["other-user.png"],
        audio_urls=["other-user.wav"],
        extra_user_content_parts=[
            TextPart("<system_reminder>群聊历史</system_reminder>"),
            TextPart("<Quoted Message>其他成员内容</Quoted Message>"),
            TextPart("[Related Knowledge Base Results]:\n公开知识"),
        ],
        func_tool=_ToolSetStub(),
    )
    _support_session(verified_event, verified.context, verified_request)
    asyncio.run(namespace["authorize_llm"](verified, verified_event, verified_request))
    assert verified_extras["action_type"] == ""
    verified._authorize_event.assert_awaited_once_with(verified_event)
    assert "remail_project_prices" in verified_request.system_prompt
    assert verified_request.contexts == []
    assert verified_request.image_urls == []
    assert verified_request.audio_urls == []
    extra_texts = [part.text for part in verified_request.extra_user_content_parts]
    assert not any("其他群员历史" in text for text in extra_texts)
    assert not any("其他成员内容" in text for text in extra_texts)
    assert not any("公开知识" in text for text in extra_texts)
    assert any("上一条同一用户问题" in text for text in extra_texts)

    host_profile = {
        "provider_settings": {
            "show_tool_use_status": True,
            "show_tool_call_result": True,
            "display_reasoning_text": True,
            "tool_schema_mode": "skills_like",
            "file_extract": {"enable": True},
            "default_image_caption_provider_id": "caption-provider",
        },
        "platform_settings": {
            "reply_prefix": "configured prefix",
            "reply_with_mention": True,
            "reply_with_quote": True,
            "segmented_reply": {"content_cleanup_rule": "不"},
        },
        "provider_stt_settings": {"enable": True},
        "provider_tts_settings": {"enable": True},
        "content_safety": {"baidu_aip": {"enable": True}},
        "t2i": True,
    }
    before = deepcopy(host_profile)
    verified.context.get_config = lambda _umo=None: host_profile
    asyncio.run(namespace["authorize_llm"](verified, verified_event, verified_request))
    assert host_profile == before
    assert verified_event.get_extra("_remail_main_agent_ready") is True
    verified_event._remail_original_send.assert_not_awaited()


def test_initialize_preserves_default_and_other_host_profiles(tmp_path, monkeypatch):
    runtime, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    main = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    initialize = next(
        node
        for node in main.body
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "initialize"
    )

    class HostConfig(dict):
        def save_config(self, *_args, **_kwargs):
            pytest.fail("Loading this plugin must not save host configuration")

    default = HostConfig(
        {
            "provider_settings": {
                "show_tool_use_status": True,
                "tool_schema_mode": "skills_like",
            },
            "provider_ltm_settings": {"active_reply": {"enable": True}},
            "platform_settings": {
                "reply_prefix": "original prefix",
                "reply_with_quote": True,
                "id_whitelist": ["original-group"],
            },
            "provider_stt_settings": {"enable": True},
            "content_safety": {"baidu_aip": {"enable": True}},
            "admins_id": ["original-admin"],
            "t2i": True,
        }
    )
    other = deepcopy(default)
    other["platform_settings"]["reply_prefix"] = "other profile prefix"
    profiles = {"default": default, "other": other}
    before = deepcopy(profiles)
    star_api = ModuleType("astrbot.api.star")
    star_api.StarTools = SimpleNamespace(get_data_dir=lambda _name: tmp_path)
    monkeypatch.setitem(sys.modules, "astrbot.api.star", star_api)
    runtime.update(
        _install_binding_log_redaction=lambda: None,
        _install_early_entry_guard=lambda _plugin: lambda: None,
    )
    exec(
        compile(ast.Module(body=[initialize], type_ignores=[]), "main.py", "exec"),
        runtime,
    )
    plugin = SimpleNamespace(
        config={"diagnostics_enabled": False},
        context=SimpleNamespace(
            get_config=lambda _umo=None: default,
            astrbot_config_mgr=SimpleNamespace(confs=profiles),
            register_web_api=lambda *_args: None,
        ),
        _channel_system_keys=lambda: {},
        _websocket_enabled=lambda: False,
        diagnostics_page=AsyncMock(),
    )
    asyncio.run(runtime["initialize"](plugin))
    assert profiles == before
    assert plugin.config == {"diagnostics_enabled": False}
    plugin.diagnostics.close()


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
    assert "_send_private_text" in source
    assert "require_binding=False" in source
    assert source.index("_authorize_event") < source.index("_send_private_text")
    assert "event.send" not in source
    assert "event.stop_event()" in source
    assert "exc.message" not in source
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom)) for node in ast.walk(handler)
    )

    handler.decorator_list = []

    async def logged_operation(_event, _stage, _name, _inputs, awaitable):
        return await awaitable

    namespace = {
        "AstrMessageEvent": object,
        "MessageChain": lambda items: items,
        "Plain": lambda text: text,
        "ReMailError": RuntimeError,
        "_safe_egress_text": lambda text, **_kwargs: text,
        "logged_operation": logged_operation,
        "trace_finish": lambda *_args, **_kwargs: None,
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

    authorization_modes = []

    class Plugin:
        context = Context()

        @staticmethod
        def _private_target(_event):
            return "qq-main:FriendMessage:123456789"

        async def _authorize_event(self, _event, *, require_binding=True):
            authorization_modes.append(require_binding)

        async def _send_private_text(self, _event, text):
            sent.append((self._private_target(_event), [text]))
            return True

    stopped = []
    event = SimpleNamespace(
        stop_event=lambda: stopped.append(True),
    )
    asyncio.run(namespace["remail_help"](Plugin(), event))
    assert sent == [("qq-main:FriendMessage:123456789", [help_text])]
    assert authorization_modes == [False]
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
    assert "priority=-sys.maxsize" in llm_authorize_source
    assert "_authorize_event" in llm_authorize_source
    assert "_request_is_remail" in llm_authorize_source
    assert "_mark_event_owned" in llm_authorize_source
    assert "_reply" in llm_authorize_source
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
    assert "_reply" in bind_source
    assert "event.message_str" in bind_source
    assert "event.get_message_str" not in bind_source
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
        assert "_reply" in handler_source
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
    assert "_send_private_text" in profile_source
    assert "event.send" not in profile_source
    assert profile_source.index("_send_private_text") < profile_source.rindex(
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
    assert "normalize_diagnosis_payload" in diagnosis_source
    assert "event.request_llm" not in diagnosis_source
    assert "context.llm_generate" not in diagnosis_source
    assert "render_diagnosis_fact" in diagnosis_source
    assert "_safe_egress_text" in diagnosis_source
    assert "_reply" in diagnosis_source
    assert not any(
        isinstance(node, (ast.Yield, ast.YieldFrom))
        for node in ast.walk(functions["diagnose_code"])
    )
    tool_source = ast.unparse(functions["remail_code_diagnosis"])
    assert "description.strip()" in tool_source
    assert "body={'email': stored_email}" in tool_source
    assert "stored_email or email" not in tool_source
    assert "normalize_diagnosis_payload" in tool_source
    assert "diagnosis_code" in tool_source
    assert "_reply" in tool_source
    assert "project_id" not in tool_source


def test_diagnosis_binding_required_returns_message_without_llm() -> None:
    functions, _ = _load_welcome_functions()
    tree = ast.parse((PLUGIN_DIR / "main.py").read_text(encoding="utf-8"))
    assignments = [
        node
        for node in tree.body
        if isinstance(node, ast.Assign)
        and any(
            isinstance(target, ast.Name)
            and target.id
            in {
                "_DIAGNOSIS_ARGUMENTS",
                "_UNBOUND_TEXT",
                "_DIAGNOSIS_NOT_VERIFIED_RESPONSE",
            }
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
        "MessageType": SimpleNamespace(GROUP_MESSAGE="group"),
        "Plain": lambda text: text,
        "_enforce_diagnosis_fact": functions["_enforce_diagnosis_fact"],
        "_enforce_black_box": functions["_enforce_black_box"],
        "_enforce_answer_scope": functions["_enforce_answer_scope"],
        "_enforce_group_privacy": functions["_enforce_group_privacy"],
        "_safe_egress_text": functions["_safe_egress_text"],
        "_event_is_private": functions["_event_is_private"],
        "_safe_user_error": lambda _exc: "失败",
        "normalize_diagnosis_payload": normalize_diagnosis_payload,
        "render_diagnosis_fact": render_diagnosis_fact,
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
        payload = {
            "bindingRequired": True,
            "message": (
                "当前账号尚未绑定 ReMail。\n"
                "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
            ),
        }

        async def _request(self, *_args, **kwargs):
            self.request_body = kwargs.get("body")
            return self.payload

        @staticmethod
        def _result_text(payload, fallback):
            return payload.get("message") or fallback

        @staticmethod
        async def _reply(target_event, text):
            await target_event.send([text])
            target_event.stop_event()

    sent = []
    stopped = []

    async def send(message):
        sent.append(message)

    event = SimpleNamespace(
        message_str="/诊断 order@example.com 一直没收到",
        get_message_type=lambda: "friend",
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

    plugin = Plugin()
    plugin.payload = {
        "diagnosisCode": "cause_not_confirmed",
        "projectId": 2,
        "projectName": "ChatGPT",
        "message": "暂未发现明确异常。",
    }
    group_sent = []
    group_stopped = []

    async def send_group(message):
        group_sent.append(message)

    group_event = SimpleNamespace(
        message_str="/诊断 peptide_tech.3k@icloud.com 接不到码",
        get_message_type=lambda: "group",
        send=send_group,
        stop_event=lambda: group_stopped.append(True),
    )
    asyncio.run(namespace["diagnose_code"](plugin, group_event))
    assert group_sent == [
        [
            "该订单对应的是 ChatGPT 项目。 "
            "暂未发现明确异常。 请稍后重试;持续无结果时联系人工客服。"
        ]
    ]
    assert "peptide_tech.3k@icloud.com" not in group_sent[0][0]
    assert group_stopped == [True]


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
    namespace = {
        "AstrMessageEvent": object,
        "json": json,
        "_record_evidence": lambda *_args: None,
        "_REMAIL_ORDER_EMAIL_KEY": "_remail_order_email",
        "diagnosis_fact_payload": diagnosis_fact_payload,
        "normalize_diagnosis_payload": normalize_diagnosis_payload,
    }
    exec(
        compile(
            ast.Module(body=[unbound, handler], type_ignores=[]), "main.py", "exec"
        ),
        namespace,
    )

    class Plugin:
        payload = {}

        async def _request(self, *_args, **kwargs):
            self.request_body = kwargs.get("body")
            return self.payload

        async def _reply(self, _event, text):
            sent.append(text)

        @staticmethod
        def _result_text(payload, fallback):
            return payload.get("message") or fallback

    plugin = Plugin()
    sent = []
    extras = {"_remail_order_email": "trusted-order@example.com"}
    event = SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )
    for payload in (
        {"bindingRequired": True, "message": "请绑定"},
        {"accountUnavailable": True, "message": "账号不可用"},
    ):
        plugin.payload = payload
        result = asyncio.run(
            namespace["remail_code_diagnosis"](
                plugin, event, "order@example.com", "接不到码"
            )
        )
        assert result == ""
    assert sent == [
        "当前账号尚未绑定 ReMail。\n"
        "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。",
        "当前绑定的 ReMail 账号不可用，请重新绑定或联系客服。",
    ]

    plugin.payload = {
        "diagnosisCode": "cause_not_confirmed",
        "projectId": 2,
        "projectName": "ChatGPT",
        "message": "另一个项目 Genspark；邮件主题 Welcome；正文 PRIVATE_BODY",
    }
    result = asyncio.run(
        namespace["remail_code_diagnosis"](plugin, event, "[邮箱已隐藏]", "接不到码")
    )
    assert plugin.request_body == {"email": "trusted-order@example.com"}
    returned = json.loads(result)
    assert returned["projectName"] == "ChatGPT"
    assert returned["message"].startswith("暂未发现明确异常")
    assert "Genspark" not in result
    assert "PRIVATE_BODY" not in result
    assert extras["_remail_code_diagnosis_fact"] == DiagnosisFact(
        diagnosis_code="cause_not_confirmed",
        safe_message="暂未发现明确异常。 请稍后重试；持续无结果时联系人工客服。",
        purchased_project_id=2,
        purchased_project_name="ChatGPT",
    )

    extras.clear()
    plugin.request_body = None
    missing = asyncio.run(
        namespace["remail_code_diagnosis"](
            plugin, event, "invented-other-order@example.com", "接不到码"
        )
    )
    assert "需要提供订单邮箱" in json.loads(missing)["message"]
    assert plugin.request_body is None


def test_system_keys_are_read_from_plugin_config() -> None:
    schema = json.loads((PLUGIN_DIR / "_conf_schema.json").read_text(encoding="utf-8"))
    assert "launch_system_key" not in schema
    assert "platform_system_keys" not in schema
    for field in ("qq_system_key", "telegram_system_key"):
        assert schema[field]["default"] == ""
        assert schema[field]["obvious_hint"] is True
        assert schema[field]["secret"] is True
    assert schema["qq_group_owner_id"]["default"] == ""
    assert "手工填写" in schema["qq_group_owner_id"]["hint"]
    assert schema["qq_group_admin_ids"]["default"] == []
    assert schema["qq_group_management"]["default"] == []
    assert "群号|群主QQ号" in schema["qq_group_management"]["hint"]
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
    assert "实时查询结果" in welcome
    assert "@红夜" in welcome
    assert schema["feedback_enabled"]["description"] == "工作日报"
    assert schema["feedback_enabled"]["default"] is False
    assert "不会收集普通群消息" in schema["feedback_enabled"]["hint"]
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
    authorize.assert_awaited_once_with(event, require_binding=False)
    sent.assert_awaited_once_with(
        [("at", {"qq": "123456789", "name": ""}), ("plain", "欢迎加入")]
    )

    sent.reset_mock()
    authorize.reset_mock()
    plugin.config = {
        "welcome_enabled": False,
        "auto_approve_join_requests": True,
        "welcome_text": "欢迎加入",
    }
    asyncio.run(handler(plugin, event))
    authorize.assert_not_awaited()
    sent.assert_not_awaited()

    denied = qq_event(notice)
    denied.send = AsyncMock()
    plugin.config = {"welcome_enabled": True, "welcome_text": "欢迎加入"}
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
    authorize.assert_awaited_once_with(event, require_binding=False)
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
    plugin._authorize_event.assert_awaited_once_with(event, require_binding=False)
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
    authorize.assert_awaited_once_with(event, require_binding=False)
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


@pytest.mark.parametrize(
    ("target_role", "expected_label"),
    [("owner", "群主"), ("admin", "管理员")],
)
def test_member_mentions_group_management_wake_remail_fae(
    target_role: str, expected_label: str
) -> None:
    functions, _ = _load_welcome_functions()
    mentioned_ids = functions["_mentioned_qq_ids"]
    handler = functions["handoff_group_manager_mentions"]
    extras = {}
    event = SimpleNamespace(
        bot=SimpleNamespace(call_action=AsyncMock()),
        get_platform_name=lambda: "aiocqhttp",
        get_platform_id=lambda: "bot",
        get_message_type=lambda: "group",
        get_sender_id=lambda: "123456789",
        get_group_id=lambda: "529642597",
        get_self_id=lambda: "999999999",
        message_obj=SimpleNamespace(
            message_id="101",
            raw_message={
                "message": [
                    {"type": "at", "data": {"qq": "888888888"}},
                    {"type": "at", "data": {"qq": "999999999"}},
                    {"type": "text", "data": {"text": "接码怎么使用？"}},
                ]
            },
        ),
        message_str="@管理成员(888888888) 接码怎么使用？",
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
        is_wake=False,
        is_at_or_wake_command=False,
    )
    authorize = AsyncMock()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "777777777",
            "qq_group_admin_ids": [],
            "qq_group_management": [
                "529642597|888888888|"
                if target_role == "owner"
                else "529642597|777777777|888888888"
            ],
        },
        _authorize_event=authorize,
    )

    assert mentioned_ids(event) == ["888888888"]
    asyncio.run(handler(plugin, event))

    authorize.assert_awaited_once_with(event)
    assert event.message_str == "@管理成员(888888888) 接码怎么使用？"
    assert extras == {
        "_remail_group_llm_allowed": True,
        "_remail_reply_target": (
            ("bot", "aiocqhttp", "999999999", "529642597", "123456789"),
            "101",
        ),
        "_remail_admin_handoff_role": expected_label,
        "_remail_admin_handoff_text": "接码怎么使用？",
        "_remail_owned": True,
    }
    assert event.is_wake is True
    assert event.is_at_or_wake_command is True
    event.bot.call_action.assert_not_awaited()


def test_group_management_handoff_ignores_privileged_senders_and_unauthorized_groups() -> (
    None
):
    functions, remail_error = _load_welcome_functions()
    handler = functions["handoff_group_manager_mentions"]

    def make_event():
        extras = {}
        event = SimpleNamespace(
            bot=SimpleNamespace(call_action=AsyncMock()),
            get_platform_name=lambda: "aiocqhttp",
            get_platform_id=lambda: "bot",
            get_message_type=lambda: "group",
            get_sender_id=lambda: "123456789",
            get_group_id=lambda: "529642597",
            get_self_id=lambda: "999999999",
            message_obj=SimpleNamespace(
                message_id="101",
                raw_message={"message": [{"type": "at", "data": {"qq": "888888888"}}]},
            ),
            message_str="@群主(888888888)",
            get_extra=lambda key, default=None: extras.get(key, default),
            set_extra=lambda key, value: extras.__setitem__(key, value),
            is_wake=False,
            is_at_or_wake_command=False,
        )
        return event, extras

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "888888888",
            "qq_group_admin_ids": ["123456789"],
        },
        _authorize_event=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    assert extras["_remail_group_llm_allowed"] is False
    assert "_remail_admin_handoff_role" not in extras
    assert event.is_at_or_wake_command is False
    plugin._authorize_event.assert_not_awaited()
    event.bot.call_action.assert_not_awaited()

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={
            "qq_group_owner_id": "777777777",
            "qq_group_admin_ids": [],
        },
        _authorize_event=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    assert extras["_remail_group_llm_allowed"] is False
    assert "_remail_admin_handoff_role" not in extras
    assert event.is_at_or_wake_command is False
    plugin._authorize_event.assert_not_awaited()

    event, extras = make_event()
    plugin = SimpleNamespace(
        config={"qq_group_owner_id": "888888888", "qq_group_admin_ids": []},
        _authorize_event=AsyncMock(side_effect=remail_error("denied")),
        _reply=AsyncMock(),
    )
    asyncio.run(handler(plugin, event))
    plugin._reply.assert_awaited_once()
    event.bot.call_action.assert_not_awaited()
    assert extras["_remail_group_llm_allowed"] is True
    assert "_remail_admin_handoff_role" not in extras
    assert event.is_at_or_wake_command is False


def test_only_explicit_mentions_reach_group_intent_classification() -> None:
    functions, remail_error = _load_welcome_functions()
    handler = functions["classify_mentioned_group_question"]

    async def reply(event, text):
        try:
            await event.send([("plain", text)])
        finally:
            event.stop_event()

    def make_event(
        text: str,
        *,
        mention_bot: bool = False,
        handoff_role: str = "",
        sender_id: str = "123456789",
    ):
        sent = []
        stopped = []
        extras = {}
        if handoff_role:
            extras = {
                "_remail_admin_handoff_role": handoff_role,
                "_remail_admin_handoff_text": text,
            }
        self_id = "999999999"
        segments = []
        if mention_bot:
            segments.append({"type": "at", "data": {"qq": self_id}})
        elif handoff_role:
            segments.append(
                {
                    "type": "at",
                    "data": {
                        "qq": "888888888" if handoff_role == "群主" else "777777777"
                    },
                }
            )
        segments.append({"type": "text", "data": {"text": text}})

        async def send(message):
            sent.append(message)

        event = SimpleNamespace(
            get_extra=lambda key, default="": extras.get(key, default),
            get_platform_name=lambda: "aiocqhttp",
            get_platform_id=lambda: "bot",
            get_message_type=lambda: "group",
            get_group_id=lambda: "529642597",
            get_sender_id=lambda: sender_id,
            get_self_id=lambda: self_id,
            get_messages=lambda: [],
            message_obj=SimpleNamespace(
                message_id="101", raw_message={"message": segments}
            ),
            message_str=f"@{handoff_role} {text}" if handoff_role else text,
            unified_msg_origin="bot:GroupMessage:529642597",
            set_extra=lambda key, value: extras.__setitem__(key, value),
            is_wake=mention_bot or bool(handoff_role),
            is_at_or_wake_command=mention_bot or bool(handoff_role),
            send=send,
            stop_event=lambda: stopped.append(True),
        )
        _support_session(event, None)
        return event, sent, stopped

    def make_plugin(decision: str = "REMAIL"):
        if decision == "API":
            plan_text = json.dumps(
                _fact_plan(
                    intents=("api",),
                    facts=(
                        _fact(
                            "api",
                            "api_documentation",
                            params={"query": "公开 API 咨询"},
                        ),
                    ),
                    answer_mode="public_api",
                ).to_dict(),
                ensure_ascii=False,
            )
        elif decision == "IGNORE":
            plan_text = json.dumps(
                _fact_plan(intents=(), route="ignore").to_dict(),
                ensure_ascii=False,
            )
        elif decision == "REMAIL":
            plan_text = json.dumps(
                _fact_plan(intents=("social",)).to_dict(), ensure_ascii=False
            )
        else:
            plan_text = decision
        return SimpleNamespace(
            config={
                "qq_group_owner_id": "888888888",
                "qq_group_admin_ids": ["777777777"],
            },
            _authorize_event=AsyncMock(),
            _public_api_capability_context=AsyncMock(return_value=""),
            _reply=reply,
            collect_group_feedback=AsyncMock(),
            remail_intent_contexts={},
            context=SimpleNamespace(
                conversation_manager=_ConversationManagerStub(),
                get_current_chat_provider_id=AsyncMock(return_value="provider"),
                llm_generate=AsyncMock(
                    return_value=SimpleNamespace(
                        role="assistant", completion_text=plan_text
                    )
                ),
            ),
        )

    event, sent, stopped = make_event("接码怎么使用？")
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_not_awaited()
    assert not sent
    assert stopped == []

    event, sent, stopped = make_event("红夜，今天天气如何？")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_not_awaited()
    assert not sent
    assert stopped == []

    event, sent, stopped = make_event("/项目 github")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    plugin.collect_group_feedback.assert_not_awaited()
    assert not sent and not stopped

    event, sent, stopped = make_event("!今天天气如何？")
    event.is_at_or_wake_command = True
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin.context.llm_generate.assert_not_awaited()
    assert not sent
    assert stopped == []

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_awaited_once_with(event)
    assert plugin.context.llm_generate.await_count == 2
    assert [
        json.loads(call.kwargs["prompt"])["workflowPhase"]
        for call in plugin.context.llm_generate.await_args_list
    ] == ["intent", "planner"]
    assert event.get_extra("provider_request") is not None
    classifier = json.loads(plugin.context.llm_generate.await_args.kwargs["prompt"])
    assert classifier["untrustedQuestion"] == normalize_security_text("接码怎么使用？")
    assert classifier["untrustedRecentContext"] == ""
    assert classifier["messageContext"]["isGroup"] is True
    assert not sent and not stopped

    credential_event, sent, stopped = make_event(
        "帮我排查，Token: secret-token-value", mention_bot=True
    )
    credential_plugin = make_plugin()
    asyncio.run(handler(credential_plugin, credential_event))
    credential_plugin.context.llm_generate.assert_not_awaited()
    assert sent and "不会发送给模型" in sent[0][0][1]
    assert stopped == [True]

    event, sent, stopped = make_event(
        "下单时，Gmail 变种邮箱后缀应该填什么", mention_bot=True
    )
    api_plugin = make_plugin("API")
    capabilities = '{"operations":[{"method":"POST","path":"/v1/open/orders"}]}'
    api_plugin._public_api_capability_context.return_value = capabilities
    asyncio.run(handler(api_plugin, event))
    assert event.get_extra("_remail_api_consultation", False) is True
    api_payload = json.loads(
        api_plugin.context.llm_generate.await_args.kwargs["prompt"]
    )
    assert api_payload["publicApiCapabilities"] == capabilities
    assert not sent and not stopped

    plugin.context.llm_generate.reset_mock()
    follow_up, sent, stopped = make_event("那多久？", mention_bot=True)
    asyncio.run(handler(plugin, follow_up))
    follow_up_payload = json.loads(
        plugin.context.llm_generate.await_args.kwargs["prompt"]
    )
    assert "ownOrders" not in follow_up_payload.pop("dynamicBackground")
    assert follow_up_payload == {
        "workflowPhase": "planner",
        "initialIntent": _fact_plan(intents=("social",)).to_dict(),
        "untrustedQuestion": normalize_security_text("那多久？"),
        "untrustedRecentContext": normalize_security_text("接码怎么使用？"),
        "publicApiCapabilities": "",
        "messageContext": {
            "isGroup": True,
            "hasOrderEmail": False,
            "entryPoint": "mentioned_group_support",
        },
    }
    assert not sent and not stopped

    plugin.context.llm_generate.reset_mock()
    other_sender, sent, stopped = make_event(
        "那多久？", mention_bot=True, sender_id="987654321"
    )
    asyncio.run(handler(plugin, other_sender))
    other_payload = json.loads(plugin.context.llm_generate.await_args.kwargs["prompt"])
    assert "dynamicBackground" in other_payload
    other_payload.pop("dynamicBackground")
    assert other_payload == {
        "workflowPhase": "planner",
        "initialIntent": _fact_plan(intents=("social",)).to_dict(),
        "untrustedQuestion": normalize_security_text("那多久？"),
        "untrustedRecentContext": "",
        "publicApiCapabilities": "",
        "messageContext": {
            "isGroup": True,
            "hasOrderEmail": False,
            "entryPoint": "mentioned_group_support",
        },
    }
    assert not sent and not stopped

    event, sent, stopped = make_event("今天天气如何？", mention_bot=True)
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", functions["_REMAIL_ONLY_TEXT"])]]
    assert stopped == [True]
    plugin.context.llm_generate.assert_awaited_once()
    assert plugin.context.conversation_manager.conversations == {}

    event, sent, stopped = make_event("今天天气如何？", handoff_role="群主")
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_awaited_once()
    assert plugin.context.conversation_manager.conversations == {}
    assert not sent
    assert not stopped
    assert event.is_wake is False
    assert event.is_at_or_wake_command is False
    assert event.message_str == "@群主 今天天气如何？"

    event, sent, stopped = make_event("API 怎么调用？", handoff_role="管理员")
    plugin = make_plugin()
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    assert plugin.context.llm_generate.await_count == 2
    assert event.is_wake is True
    assert event.is_at_or_wake_command is True
    assert event.message_str == "API 怎么调用？"
    assert not sent and not stopped

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin("")
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", functions["_REMAIL_INTENT_UNAVAILABLE_TEXT"])]]
    assert stopped == [True]
    assert plugin.context.llm_generate.await_count == 2
    assert plugin.context.conversation_manager.conversations == {}

    event, sent, stopped = make_event("/weather", mention_bot=True)
    plugin = make_plugin("IGNORE")
    asyncio.run(handler(plugin, event))
    plugin._authorize_event.assert_not_awaited()
    plugin.context.llm_generate.assert_not_awaited()
    assert not sent and not stopped
    assert plugin.context.conversation_manager.conversations == {}

    event, sent, stopped = make_event("接码怎么使用？", mention_bot=True)
    plugin = make_plugin()
    plugin._authorize_event = AsyncMock(side_effect=remail_error("denied"))
    asyncio.run(handler(plugin, event))
    assert sent == [[("plain", "当前会话未获授权。")]]
    assert stopped == [True]
    plugin.context.llm_generate.assert_not_awaited()
    assert plugin.context.conversation_manager.conversations == {}

    source = (PLUGIN_DIR / "main.py").read_text(encoding="utf-8")
    assert "proactively_answer_remail_questions" not in source
    assert "_remail_proactive_support" not in source


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
    assert render({}, {"announcements": []}) == "暂时无法读取系统通知或公告。"
    assert render({"notice": ""}, {"announcements": []}) == "暂无系统通知或公告。"


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
        "_safe_egress_text": lambda text, **_kwargs: text,
        "MessageChain": lambda items: items,
        "Plain": lambda value: value,
        "logger": SimpleNamespace(info=lambda *_args: None, warning=lambda *_args: None),
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
