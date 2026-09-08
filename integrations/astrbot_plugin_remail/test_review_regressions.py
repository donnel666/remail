import json
from types import SimpleNamespace

import pytest

from .security import normalize_security_text
from .test_background import event_for
from .test_persona_delivery import _critic, _deliver, _recharge_event, _response, _stage
from .test_security import _fact, _fact_plan, _load_welcome_functions


def test_repair_keeps_all_critic_issues_and_validates_reasoning_json():
    functions, _ = _load_welcome_functions()
    event, _ = _recharge_event(functions)
    original = "请访问 https://wrong.example.test/pay 充值。"
    approved = "可以使用兑换码充值积分。"
    issues = [
        {
            "text": normalize_security_text(original),
            "reason": f"第{i}项：请核对当前支付入口。",
        }
        for i in range(8)
    ]
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "facts" and calls.count("facts") == 1:
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "decision": "reject",
                        "supportedEvidence": [],
                        "violations": ["unsupported_claim"],
                        "issues": issues,
                    },
                    ensure_ascii=False,
                ),
            )
        if stage == "fact_repair":
            assert payload["reviewFeedback"]["issues"] == issues
            assert payload["reviewFeedback"]["literalCheck"]
            return SimpleNamespace(
                role="assistant",
                completion_text="",
                reasoning_content=_response(approved).completion_text,
            )
        if stage == "writer":
            assert payload["authoritativeAnswer"] == normalize_security_text(approved)
            return _response(approved)
        return _critic()

    answer, _ = _deliver(functions, event, generate, draft=original)
    assert answer == normalize_security_text(approved)
    assert calls == ["facts", "fact_repair", "facts", "writer", "delivery"]


def test_writer_rate_limit_is_recorded_once_and_preserves_approved_answer():
    class RateLimitError(RuntimeError):
        status_code = 429

    functions, _ = _load_welcome_functions()
    event, log = _recharge_event(functions)
    approved = "可以使用兑换码充值积分。"
    calls = []

    async def generate(**kwargs):
        stage = _stage(kwargs, json.loads(kwargs["prompt"]))
        calls.append(stage)
        if stage == "writer":
            raise RateLimitError("rate limited")
        return _critic()

    answer, _ = _deliver(functions, event, generate, draft=approved)
    assert answer == normalize_security_text(approved)
    assert calls == ["facts", "writer"]
    assert (
        sum(
            row["stage"] == "writer" and row["outcome"] == "failed"
            for row in log.entries
        )
        == 1
    )
    assert event.get_extra("_remail_writer_retry_blocked") == "rate_limit"


@pytest.mark.parametrize("question,intent,approved", [
    ("example@outlook.com 这个邮箱怎么导入 ReMail？", "service", "打开邮箱资源页面，点击导入，按页面给出的格式提交。"),
    ("我的绑定邮箱是 example@outlook.com，怎么充值？", "recharge", "打开钱包充值页面，按页面提示充值积分。"),
    ("example@outlook.com 这个账号怎么绑定？", "account", "请在私聊中按绑定命令格式操作。"),
])
def test_email_in_an_ordinary_usage_question_does_not_force_order_diagnosis(question, intent, approved):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question=question)
    event.set_extra("_remail_owned", True)
    functions["_prepare_owned_event_input"](event)
    claim = {"recharge": "recharge_config", "account": "binding_status"}.get(intent)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(
        intents=(intent,), facts=(_fact(intent, claim),) if claim else (),
    ))
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        stage = _stage(kwargs, payload)
        calls.append(stage)
        if stage == "writer":
            return _response(approved, used=())
        return _critic(used=())

    answer, _ = _deliver(functions, event, generate, draft=approved)
    assert answer == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery"]


@pytest.mark.parametrize("question", [
    "example@outlook.com 一直收不到验证码",
    "查一下 example@outlook.com",
])
def test_real_mailbox_diagnosis_still_requires_fact_before_models(question):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question=question)
    event.set_extra("_remail_owned", True)
    functions["_prepare_owned_event_input"](event)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("social",)))

    async def generate(**_kwargs):
        pytest.fail("an unverified individual mailbox must not reach an output model")

    answer, context = _deliver(functions, event, generate, draft="我来说明一下。")
    assert answer == normalize_security_text(functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"])
    context.llm_generate.assert_not_awaited()


def test_writer_cannot_add_receipt_claim_to_usage_help_with_an_email():
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question="example@outlook.com 这个邮箱怎么导入？")
    event.set_extra("_remail_owned", True)
    functions["_prepare_owned_event_input"](event)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))

    async def generate(**kwargs):
        if _stage(kwargs, json.loads(kwargs["prompt"])) == "writer":
            return _response("你的邮箱实际已经收到邮件，你买错项目了。", used=())
        return _critic(used=())

    answer, _ = _deliver(functions, event, generate, draft="打开资源页面，点击导入。")
    assert answer == normalize_security_text(functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"])


@pytest.mark.parametrize("question", [
    "这个订单邮箱一直没信",
    "我买错项目了，怎么办？",
])
def test_personal_diagnosis_cannot_be_exempted_by_a_service_plan(question):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question=question)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))

    async def generate(**_kwargs):
        pytest.fail("a provisional service intent cannot authorize individual diagnosis")

    answer, context = _deliver(functions, event, generate, draft="我来说明一下。")
    assert answer == normalize_security_text(functions["_DIAGNOSIS_NOT_VERIFIED_RESPONSE"])
    context.llm_generate.assert_not_awaited()


@pytest.mark.parametrize("question", [
    "接码没收到会自动退款吗？",
    "如果我的订单没收到邮件会自动退款吗？",
    "请问如果我没收到邮件，接码会自动退款吗？",
])
def test_static_missing_mail_refund_rule_does_not_force_individual_diagnosis(question):
    functions, _ = _load_welcome_functions()
    event = event_for(private=True, question=question)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))
    approved = "接码窗口内未收到有效邮件按接码规则处理，具体订单结果需要查询本人记录。"
    calls = []

    async def generate(**kwargs):
        stage = _stage(kwargs, json.loads(kwargs["prompt"]))
        calls.append(stage)
        if stage == "writer":
            return _response(approved, used=())
        return _critic(used=())

    answer, _ = _deliver(functions, event, generate, draft=approved)
    assert answer == normalize_security_text(approved)
    assert calls == ["facts", "writer", "delivery"]
