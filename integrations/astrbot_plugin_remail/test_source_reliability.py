"""Regression checks for source authority and complete evidence hand-off."""

import asyncio
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .persona import (
    CRITIC_SYSTEM_PROMPT,
    PERSONA_SYSTEM_PROMPT,
    build_persona_payload,
    has_unsupported_concrete_facts,
)
from .sources import SOURCE_RELIABILITY_RULES, evidence_block, source_metadata
from .test_background import event_for
from .test_security import _fact, _fact_plan, _load_welcome_functions
from .workflow import PLANNER_SYSTEM_PROMPT, PUBLIC_BUSINESS_RULES


def _project(warranty=60):
    return {
        "items": [
            {
                "id": 2,
                "name": "Demo",
                "products": [
                    {
                        "type": "icloud",
                        "status": "enabled",
                        "codeEnabled": False,
                        "purchaseEnabled": True,
                        "purchasePrice": "20",
                        "activationWindowMinutes": 60,
                        "warrantyMinutes": warranty,
                    }
                ],
            }
        ],
        "total": 1,
    }


def _context(answer, *, approve=True):
    calls = []

    async def generate(**kwargs):
        payload = json.loads(kwargs["prompt"])
        calls.append((kwargs["system_prompt"], payload))
        if kwargs["system_prompt"] == PERSONA_SYSTEM_PROMPT:
            return SimpleNamespace(
                role="assistant",
                completion_text=json.dumps(
                    {
                        "answer": answer,
                        "usedEvidence": payload["requiredEvidence"],
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
                    "decision": "approve" if approve else "reject",
                    "supportedEvidence": payload["requiredEvidence"],
                    "violations": [] if approve else ["provenance_error"],
                }
            ),
        )

    return SimpleNamespace(
        get_current_chat_provider_id=AsyncMock(return_value="provider"),
        llm_generate=AsyncMock(side_effect=generate),
    ), calls


@pytest.mark.parametrize("approve", [True, False])
def test_strong_prefetch_overrides_old_faq_in_normal_and_failure_paths(approve):
    functions, _ = _load_welcome_functions()
    event = event_for(question="Demo 的常见问题里那个质保怎么理解？")
    plan = _fact_plan(
        intents=("faq",),
        facts=(_fact("faq", "faqs"),),
        entities={"projectQuery": "Demo"},
    )
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", plan)
    functions["_record_evidence"](
        event, "projects", _project(), {"background": True, "search": "", "offset": 0}
    )
    functions["_record_evidence"](
        event,
        "faqs",
        {
            "sourceValid": True,
            "enabled": True,
            "truncated": False,
            "items": [{"question": "Demo 质保", "answer": "旧说明写着 1440 分钟。"}],
        },
        {"background": True},
    )
    answer = "Demo 当前的购买质保为 60 分钟，质保不是邮箱使用期限。"
    context, calls = _context(answer, approve=approve)
    response = SimpleNamespace(role="assistant", completion_text=answer)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert len(calls) == 2
    for prompt, payload in calls:
        assert SOURCE_RELIABILITY_RULES in prompt
        evidence = payload["evidence"]
        assert any(
            '"strength":"strong"' in item["summary"] and "60 分钟" in item["summary"]
            for item in evidence
        )
        assert any(
            '"strength":"weak"' in item["summary"] and "1440 分钟" in item["summary"]
            for item in evidence
        )
    assert "60 分钟" in response.completion_text
    assert "1440" not in response.completion_text


@pytest.mark.parametrize(
    "answer",
    [
        "购买邮箱可以接码，质保不是邮箱使用期限。",
        "购买邮箱能用到服务终止，质保不是邮箱使用期限。",
    ],
)
def test_static_capability_language_is_not_a_dynamic_failure(answer):
    functions, _ = _load_welcome_functions()
    event = event_for(question="购买邮箱能接码吗？")
    event.set_extra("_remail_owned", True)
    event.set_extra("_remail_intent_plan_v1", _fact_plan(intents=("service",)))
    context, calls = _context(answer)
    response = SimpleNamespace(role="assistant", completion_text=answer)
    asyncio.run(
        functions["enforce_redemption_channel_priority"](
            SimpleNamespace(context=context), event, response
        )
    )
    assert len(calls) == 2
    assert "质保不是邮箱使用期限" in response.completion_text
    assert "请稍后重试" not in response.completion_text


