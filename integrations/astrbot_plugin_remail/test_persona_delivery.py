"""Offline delivery wiring checks; model doubles do not establish LLM quality."""

import asyncio
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .diagnostics import DiagnosticLog, LLMDeadlineExceeded, LLM_TIMEOUT_TEXT
from .persona import (
    CRITIC_SYSTEM_PROMPT,
    FACT_REPAIR_SYSTEM_PROMPT,
    PERSONA_SYSTEM_PROMPT,
)
from .security import normalize_security_text
from .test_background import event_for
from .test_security import _fact, _fact_plan, _load_welcome_functions


def _recharge_event(functions, *, question="怎么充值"):
    event = event_for(private=False, question=question)
    event.set_extra("_remail_owned", True)
    event.set_extra(
        "_remail_personality_style", "沉稳直接，像熟悉业务的朋友，少说套话。"
    )
    event.set_extra(
        "_remail_intent_plan_v1",
        _fact_plan(
            intents=("recharge",), facts=(_fact("recharge", "recharge_config"),)
        ),
    )
    diagnostics = DiagnosticLog(None)
    diagnostics.attach(event)
    functions["_record_evidence"](
        event,
        "recharge_config",
        {
            "sourceValid": True,
            "enabled": True,
            "paymentMethods": ["epusdt_usdt_tron"],
            "paymentCurrencies": {"epusdt_usdt_tron": "USDT"},
            "minPoints": "10000",
            "feeRate": "0.06",
            "feeCapPoints": "0",
            "tiers": [{"points": "50000.00", "feePoints": "0.00"}],
            "redemptionCodePurchaseUrl": "https://cards.example.test/shop",
        },
        {"background": True},
    )
    return event, diagnostics


def _response(text, *, used=("recharge",)):
    return SimpleNamespace(
        role="assistant",
        completion_text=json.dumps(
            {"answer": text, "usedEvidence": list(used), "seals": []},
            ensure_ascii=False,
        ),
    )


def _critic(*, approve=True, used=("recharge",), violation="off_topic", issue=None):
    result = {
        "decision": "approve" if approve else "reject",
        "supportedEvidence": list(used),
        "violations": [] if approve else [violation],
    }
    if issue is not None:
        text, reason = issue
        result["issues"] = [{"text": normalize_security_text(text), "reason": reason}]
    return SimpleNamespace(
        role="assistant", completion_text=json.dumps(result, ensure_ascii=False)
    )


def _stage(kwargs, payload):
    assert kwargs["tools"] is None and kwargs["contexts"] is None
    if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
        return "writer"
    if kwargs["system_prompt"] == FACT_REPAIR_SYSTEM_PROMPT:
        return "fact_repair"
    assert kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT
    assert payload["reviewMode"] in {"facts", "delivery"}
    return payload["reviewMode"]


def _assert_writer_input(payload, approved, *, used=("recharge",)):
    assert set(payload) == {
        "question", "authoritativeAnswer", "personalityStyle",
        "requiredEvidence", "immutableSeals",
    }
    assert payload["authoritativeAnswer"] == normalize_security_text(approved)
    assert payload["requiredEvidence"] == list(used)
    assert payload["immutableSeals"] == []


def _deliver(functions, event, generate, *, draft="当前在线支付可以充值积分。"):
    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    response = SimpleNamespace(role="assistant", completion_text=draft)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    return response.completion_text, context


@pytest.mark.parametrize(
    "answer",
    ["在用户后台使用USDT充值积分。", "在用户后台使用 USDT（TRON 网络）充值积分。"],
)
def test_public_user_console_payment_help_survives_the_implementation_gate(answer):
    functions, _ = _load_welcome_functions()
    assert functions["_enforce_black_box"](
        answer, "用户后台怎么充值？"
    ) == normalize_security_text(answer)


@pytest.mark.parametrize(
    "answer",
    [
        "系统按分桶策略处理。", "系统执行同项目历史排除。", "系统采用资源复用策略。",
        "系统采用别名复用策略。", "ReMail 用 PostgreSQL 保存订单。",
    ],
)
def test_public_api_question_does_not_allow_private_strategy_disclosure(answer):
    functions, _ = _load_welcome_functions()
    assert functions["_enforce_black_box"](
        answer, "公开 API 的响应字段应该如何解析？"
    ) == functions["_BLACK_BOX_RESPONSE"]


