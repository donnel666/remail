"""Focused workflow regressions; model doubles do not evaluate real LLM quality."""

import asyncio
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .persona import CRITIC_SYSTEM_PROMPT, PERSONA_SYSTEM_PROMPT
from .security import normalize_security_text
from .test_security import _fact, _fact_plan, _load_welcome_functions
from .workflow import PLANNER_SYSTEM_PROMPT, PUBLIC_BUSINESS_RULES, parse_fact_plan


def event_for(*, private=True, question="我们买的邮箱能用多长时间", sender="123456789"):
    extras = {}
    return SimpleNamespace(
        message_str=question,
        unified_msg_origin="qq:FriendMessage:123456789"
        if private
        else "qq:GroupMessage:10001",
        get_message_type=lambda: "friend" if private else "group",
        get_platform_name=lambda: "aiocqhttp",
        get_group_id=lambda: "" if private else "10001",
        get_sender_id=lambda: sender,
        get_self_id=lambda: "999999",
        get_messages=lambda: [],
        message_obj=SimpleNamespace(message=[], raw_message={}),
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
        stop_event=AsyncMock(),
    )


def test_static_service_and_clarification_are_valid_without_dynamic_facts():
    for plan in (
        _fact_plan(intents=("service",)),
        _fact_plan(intents=(), answer_mode="clarify"),
    ):
        assert not plan.required
        assert not parse_fact_plan(
            "```json\n" + json.dumps(plan.to_dict()) + "\n```"
        ).failed
    for concept in (
        "质保是",
        "激活窗口",
        "积分余额",
        "兑换",
        "售后",
        "目标业务",
        "订单生命周期",
        "必须绑定",
    ):
        assert concept in PUBLIC_BUSINESS_RULES
    assert PUBLIC_BUSINESS_RULES in PLANNER_SYSTEM_PROMPT
    assert "facts []" in PLANNER_SYSTEM_PROMPT


def test_planner_gets_context_and_repairs_format_without_echoing_raw_output():
    functions, _ = _load_welcome_functions()
    event = event_for(private=False)
    event.set_extra(
        "_remail_dynamic_background",
        {"projectCatalog": {"sourceValid": True, "items": []}},
    )
    plan = _fact_plan(intents=("service",))
    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(
            side_effect=[
                SimpleNamespace(
                    completion_text="BAD MODEL OUTPUT private@example.test"
                ),
                SimpleNamespace(completion_text=json.dumps(plan.to_dict())),
            ]
        ),
    )
    actual = asyncio.run(
        functions["_generate_fact_plan"](
            context, event, event.message_str, "上一轮在问购买邮箱"
        )
    )
    assert actual == plan
    assert context.llm_generate.await_count == 2
    retry = context.llm_generate.await_args.kwargs
    payload = json.loads(retry["prompt"])
    assert payload["validationFeedback"]["error"] == "invalid_json"
    assert "购买邮箱" in payload["untrustedRecentContext"]
    assert "projectCatalog" in payload["dynamicBackground"]
    assert "private@example.test" not in retry["prompt"]
    assert retry["tools"] is None and retry["contexts"] is None


