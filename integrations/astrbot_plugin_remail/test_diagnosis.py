from dataclasses import asdict

import pytest

from .diagnosis import (
    SUCCESS_DIAGNOSIS_CODES,
    apply_diagnosis_seal,
    diagnosis_fact_payload,
    normalize_diagnosis_payload,
    render_diagnosis_fact,
    seal_diagnosis_fact,
)


def _success(code: str) -> dict:
    payload = {
        "message": "安全诊断结论。",
        "diagnosisCode": code,
    }
    if code != "order_not_found":
        payload.update({"projectId": 2, "projectName": "Purchased Project"})
    return payload


def test_normalize_accepts_only_consistent_success_payloads() -> None:
    fixed_messages = {
        "order_not_found": "当前账号下没有找到该邮箱对应的订单。 请确认邮箱后重试。",
        "pickup_not_requested": "验证码邮件已经到达，但尚未完成领取。 请回到对应订单重新获取验证码。",
        "resource_abnormal_refunded": "邮箱资源异常，系统已自动退款。 请在工作台查看退款记录后重新下单。",
        "pickup_grace_period": "验证码正在处理中。 请稍后重新获取。",
        "cause_not_confirmed": "暂未发现明确异常。 请稍后重试；持续无结果时联系人工客服。",
    }
    for code in SUCCESS_DIAGNOSIS_CODES - {"project_mismatch"}:
        payload = _success(code)
        payload["message"] = "另一项目 Genspark；主题 Welcome；正文 private"
        fact = normalize_diagnosis_payload(payload)
        assert fact is not None
        assert fact.diagnosis_code == code
        assert fact.safe_message == fixed_messages[code]
        assert "Genspark" not in render_diagnosis_fact(fact)
        assert "Welcome" not in render_diagnosis_fact(fact)
        assert not hasattr(fact, "other_project_name")
        assert set(asdict(fact)) == {
            "diagnosis_code",
            "safe_message",
            "purchased_project_id",
            "purchased_project_name",
        }

    for payload in (
        {**_success("cause_not_confirmed"), "result": "cause_not_confirmed"},
        {**_success("pickup_not_requested"), "mailReceived": True},
        {**_success("pickup_grace_period"), "projectMismatch": True},
        {**_success("cause_not_confirmed"), "mailReceived": "false"},
        {**_success("unknown")},
        {**_success("cause_not_confirmed"), "sender": "private@example.com"},
    ):
        assert normalize_diagnosis_payload(payload) is None


def test_mismatch_requires_the_complete_backend_proof_tuple() -> None:
    payload = {
        **_success("project_mismatch"),
        "message": "unsafe other project body private@example.com",
        "result": "project_mismatch",
        "mailReceived": True,
        "projectMismatch": True,
    }
    fact = normalize_diagnosis_payload(payload)
    assert fact is not None
    text = render_diagnosis_fact(fact)
    assert "你购买的是 Purchased Project 项目" in text
    assert "实际已经收到邮件" in text
    assert "项目买错了" in text
    assert "unsafe" not in text
    assert "unsafe" not in fact.safe_message
    assert "private@example.com" not in fact.safe_message
    public = diagnosis_fact_payload(fact)
    assert public == {
        "diagnosisCode": "project_mismatch",
        "message": "邮箱实际已经收到邮件，但该邮件不匹配你购买的项目。",
        "projectId": 2,
        "projectName": "Purchased Project",
        "result": "project_mismatch",
        "mailReceived": True,
        "projectMismatch": True,
    }
    assert "unsafe" not in str(public)

    for key, value in (
        ("diagnosisCode", "cause_not_confirmed"),
        ("result", ""),
        ("mailReceived", False),
        ("projectMismatch", False),
    ):
        invalid = dict(payload)
        invalid[key] = value
        assert normalize_diagnosis_payload(invalid) is None
    assert normalize_diagnosis_payload(payload, verified=False) is None


@pytest.mark.parametrize(
    ("flag", "code"),
    (
        ("bindingRequired", "binding_required"),
        ("accountUnavailable", "account_unavailable"),
    ),
)
def test_binding_states_are_separate_from_diagnosis(flag: str, code: str) -> None:
    fact = normalize_diagnosis_payload(
        {"message": "Genspark；邮件主题 Welcome；验证码 768071", flag: True}
    )
    assert fact is not None
    assert fact.diagnosis_code == code
    assert fact.purchased_project_id is None
    expected = {
        "binding_required": (
            "当前账号尚未绑定 ReMail。\n"
            "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
        ),
        "account_unavailable": "当前绑定的 ReMail 账号不可用，请重新绑定或联系客服。",
    }
    assert render_diagnosis_fact(fact) == expected[code]
    assert "Genspark" not in fact.safe_message
    assert "768071" not in fact.safe_message

    assert (
        normalize_diagnosis_payload(
            {"message": "bad", "bindingRequired": True, "accountUnavailable": True}
        )
        is None
    )
    assert (
        normalize_diagnosis_payload(
            {"message": "bad", flag: True, "diagnosisCode": "cause_not_confirmed"}
        )
        is None
    )


@pytest.mark.parametrize(
    "project_name",
    (
        "ID2；另一个项目 Genspark",
        "ID2\n另一个项目 Genspark",
        "ID2 邮件标题 Welcome",
        "ID2 邮 件 标 题 Welcome",
        "ID2 其 他 项 目 Genspark",
        "ID2 other project Genspark",
        "ID2 sender OtherCorp",
        "ID2: Genspark",
        "user@example.com",
        "A" * 121,
    ),
)
def test_project_name_rejects_cross_project_and_mail_field_injection(
    project_name: str,
) -> None:
    payload = _success("cause_not_confirmed")
    payload["projectName"] = project_name
    assert normalize_diagnosis_payload(payload) is None


@pytest.mark.parametrize(
    "project_name",
    (
        "ChatGPT",
        "Purchased Project",
        "OpenAI GPT-4o",
        "Microsoft 365 (Outlook)",
        "微信登录",
        "Claude_3.5+API",
    ),
)
def test_project_name_accepts_normal_chinese_and_english_names(
    project_name: str,
) -> None:
    payload = _success("cause_not_confirmed")
    payload["projectName"] = project_name
    fact = normalize_diagnosis_payload(payload)
    assert fact is not None
    assert fact.purchased_project_name == project_name


def test_seal_is_opaque_single_use_and_fails_closed() -> None:
    fact = normalize_diagnosis_payload(
        {
            **_success("project_mismatch"),
            "result": "project_mismatch",
            "mailReceived": True,
            "projectMismatch": True,
        }
    )
    assert fact is not None
    first = seal_diagnosis_fact(fact)
    second = seal_diagnosis_fact(fact)
    assert first.token != second.token
    assert "Purchased Project" not in first.token
    assert apply_diagnosis_seal(f"先说明：{first.token}", first) == (
        "先说明：" + first.text
    )
    assert apply_diagnosis_seal("模型删除了占位符", first) == first.text
    assert apply_diagnosis_seal(first.token + first.token, first) == first.text