def test_react_final_answer_precedes_privacy_and_the_expression_only_writer():
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = (
        "去 ReMail 钱包用 USDT（TRON 网络）充值积分。"
        "也能从 https://cards.example.test/shop 买兑换码，再回 ReMail 兑换积分。"
        "你想用哪种方式？"
    )
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            assert payload["candidateAnswer"] == normalize_security_text(approved)
            assert "USDT(TRON 网络)" in next(
                item["summary"] for item in payload["evidence"]
                if item["id"] == "recharge"
            )
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            assert "熟悉业务的朋友" in payload["personalityStyle"]
            return _response(approved)
        assert stage == "delivery"
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        assert payload["candidateAnswer"] == normalize_security_text(approved)
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery"]
    assert context.llm_generate.await_count == 3
    assert "epusdt_usdt_tron" not in actual and "50000" not in actual
    entries = log.entries
    react = next(row for row in entries if row["stage"] == "react" and row["outcome"] == "started")
    agent = next(row for row in entries if row["stage"] == "agent" and row["outcome"] == "completed")
    privacy = next(row for row in entries if row["stage"] == "privacy" and row["outcome"] == "started")
    writer = next(row for row in entries if row["stage"] == "writer" and row["outcome"] == "started")
    assert react["seq"] < agent["seq"] < privacy["seq"] < writer["seq"]
    assert agent["details"]["output"]["completion_text"] == normalize_security_text(approved)
    assert not any(row["stage"] == "react" for row in entries if row["seq"] > privacy["seq"])


def test_style_rejection_retries_the_same_complete_approved_answer():
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    style = "沉稳直接，像熟悉业务的朋友，少说套话；不要称呼用户为亲。"
    event.set_extra("_remail_personality_style", style)
    approved = "去 ReMail 钱包选 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    first = "亲，" + approved
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            assert normalize_security_text(style) in payload["personalityStyle"]
            if calls.count("writer") == 1:
                return _response(first)
            assert payload["personalityStyle"] != normalize_security_text(style)
            return _response(approved)
        assert stage == "delivery"
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        assert payload["personalityStyle"] == normalize_security_text(style)
        if calls.count("delivery") == 1:
            return _critic(approve=False, violation="style_mismatch")
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


@pytest.mark.parametrize("first_failure", ["style_mismatch", "invalid_writer"])
def test_two_failed_writer_attempts_fall_back_to_the_complete_approved_answer(first_failure):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "可以用 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    original_style = normalize_security_text(event.get_extra("_remail_personality_style"))
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            if calls.count("writer") == 1 and first_failure == "invalid_writer":
                return SimpleNamespace(role="assistant", completion_text="bad json")
            if calls.count("writer") == 2:
                assert payload["personalityStyle"] != original_style
            return _response("亲，" + approved)
        assert stage == "delivery"
        return _critic(approve=False, violation="style_mismatch")

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert "亲" not in actual
    expected = ["facts", "writer", "delivery", "writer", "delivery"]
    if first_failure == "invalid_writer":
        expected.pop(2)
    assert calls == expected and context.llm_generate.await_count == len(expected)
    assert event.get_extra("_remail_persona_attempts", 0) == 2
    assert any(row["details"].get("reason") == "approved_answer" for row in log.entries)


@pytest.mark.parametrize(
    "invalid_review",
    [
        pytest.param('{"violations":["style_mismatch"]}', id="missing_fields"),
        pytest.param(
            '{"decision":"reject","supportedEvidence":[],"violations":["style_mismatch"]}',
            id="rejection_without_support",
        ),
        pytest.param(
            '{"decision":"reject","decision":"reject",'
            '"supportedEvidence":["recharge"],"violations":["style_mismatch"]}',
            id="duplicate_key",
        ),
    ],
)
def test_invalid_delivery_review_only_retries_expression_from_the_locked_answer(invalid_review):
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    approved = "去 ReMail 钱包选 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response(approved)
        assert stage == "delivery"
        if calls.count("delivery") == 1:
            return SimpleNamespace(role="assistant", completion_text=invalid_review)
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