def test_faq_25_and_order_supplement_remain_available_to_the_critic():
    functions, _ = _load_welcome_functions()
    event = event_for(question="人工审核中要怎么办")
    faq_plan = _fact_plan(intents=("faq",), facts=(_fact("faq", "faqs"),))
    faqs = [{"question": f"一般问题{i}", "answer": "一般说明"} for i in range(24)] + [
        {"question": "人工审核中", "answer": "请保留操作时间并查询审核进展。"}
    ]
    functions["_record_evidence"](
        event,
        "faqs",
        functions["_faq_view"]({"enabled": True, "items": faqs, "truncated": False}),
        {},
    )
    assert (
        "请保留操作时间"
        in functions["_persona_evidence_packet"](event, faq_plan)["faq"]
    )
    order_plan = _fact_plan(
        intents=("orders",), privacy="private", facts=(_fact("orders", "orders"),)
    )
    for offset, name in ((0, "ProjectFirst"), (100, "ProjectLater")):
        data = functions["_orders_view"](
            {
                "available": True,
                "total": 101,
                "offset": offset,
                "truncated": offset == 0,
                "items": [
                    {
                        "projectId": offset + 1,
                        "projectName": name,
                        "serviceMode": "purchase",
                        "productType": "icloud",
                        "status": "active",
                    }
                ],
            }
        )
        functions["_record_evidence"](event, "orders", data, {"offset": offset})
    packet = functions["_persona_evidence_packet"](event, order_plan)
    assert "ProjectFirst" in json.dumps(packet)
    assert "ProjectLater" in json.dumps(packet)


def test_notice_dates_are_not_replaced_by_fetch_time_or_lost():
    functions, _ = _load_welcome_functions()
    texts = []
    for date in ("2024-01-01T00:00:00Z", "2026-09-05T00:00:00Z"):
        view = functions["_announcement_view"](
            {"notice": ""},
            {
                "announcements": [
                    {
                        "title": "计划",
                        "content": "下周开放。",
                        "startTime": date,
                        "endTime": "",
                    }
                ],
                "truncated": False,
            },
        )
        assert len(view["announcements"]) == 1
        assert view["announcements"][0]["time"]["publishedAt"] is None
        text = functions["_render_announcement_evidence"](view)
        assert date[:10] in text
        texts.append(text)
    assert texts[0] != texts[1]


def test_four_large_sources_and_exact_unit_conversion_remain_valid():
    evidence = {
        "policy.business": evidence_block("policy.business", PUBLIC_BUSINESS_RULES),
        **{f"fact{i}": "公开事实。" * 799 for i in range(4)},
    }
    assert (
        len(
            build_persona_payload(
                question="综合问题",
                agent_draft="答复",
                authoritative_answer="答复",
                evidence=evidence,
            ).evidence
        )
        == 5
    )
    assert not has_unsupported_concrete_facts(
        "质保 24 小时，也就是 1 天。", ["质保 1440 分钟。"]
    )
    assert not has_unsupported_concrete_facts(
        "价格为 20.00 积分；需要 20 积分。", ["价格 20 积分。"]
    )
    assert has_unsupported_concrete_facts("价格 20 元。", ["价格 20 积分。"])
    assert has_unsupported_concrete_facts("质保 25 小时。", ["质保 1440 分钟。"])


