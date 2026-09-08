import json

from .persona import build_critic_payload, build_persona_payload, sanitize_model_text
from .security import contains_credentials, normalize_security_text, redact_credentials
from .sessions import _safe_history
from .test_background import event_for
from .test_persona_delivery import _critic, _deliver, _response, _stage
from .test_security import _fact_plan, _load_welcome_functions


def test_public_api_examples_keep_placeholders_but_redact_actual_credentials():
    # The 0.7.6 API repair was valid JSON but these examples triggered its hard gate.
    public_examples = (
        "Authorization: Bearer <YOUR_API_KEY>",
        "curl -H 'Authorization: Bearer <YOUR_API_KEY>' \\\n  https://example.com/v1/open/orders",
        'curl -H "Authorization: Bearer <YOUR_API_KEY>" \\\n  https://example.com/v1/open/orders',
        "--data-urlencode 'token=<SERVICE_TOKEN>'",
        "token=${SERVICE_TOKEN}",
        "Authorization: Bearer <你的API Key>",
        "Authorization: Bearer rk-...",
        "curl -H 'Authorization: Bearer rk-...' \\\n  https://example.com",
        "`Authorization: Bearer <YOUR_API_KEY>`",
        "**Authorization: Bearer <YOUR_API_KEY>**",
        "Bearer `<SERVICE_TOKEN>`",
        "Bearer '<SERVICE_TOKEN>'",
        "Token: `<SERVICE_TOKEN>`",
        "API Key: `<YOUR_API_KEY>`",
        "API Key 只在你自己的客户端中使用，不要发到聊天中。",
        "**取件接口使用邮箱和服务凭证，不使用账号 API Key：**",
        "**API Key:**\n请在本地配置。",
        "`API Key:`",
    )
    for text in public_examples:
        assert redact_credentials(text) == normalize_security_text(text), text
        assert not contains_credentials(text), text

    for text, secret in (
        ("Authorization: Bearer real-secret-value", "real-secret-value"),
        (
            "curl -H 'Authorization: Bearer real-secret-value' \\\n  https://example.com",
            "real-secret-value",
        ),
        ("token=<ATTACKER_SECRET>", "ATTACKER_SECRET"),
        ("token=<SERVICE_TOKEN=real-secret>", "real-secret"),
        ("token=<SERVICE_TOKEN>real-secret", "real-secret"),
        ("Bearer <SERVICE_TOKEN>real-secret", "real-secret"),
        ("API Key: <你的真实密钥>", "你的真实密钥"),
        ("Token: `real-secret-value`", "real-secret-value"),
        ("Bearer `real-secret-value`", "real-secret-value"),
        ("Bearer 'real-secret-value'", "real-secret-value"),
        ("Bearer 'real-secret-value", "real-secret-value"),
        ("**Authorization: Bearer real-secret-value**", "real-secret-value"),
        ("token=rk-actual-secret-value...", "rk-actual-secret-value"),
        ("rk-12345678901234567890", "rk-12345678901234567890"),
        ("password=**", "password=**"),
        ("**password:** actual-secret", "actual-secret"),
        ("API Key 只在你自己的客户端中使用 real-secret-value", "real-secret-value"),
    ):
        assert contains_credentials(text), text
        assert secret not in redact_credentials(text), text


def test_symbolic_api_calls_survive_input_history_and_full_delivery():
    functions, _ = _load_welcome_functions()
    for code in (
        "message = response.json()",
        "const message = await response.json();",
        "body = json.dumps(payload)",
        "body = json.dumps(payload, config.options)",
        "`message = response.json()`",
        '```python\nmessage = response.json()\nprint(message["orderNo"])\n```',
    ):
        assert sanitize_model_text(code) == code
        assert functions["_safe_llm_context_text"](code) == code
        input_event = event_for(private=True, question=code)
        functions["_prepare_owned_event_input"](input_event)
        assert input_event.message_str == code

        question = "如何解析公开 API 的 JSON 响应？"
        history, truncated = _safe_history(json.dumps([
            {"role": "user", "content": question},
            {"role": "assistant", "content": code},
        ], ensure_ascii=False))
        assert not truncated
        history_answer = json.loads(history.split("\n", 1)[1])["items"][0]["answer"]
        assert history_answer == " ".join(code.split())

        event = event_for(private=True, question=question)
        event.set_extra("_remail_owned", True)
        event.set_extra(
            "_remail_intent_plan_v1",
            _fact_plan(
                intents=("social",), answer_mode="client_guidance", privacy="private"
            ),
        )
        stages = []

        async def generate(**kwargs):
            payload = json.loads(kwargs["prompt"])
            stage = _stage(kwargs, payload)
            stages.append(stage)
            if stage == "writer":
                assert payload["authoritativeAnswer"] == code
                return _response(code, used=())
            assert stage in {"facts", "delivery"}
            assert payload["candidateAnswer"] == code
            if stage == "delivery":
                assert payload["approvedAnswer"] == code
            return _critic(used=())

        answer, context = _deliver(functions, event, generate, draft=code)
        assert answer == code
        assert stages == ["facts", "writer", "delivery"]
        assert context.llm_generate.await_count == 3


def test_mail_literals_and_credentials_cannot_hide_inside_api_code():
    functions, _ = _load_welcome_functions()
    public_code = "message = response.json()"
    for private, secrets in (
        ("message='private text'", ("private", "text")),
        ('message="private text"', ("private", "text")),
        ('message="private text', ("private", "text")),
        (r'message="private \"quoted\" text"', ("private", "quoted", "text")),
        ("message=PRIVATE_MESSAGE", ("PRIVATE_MESSAGE",)),
        ("message: PRIVATE_MESSAGE", ("PRIVATE_MESSAGE",)),
        ("body:真实正文", ("真实正文",)),
        ("message=response.json('PRIVATE_MESSAGE')", ("PRIVATE_MESSAGE",)),
        ("const message = await response.json('PRIVATE_MESSAGE');", ("PRIVATE_MESSAGE",)),
        ("message = response.json(payload, 'PRIVATE_MESSAGE')", ("PRIVATE_MESSAGE",)),
        ("message = response.json(654321)", ("654321",)),
        ("sender=user@example.test", ("user@example.test",)),
        ("Subject: PRIVATE_SUBJECT\nFrom: user@example.test\nBody: PRIVATE_BODY",
         ("PRIVATE_SUBJECT", "user@example.test", "PRIVATE_BODY")),
        ('password="sentinel-secret"', ("sentinel-secret",)),
        ("Authorization: Bearer sentinel-token", ("sentinel-token",)),
        ("验证码：654321", ("654321",)),
    ):
        raw = f"{public_code}\n{private}\nnext_step = payload"
        writer = build_persona_payload(
            question=raw, agent_draft=raw, authoritative_answer=raw, evidence={}
        )
        critic = build_critic_payload(
            question=raw, candidate_answer=raw, evidence={}, review_mode="facts"
        )
        for safe in (
            sanitize_model_text(raw),
            functions["_safe_llm_context_text"](raw),
            writer.question,
            writer.agent_draft,
            writer.authoritative_answer,
            critic.question,
            critic.candidate_answer,
        ):
            assert public_code in safe and "next_step = payload" in safe
            assert all(secret not in safe for secret in secrets), (private, safe)
        history, truncated = _safe_history(json.dumps([
            {"role": "user", "content": "如何解析公开 API 响应？"},
            {"role": "assistant", "content": raw},
        ], ensure_ascii=False))
        assert truncated
        assert all(secret not in history for secret in secrets)