@pytest.mark.parametrize(
    "failure", ["invalid_writer", "invalid_role", "unsupported_literal", "critic_rejected"]
)
def test_failed_expression_gets_one_retry_without_changing_the_approved_answer(failure):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "去 ReMail 钱包选 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            if calls.count("writer") == 1:
                if failure == "invalid_writer":
                    return SimpleNamespace(role="assistant", completion_text="bad json")
                if failure == "invalid_role":
                    return SimpleNamespace(role="tool", completion_text="ignored")
                if failure == "unsupported_literal":
                    return _response("请访问 https://unknown.example.test/pay。")
            return _response(approved)
        assert stage == "delivery"
        return _critic(approve=calls.count("writer") == 2)

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert calls.count("writer") == 2
    assert context.llm_generate.await_count == (
        5 if failure in {"critic_rejected", "unsupported_literal"} else 4
    )
    if failure != "unsupported_literal":
        assert any(row["details"].get("reason") == failure for row in log.entries)
    assert any(row["stage"] == "writer" and row["outcome"] == "fallback" for row in log.entries)


@pytest.mark.parametrize(
    "draft",
    [
        "去 https://unlisted.example.test/pay 充值积分。",
        "使用 `UNLISTED_PROJECT_X9` 项目充值积分。",
    ],
)
def test_react_does_not_apply_removed_literal_gate_to_critic_approved_answer(draft):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            # The removed deterministic literal gate must not override the critic.
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, draft)
            return _response(draft)
        assert stage == "delivery"
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=draft)
    assert actual == normalize_security_text(draft)
    assert calls == ["facts", "writer", "delivery"]
    assert context.llm_generate.await_count == len(calls)
    assert not any(
        row["stage"] == "react"
        and row["details"].get("reason") == "unsupported_literal"
        for row in log.entries
    )


@pytest.mark.parametrize("inference_stage", ["react", "writer"])
def test_numeric_inference_is_reviewed_in_react_and_cannot_start_in_writer(inference_stage):
    functions, _ = _load_welcome_functions()
    question = "两次最低充值合计多少积分？" if inference_stage == "react" else "最低每次充值多少积分？"
    event, _ = _recharge_event(functions, question=question)
    original = "最低每次充值10000积分。"
    computed = "最低每次充值10000积分，两次合计20000积分。"
    approved = computed if inference_stage == "react" else original
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response(computed if calls.count("writer") == 1 else approved)
        assert stage == "delivery"
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(computed)
    expected = ["facts", "writer", "delivery"]
    assert calls == expected and context.llm_generate.await_count == len(expected)