def test_default_background_loads_site_and_group_reference_with_explicit_strengths():
    functions, _ = _load_welcome_functions()
    group_loader = AsyncMock(
        return_value={
            "weak": True,
            "status": "ready",
            "items": [
                {
                    "kind": "group_notice",
                    "text": "旧群说明",
                    "publishedAt": None,
                    "timeBasis": "unknown",
                },
                {
                    "kind": "group_essence",
                    "text": "先核对服务模式",
                    "publishedAt": "2024-01-01T00:00:00Z",
                    "timeBasis": "featured",
                },
            ],
            "truncated": False,
        }
    )
    functions["load_group_context"] = group_loader
    event = event_for(private=False)
    event.set_extra("_remail_binding_state", "bound")

    async def public(path, **_kwargs):
        if path == "/v1/notice":
            return {"notice": "网站通知"}
        if path.startswith("/v1/announcements"):
            return {
                "announcements": [
                    {
                        "title": "旧公告",
                        "content": "旧说明",
                        "startTime": "2024-01-01T00:00:00Z",
                    }
                ],
                "truncated": False,
            }
        return {"enabled": True, "items": [], "truncated": False}

    plugin = SimpleNamespace(
        config={},
        _request=AsyncMock(return_value={"items": [], "total": 0}),
        _public_request=AsyncMock(side_effect=public),
        _public_api_capability_context=AsyncMock(return_value=""),
    )
    asyncio.run(functions["_prepare_fae_context"](plugin, event))
    group_loader.assert_awaited_once_with(event, authorized=True, max_age_days=0)
    background = event.get_extra("_remail_dynamic_background")
    assert background["announcements"]["announcements"][0]["title"] == "旧公告"
    assert len(background["groupContext"]["items"]) == 2
    assert background["sourceReliability"]["projectCatalog"]["strength"] == "strong"
    for key in ("faqs", "announcements", "groupContext"):
        assert background["sourceReliability"][key]["strength"] == "weak"
    assert SOURCE_RELIABILITY_RULES in PLANNER_SYSTEM_PROMPT
    assert source_metadata("orders")["strength"] == "strong"
    other = event_for(private=False)
    other.set_extra("_remail_binding_state", "bound")
    plugin.config = {
        "group_context_enabled": False,
        "site_announcements_context_enabled": False,
    }
    group_loader.reset_mock()
    asyncio.run(functions["_prepare_fae_context"](plugin, other))
    group_loader.assert_not_awaited()
    assert (
        other.get_extra("_remail_dynamic_background")["announcements"]["status"]
        == "disabled"
    )


@pytest.mark.parametrize("claim", ["projects", "project_prices"])
@pytest.mark.parametrize("latest_valid", [True, False])
def test_same_scope_latest_empty_or_invalid_snapshot_replaces_previous(
    claim, latest_valid
):
    functions, _ = _load_welcome_functions()
    event = event_for(question="Demo 当前配置和价格是什么？")
    intent = "project" if claim == "projects" else "price"
    plan = _fact_plan(
        intents=(intent,),
        facts=(_fact("current", claim, params={"projectQuery": "Demo"}),),
    )
    event.set_extra("_remail_intent_plan_v1", plan)
    params = {"projectQuery": "Demo", "offset": 0, "productTypes": []}
    previous = (
        _project()
        if claim == "projects"
        else functions["_project_price_view"](_project(), ())
    )
    empty = {"items": [], "total": 0, "truncated": False}
    if claim == "project_prices":
        empty = functions["_project_price_view"](empty, ())
    functions["_record_evidence"](event, claim, previous, params)
    functions["_record_evidence"](event, claim, empty if latest_valid else {}, params)
    entries = functions["_evidence_entries"](event, claim)
    assert len(entries) == 1 and entries[0]["valid"] is latest_valid
    assert functions["_fact_is_satisfied"](event, plan.facts[0], plan) is latest_valid
    data = functions["_evidence_data"](event, claim, plan, plan.facts[0])
    assert data == (empty if latest_valid else None)
    packet = json.dumps(
        functions["_persona_evidence_packet"](event, plan), ensure_ascii=False
    )
    fallback = functions["_grounded_dynamic_answer"](event, event.message_str)
    assert "质保 60 分钟" not in packet + fallback
    assert "购买邮箱 20 积分" not in packet + fallback


@pytest.mark.parametrize("claim", ["projects", "project_prices"])
@pytest.mark.parametrize("has_other_project", [True, False])
def test_complete_broad_snapshot_supersedes_old_targeted_rows(claim, has_other_project):
    functions, _ = _load_welcome_functions()
    event = event_for(question="Demo 现在还提供吗？")
    intent = "project" if claim == "projects" else "price"
    plan = _fact_plan(
        intents=(intent,),
        facts=(_fact("current", claim, params={"projectQuery": "Demo"}),),
    )
    event.set_extra("_remail_intent_plan_v1", plan)
    old = _project()
    current = {"items": [], "total": 0, "truncated": False}
    if has_other_project:
        current = _project(warranty=90)
        current["items"][0].update(id=3, name="CurrentOnly")
        current["truncated"] = False
    if claim == "project_prices":
        old = functions["_project_price_view"](old, ())
        current = functions["_project_price_view"](current, ())
    functions["_record_evidence"](
        event, claim, old, {"projectQuery": "Demo", "offset": 0, "productTypes": []}
    )
    functions["_record_evidence"](
        event, claim, current, {"search": "", "offset": 0, "productTypes": []}
    )
    assert len(functions["_evidence_entries"](event, claim)) == 1
    assert functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    assert functions["_evidence_data"](event, claim, plan, plan.facts[0]) == current
    packet = json.dumps(
        functions["_persona_evidence_packet"](event, plan), ensure_ascii=False
    )
    fallback = functions["_grounded_dynamic_answer"](event, event.message_str)
    assert "- #2 Demo" not in packet + fallback
    assert "Demo / iCloud" not in packet + fallback


