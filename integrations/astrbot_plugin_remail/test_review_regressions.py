import json
from types import SimpleNamespace

from .security import normalize_security_text
from .test_persona_delivery import _critic, _deliver, _recharge_event, _response, _stage
from .test_security import _load_welcome_functions


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