@pytest.mark.parametrize(
    "question,bad_clause,correction",
    [
        (
            "iCloud邮箱能用多久？",
            "购买邮箱的使用寿命由激活窗口和质保期决定。",
            "购买是长效服务，服务正常且未退款或终止时可持续使用；激活窗口和质保不是使用寿命。",
        ),
        (
            "iCloud邮箱只能用24小时吗？",
            "这里的24小时就是购买邮箱的使用期限。",
            "不能仅凭24小时认定购买邮箱到期；要看页面字段是接码、激活还是质保。",
        ),
    ],
)
def test_duration_is_corrected_before_react_finishes_and_writer_preserves_the_result(
    question, bad_clause, correction
):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question=question)
    event.set_extra("_remail_owned", True)
    event.set_extra(
        "_remail_intent_plan_v1",
        _fact_plan(
            intents=("project",), entities={"projectQuery": "icloud"},
            facts=(_fact("icloud", "projects", params={"search": "icloud"}),),
        ),
    )
    functions["_record_evidence"](
        event, "projects", {"items": [], "total": 0, "truncated": False},
        {"search": "icloud", "offset": 0},
    )
    modes = "接码是短期单次服务，购买是长效服务。"
    initial, repaired = modes + bad_clause, modes + correction
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        assert normalize_security_text(question) in payload["question"]
        if stage == "facts":
            if calls.count("facts") == 1:
                return _critic(
                    approve=False, used=(), violation="reversed_relation",
                    issue=(bad_clause, "期限字段与使用寿命不是同一概念；保留正确的模式区别。"),
                )
            assert payload["candidateAnswer"] == normalize_security_text(repaired)
            return _critic(used=("policy.business",))
        if stage == "fact_repair":
            assert payload["agentDraft"] == normalize_security_text(initial)
            assert payload["authoritativeAnswer"] == normalize_security_text(initial)
            assert payload["reviewFeedback"]["violations"] == ["reversed_relation"]
            assert any(item["id"] == "policy.business" for item in payload["evidence"])
            return _response(repaired, used=("policy.business",))
        if stage == "writer":
            _assert_writer_input(payload, repaired, used=("policy.business",))
            assert "没有查询到" not in payload["authoritativeAnswer"]
            return _response(repaired, used=("policy.business",))
        assert payload["approvedAnswer"] == normalize_security_text(repaired)
        return _critic(used=("policy.business",))

    actual, context = _deliver(functions, event, generate, draft=initial)
    assert actual == normalize_security_text(repaired)
    assert "短期单次" in actual and "长效" in actual
    assert "没有查询到" not in actual and "价格条目" not in actual
    assert calls == ["facts", "fact_repair", "facts", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


@pytest.mark.parametrize(
    "wrong,violation",
    [
        ("优先选择在线 USDT 充值积分，卡网只作补充。", "off_topic"),
        ("当前已开放支付宝充值积分，可以直接选择支付宝。", "unsupported_claim"),
    ],
)
def test_qq_recharge_priority_is_resolved_before_the_writer(wrong, violation):
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    event.set_extra("_remail_reply_channel", "qq")
    approved = (
        "先去 https://cards.example.test/shop 购买积分兑换码，"
        "回自己的 ReMail 钱包在兑换码充值处输入兑换码。"
        "也支持 USDT（TRON 网络）在线充值，按支付页操作即可。"
    )
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            assert payload["replyChannel"] == "qq"
            if calls.count("facts") == 1:
                return _critic(
                    approve=False, used=(), violation=violation,
                    issue=(wrong, "QQ应先介绍当前卡网入口，不能虚构未开放的支付宝。"),
                )
            return _critic()
        if stage == "fact_repair":
            assert payload["replyChannel"] == "qq"
            assert payload["reviewFeedback"]["violations"] == [violation]
            assert payload["agentDraft"] == normalize_security_text(wrong)
            return _response(approved)
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response(approved)
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=wrong)
    assert actual == normalize_security_text(approved)
    assert actual.index("兑换码") < actual.index("USDT") and "支付宝" not in actual
    assert calls == ["facts", "fact_repair", "facts", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


def test_react_repair_corrects_recharge_steps_before_expression_is_locked():
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    wrong = (
        "先输入充值金额，用 USDT 完成转账（含手续费）；"
        "输入 ReMail 账号兑换积分，转账后立即到账，去邮箱订单记录确认。"
    )
    approved = (
        "在 ReMail 钱包输入或选择要充值的积分数，当前最低充值配置为10000积分。"
        "选择 USDT（TRON 网络）时，按支付页显示的实际币种和金额付款。"
        "如果使用兑换码，在本人钱包的兑换码充值处输入兑换码。"
        "到钱包账单查看充值或兑换结果，确认可用积分余额后再下单。"
    )
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            if calls.count("facts") == 1:
                return _critic(
                    approve=False, used=(), violation="unsupported_claim",
                    issue=(wrong, "输入积分、兑换码、渠道费用与结果查询位置须按公开规则修正，不能保证即时到账。"),
                )
            assert payload["candidateAnswer"] == normalize_security_text(approved)
            return _critic()
        if stage == "fact_repair":
            assert payload["agentDraft"] == normalize_security_text(wrong)
            assert payload["reviewFeedback"]["violations"] == ["unsupported_claim"]
            assert "10000" in next(item["summary"] for item in payload["evidence"] if item["id"] == "recharge")
            return _response(approved)
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response(approved)
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        return _critic()

    actual, context = _deliver(functions, event, generate, draft=wrong)
    assert actual == normalize_security_text(approved)
    assert "10000积分" in actual and "含手续费" not in actual and "立即到账" not in actual
    assert "邮箱订单记录" not in actual
    assert calls == ["facts", "fact_repair", "facts", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


def test_final_react_answer_can_follow_a_new_topic_without_old_plans_forced_evidence():
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions, question="gmal可以接码注册dola吗 好像没看到这个项目")
    event.set_extra("_remail_same_sender_context", "怎么充值")
    functions["_record_evidence"](
        event, "projects", {"items": [], "total": 0, "truncated": False},
        {"search": "dola", "offset": 0},
    )
    approved = (
        "先看你要注册的项目：目前没查到 Dola 的匹配项目，"
        "不能只凭 Gmail 就保证支持注册。你说的 Dola 是哪个服务？"
    )
    project_ids = []
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        assert "gmal可以接码注册dola吗" in payload["question"]
        if stage == "facts":
            project_ids.extend(item["id"] for item in payload["evidence"] if item["id"].startswith("tool.projects."))
            assert project_ids and payload["requiredEvidence"] == []
            assert "充值" not in payload["candidateAnswer"]
            return _critic(used=project_ids)
        if stage == "writer":
            _assert_writer_input(payload, approved, used=project_ids)
            return _response(approved, used=project_ids)
        assert payload["requiredEvidence"] == project_ids
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        return _critic(used=project_ids)

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery"] and context.llm_generate.await_count == 3


def test_unapproved_off_topic_config_never_reaches_writer_or_final_fallback():
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions, question="Gmail可以接码注册dola吗")
    raw = functions["_grounded_dynamic_answer"](event, event.message_str)
    assert "当前可用充值配置" in raw
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic(approve=False, violation="off_topic")
        assert stage == "fact_repair"
        return _response(raw)

    actual, context = _deliver(functions, event, generate, draft=raw)
    assert actual == normalize_security_text(functions["_REMAIL_SAFE_ERROR_TEXT"])
    assert "充值配置" not in actual and "0.06" not in actual
    assert calls == ["facts", "fact_repair", "facts"]
    assert context.llm_generate.await_count == 3
    reviews = [row["details"].get("reviewFeedback") for row in log.entries if row["outcome"] == "rejected"]
    assert sum(isinstance(review, dict) and review["violations"] == ["off_topic"] for review in reviews) == 2