@pytest.mark.parametrize("filtered", [True, False])
def test_partial_or_type_filtered_catalog_cannot_remove_other_project_evidence(
    filtered,
):
    functions, _ = _load_welcome_functions()
    event = event_for()
    old = functions["_project_price_view"](_project(), ())
    current = _project()
    current["items"][0].update(id=3, name="OtherProject")
    current["items"][0]["products"][0]["type"] = "gmail"
    current.update(total=1 if filtered else 101, truncated=not filtered)
    requested = ("gmail",) if filtered else ()
    functions["_record_evidence"](
        event,
        "project_prices",
        old,
        {"projectQuery": "Demo", "offset": 0, "productTypes": []},
    )
    functions["_record_evidence"](
        event,
        "project_prices",
        functions["_project_price_view"](current, requested),
        {"search": "", "offset": 0, "productTypes": list(requested)},
    )
    assert len(functions["_evidence_entries"](event, "project_prices")) == 2


@pytest.mark.parametrize("claim", ["projects", "project_prices"])
def test_explicit_broad_supplement_preserves_its_own_project_and_types(claim):
    functions, _ = _load_welcome_functions()
    event = event_for(question="查看其他项目和其他类型")
    plan = _fact_plan(
        intents=("service",),
        entities={"projectId": 2, "projectQuery": "Demo", "productTypes": ["icloud"]},
    )
    current = _project(warranty=90)
    current["items"][0].update(id=120, name="Other120")
    current["items"][0]["products"][0]["type"] = "gmail"
    current.update(offset=100, total=101, truncated=False)
    if claim == "project_prices":
        current = functions["_project_price_view"](current, ())
    functions["_record_evidence"](
        event, claim, current, {"search": "", "offset": 100, "productTypes": []}
    )
    packet = json.dumps(
        functions["_persona_evidence_packet"](event, plan), ensure_ascii=False
    )
    assert "Other120" in packet and "Gmail" in packet
    assert "iCloud：" not in packet and "iCloud：状态" not in packet


@pytest.mark.parametrize("failed_source", ["notice", "announcements"])
@pytest.mark.parametrize("has_content", [True, False])
def test_site_reference_partial_failure_keeps_surviving_data_and_scope(
    failed_source, has_content
):
    functions, _ = _load_welcome_functions()
    event = event_for(private=False, question="网站通知和公告有什么？")
    plan = _fact_plan(
        intents=("announcement",), facts=(_fact("notice", "announcements"),)
    )
    event.set_extra("_remail_intent_plan_v1", plan)

    async def public(path, **_kwargs):
        source = (
            "notice"
            if path == "/v1/notice"
            else "announcements"
            if path.startswith("/v1/announcements")
            else "faqs"
        )
        if source == failed_source:
            raise RuntimeError("independent source unavailable")
        if source == "notice":
            return {"notice": "保留下来的网站通知" if has_content else ""}
        if source == "announcements":
            return {
                "announcements": [
                    {
                        "title": "保留下来的公告",
                        "content": "公开说明",
                        "startTime": "2026-09-05T00:00:00Z",
                    }
                ]
                if has_content
                else [],
                "truncated": False,
            }
        return {"enabled": True, "items": [], "truncated": False}

    plugin = SimpleNamespace(
        config={"group_context_enabled": False},
        _request=AsyncMock(return_value={"items": [], "total": 0}),
        _public_request=AsyncMock(side_effect=public),
        _public_api_capability_context=AsyncMock(return_value=""),
    )
    asyncio.run(functions["_prepare_fae_context"](plugin, event))
    background = event.get_extra("_remail_dynamic_background")
    view = background["announcements"]
    assert (
        view["status"] == "partial" and view["sources"][failed_source] == "unavailable"
    )
    assert background["sourceReliability"]["announcements"]["availability"] == "partial"
    packet = functions["_persona_evidence_packet"](event, plan)["notice"]
    if has_content:
        assert "保留下来" in packet
        assert f'"{failed_source}": "unavailable"' in packet
    else:
        assert "部分未能取得" in packet
        assert "没有查询到仍在发布" not in packet
