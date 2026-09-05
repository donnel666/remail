"""Points remain distinct from payment currencies throughout the read-only flow."""

import ast
import asyncio
import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from .test_background import event_for
from .test_security import _fact, _fact_plan, _load_welcome_functions
from .workflow import (
    FactRequest,
    PLANNER_SYSTEM_PROMPT,
    PUBLIC_BUSINESS_RULES,
    parse_fact_plan,
)


def _runtime():
    functions, error = _load_welcome_functions()
    tree = ast.parse(Path(__file__).with_name("main.py").read_text())
    tool = next(
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name == "remail_recharge_quote"
    )
    tool.decorator_list = []
    exec(
        compile(ast.Module(body=[tool], type_ignores=[]), "main.py", "exec"), functions
    )
    return functions, error


def _quote(currency="CNY"):
    return {
        "points": "1000.00",
        "bonusPoints": "100.00",
        "feePoints": "0.00" if currency == "USDT" else "5.00",
        "creditedPoints": "1100.00",
        "paymentAmount": "10.00" if currency == "USDT" else "1.01",
        "paymentCurrency": currency,
    }


def test_recharge_units_are_static_but_channels_and_amounts_require_current_facts():
    functions, _ = _runtime()
    for term in (
        "积分",
        "CNY",
        "USD",
        "USDT",
        "paymentAmount",
        "paymentCurrency",
        "不是人民币或美元",
        "报价不表示已经支付或到账",
    ):
        assert term in PUBLIC_BUSINESS_RULES
    assert "recharge_quote" in PLANNER_SYSTEM_PROMPT
    assert "do not silently interpret '充10元' as 10 points" in PLANNER_SYSTEM_PROMPT
    assert "remail_recharge_quote" in functions["_ALLOWED_REMAIL_TOOLS"]
    assert "recharge_quote" in functions["STRONG_SOURCES"]
    config = functions["_recharge_config_view"](
        {
            "enabled": True,
            "paymentMethods": ["alipay", "epusdt_usdt_tron"],
            "tiers": [],
            "paymentCurrencies": {
                "alipay": "CNY",
                "epusdt_usdt_tron": "USDT",
                "disabled": "USD",
                "merchantKey": "private-value",
            },
        }
    )
    assert config["paymentCurrencies"] == {"alipay": "CNY", "epusdt_usdt_tron": "USDT"}
    rendered = functions["_render_recharge_evidence"](config)
    assert "CNY（不是积分）" in rendered and "USDT（不是积分）" in rendered
    assert "USD（" not in rendered and "$" not in rendered
    old = functions["_recharge_config_view"](
        {"enabled": True, "paymentMethods": ["alipay"], "tiers": []}
    )
    assert old["paymentCurrencies"] == {}
    assert "未提供支付币种" in functions["_render_recharge_evidence"](old)


def test_read_only_quote_preserves_units_scope_and_default_method():
    functions, _ = _runtime()
    event = event_for(question="充值1000积分用USDT需要付多少钱")
    plugin = SimpleNamespace(
        _authorize_event=AsyncMock(), _request=AsyncMock(return_value=_quote("USDT"))
    )
    view = json.loads(
        asyncio.run(
            functions["remail_recharge_quote"](
                plugin, event, "1000", "epusdt_usdt_tron"
            )
        )
    )
    plugin._authorize_event.assert_awaited_once_with(event)
    plugin._request.assert_awaited_once_with(
        "POST",
        "/v1/bot/recharges/quote",
        event=event,
        body={"points": "1000", "paymentMethod": "epusdt_usdt_tron"},
    )
    fact = _fact(
        "quote",
        "recharge_quote",
        params={"points": "1000", "paymentMethod": "epusdt_usdt_tron"},
    )
    plan = _fact_plan(
        intents=("recharge",), facts=(_fact("config", "recharge_config"), fact)
    )
    fact = plan.facts[1]
    assert not parse_fact_plan(json.dumps(plan.to_dict())).failed
    assert functions["_fact_is_satisfied"](event, fact, plan)
    for wrong in ({"points": "100"}, {"paymentMethod": "alipay"}):
        assert not functions["_fact_is_satisfied"](
            event, FactRequest("wrong", "recharge_quote", True, params=wrong), plan
        )
    rendered = functions["_render_recharge_quote_evidence"](view)
    assert "充值积分：1000.00 积分" in rendered
    assert "预计到账：1100.00 积分" in rendered
    assert "报价支付金额：10.00 USDT" in rendered
    assert "未到账" in rendered and "勿按试算直接转账" in rendered
    packet = functions["_persona_evidence_packet"](event, plan)
    assert '"strength":"strong"' in packet["quote"]
    assert "10.00 USDT" in packet["quote"]
    plugin._request.return_value = _quote()
    default = json.loads(
        asyncio.run(functions["remail_recharge_quote"](plugin, event, "1000"))
    )
    assert plugin._request.await_args.kwargs["body"] == {"points": "1000"}
    assert default["paymentMethod"] == "" and default["paymentCurrency"] == "CNY"
    assert (
        functions["_evidence_data"](event, "recharge_quote", plan, fact)[
            "paymentCurrency"
        ]
        == "USDT"
    )