def test_sync_failure_guard_cannot_send_unreviewed_business_or_old_canonical():
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    event.set_extra(functions["_REMAIL_CANONICAL_RESPONSE_KEY"], "旧问题的未验证回答")
    assert functions["_safe_response_fallback"](event) == functions["_REMAIL_SAFE_ERROR_TEXT"]


def test_delivery_critic_must_support_all_locked_sources_before_using_reworded_text():
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "可以使用 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response("好的，" + approved)
        assert stage == "delivery" and payload["requiredEvidence"] == ["recharge"]
        return _critic(used=())

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved) and not actual.startswith("好的")
    assert context.llm_generate.await_count == 5
    assert sum(row["details"].get("reason") == "invalid_critic" for row in log.entries) == 2
    assert any(row["details"].get("reason") == "approved_answer" for row in log.entries)


@pytest.mark.parametrize("missing", [
    "卡网入口是 https://cards.example.test/shop。",
    "最低充值 10000 积分。",
])
def test_writer_omission_is_left_to_semantic_review(missing):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "最低充值 10000 积分。卡网入口是 https://cards.example.test/shop。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic()
        if stage == "writer":
            _assert_writer_input(payload, approved)
            return _response(missing if calls.count("writer") == 1 else approved)
        assert stage == "delivery"
        assert payload["candidateAnswer"] == normalize_security_text(missing)
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        return _critic()

    actual, _ = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(missing)
    assert calls == ["facts", "writer", "delivery"]
    assert not any(
        row["details"].get("reason") == "omitted_literal" for row in log.entries
    )


@pytest.mark.parametrize(
    "missing",
    [
        "接码是短期单次服务，购买是长效服务，可以持续使用。页面24小时可能指接码时效、激活窗口或质保期。",
        "接码是短期单次服务，购买是长效服务。服务正常且未退款或终止时可持续使用。页面24小时可能指激活窗口。",
    ],
)
def test_writer_cannot_drop_a_condition_or_one_of_the_correct_possibilities(missing):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question="页面写24小时，购买邮箱能用多久？")
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))
    approved = (
        "接码是短期单次服务，购买是长效服务。"
        "服务正常且未退款或终止时，购买邮箱可持续使用。"
        "页面24小时可能指接码时效、激活窗口或质保期；这些不直接等同于购买邮箱的使用寿命。"
    )
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts":
            return _critic(used=("policy.business",))
        if stage == "writer":
            _assert_writer_input(payload, approved, used=("policy.business",))
            return _response(
                missing if calls.count("writer") == 1 else approved,
                used=("policy.business",),
            )
        assert stage == "delivery"
        assert payload["approvedAnswer"] == normalize_security_text(approved)
        if calls.count("delivery") == 1:
            return _critic(
                approve=False, used=("policy.business",), violation="omitted_fact",
                issue=(missing, "不能删除原文中的适用条件或任一正确的并列可能性。"),
            )
        return _critic(used=("policy.business",))

    actual, context = _deliver(functions, event, generate, draft=approved)
    assert actual == normalize_security_text(approved)
    assert "未退款或终止" in actual and "接码时效、激活窗口或质保期" in actual
    assert calls == ["facts", "writer", "delivery", "writer", "delivery"]
    assert context.llm_generate.await_count == 5