def test_background_sources_are_bounded_scoped_and_reused_per_event():
    functions, _ = _load_welcome_functions()
    project = {
        "id": 2,
        "name": "OwnProject",
        "products": [
            {
                "type": "icloud",
                "status": "enabled",
                "codeEnabled": True,
                "purchaseEnabled": True,
                "codePrice": "2",
                "purchasePrice": "5",
                "publicAvailable": None,
                "mailRules": "DO_NOT_COPY",
                "codeSupplierPrice": "SECRET_COST",
            }
        ],
        "owner": {"email": "owner@example.test"},
        "description": "PRIVATE_DESCRIPTION",
    }
    order = {
        "projectId": 2,
        "projectName": "OwnProject",
        "productType": "icloud",
        "serviceMode": "purchase",
        "status": "active",
        "createdAt": "2026-09-05T00:00:00Z",
        "email": "order@example.test",
        "orderNo": "PRIVATE_ORDER",
        "subject": "PRIVATE_SUBJECT",
    }

    async def request(_method, path, **kwargs):
        if path == "/v1/bot/projects":
            assert kwargs["params"]["limit"] == 100
            return {"items": [project], "total": 101}
        if path == "/v1/bot/orders":
            assert kwargs["params"] == {"offset": 0, "limit": 100}
            return {"available": True, "items": [order], "total": 1, "offset": 0}
        return {"enabled": False, "redemptionCodePurchaseUrl": ""}

    plugin = SimpleNamespace(
        _request=AsyncMock(side_effect=request),
        _public_request=AsyncMock(
            return_value={
                "enabled": True,
                "items": [{"question": "最新问题", "answer": "当前公开解释"}],
                "truncated": False,
            }
        ),
        _public_api_capability_context=AsyncMock(return_value='{"operations":[]}'),
    )
    event = event_for()
    asyncio.run(functions["_prepare_fae_context"](plugin, event))
    asyncio.run(functions["_prepare_fae_context"](plugin, event))
    assert plugin._request.await_count == 3
    assert plugin._public_request.await_count == 3
    plugin._public_request.assert_any_await("/v1/faqs?limit=100", ttl=0)
    plugin._public_request.assert_any_await("/v1/announcements?limit=100", ttl=0)
    background = event.get_extra("_remail_dynamic_background")
    assert background["projectCatalog"]["truncated"] is True
    assert (
        background["projectCatalog"]["items"][0]["products"][0]["publicAvailable"]
        is None
    )
    assert background["ownOrders"]["items"][0]["status"] == "active"
    for secret in (
        "SECRET_COST",
        "DO_NOT_COPY",
        "PRIVATE_DESCRIPTION",
        "owner@example.test",
        "order@example.test",
        "PRIVATE_ORDER",
        "PRIVATE_SUBJECT",
    ):
        assert secret not in json.dumps(background)
    service_packet = functions["_persona_evidence_packet"](
        event, _fact_plan(intents=("service",))
    )
    assert "policy.business" in service_packet
    assert any('"strength":"strong"' in text for text in service_packet.values())
    group = event_for(private=False)
    plugin._request.reset_mock()
    asyncio.run(functions["_prepare_fae_context"](plugin, group))
    assert all(
        call.args[1] != "/v1/bot/orders" for call in plugin._request.await_args_list
    )
    assert group.get_extra("_remail_dynamic_background")["ownOrders"] == {
        "privateOnly": True
    }


@pytest.mark.parametrize(
    "question,answer",
    [
        (
            "我们买的邮箱能用多长时间",
            "购买邮箱是长效服务，质保是售后保障窗口，不是邮箱使用期限；服务正常且未终止时可持续收件，不代表永久可用。",
        ),
        (
            "接码没收到邮件会退款吗",
            "接码窗口内未收到有效邮件时，按接码规则自动退款；这不是对你这笔订单已经退款的确认。",
        ),
    ],
)
def test_static_answers_reach_persona_and_critic_without_faq(question, answer):
    functions, _ = _load_welcome_functions()
    event = event_for(question=question)
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))
    event.set_extra(
        "_remail_personality_style", "表达沉稳，先解释区别，不使用客服套话。"
    )

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            assert "沉稳" in payload["personalityStyle"]
            assert payload["immutableSeals"] == []
            assert any(item["id"] == "policy.business" for item in payload["evidence"])
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "answer": answer,
                        "usedEvidence": ["policy.business"],
                        "seals": [],
                    },
                    ensure_ascii=False,
                ),
            )
        assert kwargs["system_prompt"] == CRITIC_SYSTEM_PROMPT
        return SimpleNamespace(
            role="assistant",
            completion_text=json.dumps(
                {
                    "decision": "approve",
                    "supportedEvidence": ["policy.business"],
                    "violations": [],
                }
            ),
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    response = SimpleNamespace(completion_text=answer, role="assistant")
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert response.completion_text == normalize_security_text(answer)
    assert context.llm_generate.await_count == 2


def test_multiple_public_sources_use_llms_not_immutable_answer_templates():
    functions, _ = _load_welcome_functions()
    event = event_for(question="OwnProject 的质保是什么意思？")
    event.set_extra("_remail_owned", True)
    event.set_extra(
        "_remail_intent_plan_v1",
        _fact_plan(
            intents=("faq", "project"),
            facts=(_fact("rules", "faqs"), _fact("project", "projects")),
        ),
    )
    functions["_record_evidence"](
        event,
        "faqs",
        {
            "sourceValid": True,
            "enabled": True,
            "items": [
                {"question": "质保", "answer": "质保是售后窗口，不是邮箱使用期限。"}
            ],
            "truncated": False,
        },
    )
    functions["_record_evidence"](
        event,
        "projects",
        {"items": [{"id": 2, "name": "OwnProject", "products": []}], "total": 1},
        {"search": ""},
    )
    answer = "质保是售后窗口，不是邮箱使用期限。"

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            assert payload["immutableSeals"] == []
            assert "REMAIL_SEAL" not in payload["agentDraft"]
            return SimpleNamespace(
                completion_text=json.dumps(
                    {
                        "answer": answer,
                        "usedEvidence": ["rules", "project"],
                        "seals": [],
                    }
                )
            )
        return SimpleNamespace(
            completion_text=json.dumps(
                {
                    "decision": "approve",
                    "supportedEvidence": ["rules", "project"],
                    "violations": [],
                }
            )
        )

    context = SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    )
    response = SimpleNamespace(completion_text=answer)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert context.llm_generate.await_count == 2
    assert response.completion_text == normalize_security_text(answer)