@pytest.mark.parametrize(
    "points,method",
    [
        (10, "alipay"),
        (True, "alipay"),
        ("0", ""),
        ("10元", ""),
        ("$10", ""),
        ("1.5", ""),
        ("1e3", ""),
        ("NaN", ""),
        ("9" * 19, ""),
        ("1000", "usd"),
        ("1000", {}),
        ("1000", "alipay\nX-System-Key"),
    ],
)
def test_quote_rejects_cash_or_unbounded_inputs_before_transport(points, method):
    functions, _ = _runtime()
    plugin = SimpleNamespace(_authorize_event=AsyncMock(), _request=AsyncMock())
    result = json.loads(
        asyncio.run(
            functions["remail_recharge_quote"](plugin, event_for(), points, method)
        )
    )
    assert result["ok"] is False
    plugin._request.assert_not_awaited()
    plan = _fact_plan(
        intents=("recharge",), facts=(_fact("config", "recharge_config"),)
    ).to_dict()
    plan["facts"].append(
        _fact(
            "quote",
            "recharge_quote",
            params={"points": points, "paymentMethod": method},
        )
    )
    assert parse_fact_plan(json.dumps(plan)).failed


def test_quote_projection_drops_private_fields_and_rejects_invalid_or_mismatched_values():
    functions, _ = _runtime()
    view = functions["_recharge_quote_view"](
        {
            **_quote(),
            "userId": 999,
            "gatewayKey": "private-value",
            "payUrl": "https://private.example",
        },
        "1000",
        "alipay",
    )
    assert "private" not in json.dumps(view) and "userId" not in view
    for invalid in (
        {"points": "100"},
        {"paymentAmount": "0"},
        {"creditedPoints": "NaN"},
        {"bonusPoints": "-1"},
        {"paymentCurrency": "$"},
        {"paymentCurrency": "USDT\npassword=x"},
        {"paymentAmount": 1.01},
    ):
        assert (
            functions["_recharge_quote_view"]({**_quote(), **invalid}, "1000", "alipay")
            == {}
        )


def test_failed_quote_replaces_previous_same_scope_without_reusing_old_payment_amount():
    functions, error = _runtime()
    event = event_for()
    plugin = SimpleNamespace(
        _authorize_event=AsyncMock(), _request=AsyncMock(return_value=_quote())
    )
    fact = _fact(
        "quote", "recharge_quote", params={"points": "1000", "paymentMethod": "alipay"}
    )
    plan = _fact_plan(
        intents=("recharge",), facts=(_fact("config", "recharge_config"), fact)
    )
    fact = plan.facts[1]
    asyncio.run(functions["remail_recharge_quote"](plugin, event, "1000", "alipay"))
    assert functions["_fact_is_satisfied"](event, fact, plan)
    plugin._request.side_effect = error(503, "unavailable")
    asyncio.run(functions["remail_recharge_quote"](plugin, event, "1000", "alipay"))
    assert not functions["_fact_is_satisfied"](event, fact, plan)
    assert "1.01" not in json.dumps(functions["_persona_evidence_packet"](event, plan))


def test_code_store_remains_a_separate_dynamic_recharge_path_when_online_is_disabled():
    functions, _ = _runtime()
    event = event_for(question="在线充值关了，还能去卡网买兑换码吗")
    plan = _fact_plan(
        intents=("recharge",), facts=(_fact("config", "recharge_config"),)
    )
    for url in ("https://current.example/cards", "https://updated.example/points"):
        config = functions["_recharge_config_view"](
            {
                "enabled": False,
                "paymentMethods": [],
                "tiers": [],
                "redemptionCodePurchaseUrl": url,
            }
        )
        functions["_record_evidence"](event, "recharge_config", config, {})
        packet = functions["_persona_evidence_packet"](event, plan)
        assert url in packet["config"]
        assert "兑换成积分" in packet["config"]
    assert "current.example" not in packet["config"]
    assert "在线充值 enabled=false" in PUBLIC_BUSINESS_RULES
    assert (
        "卡网自身售价、币种、折扣不由在线 recharge_quote 证明" in PUBLIC_BUSINESS_RULES
    )