@pytest.mark.parametrize("timeout_stage", ["facts", "writer", "delivery"])
def test_only_missing_react_final_answer_uses_timeout_text(timeout_stage):
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    approved = "可以使用 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == timeout_stage:
            logged_stage = {"facts": "react", "writer": "writer", "delivery": "critic"}[stage]
            event.set_extra("_remail_llm_timeout_stage", logged_stage)
            raise LLMDeadlineExceeded(logged_stage, 90)
        if stage == "facts":
            return _critic()
        assert stage == "writer"
        _assert_writer_input(payload, approved)
        return _response("好的，" + approved)

    actual, _ = _deliver(functions, event, generate, draft=approved)
    if timeout_stage == "facts":
        assert actual == normalize_security_text(LLM_TIMEOUT_TEXT)
        assert calls == ["facts"]
    else:
        assert actual == normalize_security_text(approved)
        assert calls == (["facts", "writer"] if timeout_stage == "writer" else ["facts", "writer", "delivery"])


@pytest.mark.parametrize("cancel_stage", ["facts", "fact_repair", "writer", "delivery"])
def test_cancelled_delivery_does_not_start_a_recovery_model_call(cancel_stage):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "可以使用 USDT（TRON 网络）充值积分，支付金额以本次页面为准。"
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == cancel_stage:
            raise asyncio.CancelledError()
        if stage == "facts":
            return _critic(approve=cancel_stage != "fact_repair")
        assert stage == "writer"
        return _response(approved)

    with pytest.raises(asyncio.CancelledError):
        _deliver(functions, event, generate, draft=approved)
    expected = {
        "facts": ["facts"], "fact_repair": ["facts", "fact_repair"],
        "writer": ["facts", "writer"], "delivery": ["facts", "writer", "delivery"],
    }
    assert calls == expected[cancel_stage]
    assert not any(row["stage"] == "writer" and row["outcome"] == "fallback" for row in log.entries)


@pytest.mark.parametrize("draft", ["", " \n", None])
def test_empty_terminal_answer_without_tools_returns_a_safe_error(draft):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    generate = AsyncMock()
    actual, context = _deliver(functions, event, generate, draft=draft)
    assert actual == normalize_security_text(functions["_REMAIL_SAFE_ERROR_TEXT"])
    generate.assert_not_called()
    context.llm_generate.assert_not_called()
    assert any(
        row["stage"] == "agent" and row["outcome"] == "failed"
        and row["details"].get("reason") == "empty_final_answer"
        for row in log.entries
    )


@pytest.mark.parametrize("field", ["tools_call_name", "tool_calls"])
def test_tool_call_response_continues_without_becoming_an_empty_final_failure(field):
    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    context = SimpleNamespace(llm_generate=AsyncMock())
    calls = ["remail_orders"] if field == "tools_call_name" else [
        {"id": "call-1", "function": {"name": "remail_orders", "arguments": "{}"}}
    ]
    role = "tool" if field == "tools_call_name" else "assistant"
    response = SimpleNamespace(
        role=role, completion_text="工具执行前的内部草稿", **{field: calls}
    )
    messages = [SimpleNamespace(role="assistant", content="原运行上下文", tool_calls=calls)]
    run_context = SimpleNamespace(context=SimpleNamespace(event=event), messages=messages)

    async def run():
        plugin = SimpleNamespace(context=context)
        await functions["enforce_redemption_channel_priority"](plugin, event, response)
        await functions["snapshot_safe_remail_response"](plugin, event, response)
        await functions["sync_safe_response_history"](plugin, event, run_context, response)

    asyncio.run(run())
    assert response.completion_text == "" and getattr(response, field) is calls
    assert response.role == role and run_context.messages is messages
    assert messages[0].content == "原运行上下文"
    assert event.get_extra(functions["_REMAIL_CANONICAL_RESPONSE_KEY"]) is None
    context.llm_generate.assert_not_called()
    assert not any(row["stage"] in {"agent", "react", "writer", "critic"} for row in log.entries)