def test_unbound_mentions_only_receive_private_guidance_before_any_model(monkeypatch):
    functions, error = _load_welcome_functions()
    monkeypatch.setitem(
        functions, "_mentions_bot", lambda event: event.message_str.startswith("@bot")
    )
    event = event_for(private=False, question="@bot 我们买的邮箱能用多久")
    stopped = []
    event.stop_event = lambda: stopped.append(True)
    denied = error(428, "binding required")
    denied.status = 428
    plugin = SimpleNamespace(
        config={},
        _authorize_event=AsyncMock(side_effect=denied),
        _private_target=lambda current: "qq:FriendMessage:" + current.get_sender_id(),
        _reply=AsyncMock(),
        context=SimpleNamespace(
            send_message=AsyncMock(return_value=True), llm_generate=AsyncMock()
        ),
    )
    asyncio.run(functions["require_bound_service_user"](plugin, event))
    target, chain = plugin.context.send_message.await_args.args
    assert target == "qq:FriendMessage:123456789"
    assert "/绑定" in str(chain)
    assert stopped
    plugin._reply.assert_not_awaited()
    plugin.context.llm_generate.assert_not_awaited()
    plugin._authorize_event.reset_mock()
    ordinary = event_for(private=False, question="普通群聊")
    asyncio.run(functions["require_bound_service_user"](plugin, ordinary))
    plugin._authorize_event.assert_not_awaited()
    for command in ("/绑定", "/绑定状态", "/解绑"):
        asyncio.run(
            functions["require_bound_service_user"](plugin, event_for(question=command))
        )
    plugin._authorize_event.assert_not_awaited()


def test_binding_access_flags_fail_closed_and_personality_is_not_business_context():
    functions, error = _load_welcome_functions()
    for payload in (
        {"authorized": True},
        {"authorized": True, "bound": "true", "accountAvailable": True},
    ):
        with pytest.raises(error):
            asyncio.run(
                functions["_authorize_event"](
                    SimpleNamespace(_request=AsyncMock(return_value=payload)),
                    event_for(),
                )
            )
    event = event_for()
    plugin = SimpleNamespace(
        _request=AsyncMock(
            return_value={"authorized": True, "bound": False, "accountAvailable": False}
        )
    )
    asyncio.run(functions["_authorize_event"](plugin, event, require_binding=False))
    with pytest.raises(error):
        asyncio.run(functions["_authorize_event"](plugin, event))
    plugin._request.assert_awaited_once()
    context = SimpleNamespace(
        persona_manager=SimpleNamespace(
            resolve_selected_persona=AsyncMock(
                return_value=(
                    "custom",
                    {"prompt": "表达沉稳，不使用客服套话。"},
                    None,
                    False,
                )
            )
        ),
        get_config=lambda _umo: {"provider_settings": {}},
    )
    assert "沉稳" in asyncio.run(
        functions["_configured_personality"](
            context,
            event_for(),
            SimpleNamespace(system_prompt="UNRELATED_SYSTEM_SECRET"),
        )
    )
    assert "UNRELATED_SYSTEM_SECRET" not in str(
        context.persona_manager.resolve_selected_persona.await_args
    )


def test_private_followup_context_is_saved_and_sender_isolated():
    functions, _ = _load_welcome_functions()
    event = event_for()
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_canonical_response", "购买是长效服务，质保不是使用期限。")
    plugin = SimpleNamespace(remail_intent_contexts={})
    asyncio.run(
        functions["sync_safe_response_history"](
            plugin,
            event,
            SimpleNamespace(messages=[]),
            SimpleNamespace(role="assistant"),
        )
    )
    same = event_for(question="上面那个倒计时结束呢")
    assert "质保不是使用期限" in functions["_recent_intent_context"](plugin, same)
    assert not functions["_recent_intent_context"](
        plugin, event_for(sender="987654321")
    )
