"""Exercise query scope and ReAct with real projections and an in-memory transport."""

import ast
import asyncio
import json
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace

from .test_security import _fact, _fact_plan, _load_welcome_functions
from .workflow import PLANNER_SYSTEM_PROMPT, parse_fact_plan
from .sources import SOURCE_RELIABILITY_RULES


def _tools():
    functions, _ = _load_welcome_functions()
    tree = ast.parse(Path(__file__).with_name("main.py").read_text())
    main = next(
        node
        for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "Main"
    )
    methods = [
        node
        for node in main.body
        if isinstance(node, ast.AsyncFunctionDef)
        and node.name
        in {"remail_project_prices", "remail_projects", "remail_project_inventory"}
    ]
    for method in methods:
        method.decorator_list = []
    exec(
        compile(ast.Module(body=methods, type_ignores=[]), "main.py", "exec"), functions
    )
    return functions


def _event(plan):
    extras = {"_remail_intent_plan_v1": plan}
    return SimpleNamespace(
        get_extra=lambda key, default=None: extras.get(key, default),
        set_extra=lambda key, value: extras.__setitem__(key, value),
    )


def _project(index):
    return {
        "id": index,
        "name": f"Project{index}",
        "targetPlatform": f"Platform{index}",
        "products": [
            {
                "type": "icloud",
                "status": "enabled",
                "codeEnabled": True,
                "purchaseEnabled": True,
                "codePrice": "2",
                "purchasePrice": "5",
            }
        ],
    }


def _transport():
    calls = []
    projects = [_project(index) for index in range(1, 121)]

    async def request(method, path, **kwargs):
        calls.append((method, path, kwargs))
        if path == "/v1/bot/projects":
            params = kwargs["params"]
            selected = [
                item
                for item in projects
                if params.get("search", "").casefold() in item["name"].casefold()
            ]
            offset, limit = params["offset"], params["limit"]
            return {
                "items": selected[offset : offset + limit],
                "offset": offset,
                "limit": limit,
                "total": len(selected),
            }
        return {
            "projectId": int(path.split("/")[-2]),
            "observedAt": datetime.now(timezone.utc).isoformat(),
            "totalAvailable": 3,
            "products": [],
        }

    return SimpleNamespace(_request=request), calls


def test_target_price_beyond_first_page_can_be_completed_by_project_search():
    functions = _tools()
    plan = _fact_plan(
        intents=("price",),
        facts=(
            _fact(
                "price",
                "project_prices",
                params={"projectQuery": "Project120", "productTypes": ["icloud"]},
            ),
        ),
    )
    event = _event(plan)
    plugin, calls = _transport()

    asyncio.run(functions["remail_project_prices"](plugin, event))
    assert not functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    asyncio.run(functions["remail_projects"](plugin, event, search="Project120"))
    assert functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    prices = functions["_evidence_data"](event, "project_prices", plan, plan.facts[0])[
        "prices"
    ]
    assert [(item["projectId"], item["purchasePricePoints"]) for item in prices] == [
        (120, "5")
    ]
    assert calls[-1][2]["params"] == {
        "scope": "visible",
        "offset": 0,
        "limit": 100,
        "search": "Project120",
    }


def test_price_search_and_page_parameters_match_the_fact_scope():
    functions = _tools()
    plan = _fact_plan(
        intents=("price",),
        facts=(_fact("price", "project_prices", params={"offset": 100}),),
    )
    assert not parse_fact_plan(json.dumps(plan.to_dict())).failed
    assert SOURCE_RELIABILITY_RULES in PLANNER_SYSTEM_PROMPT
    event = _event(plan)
    plugin, calls = _transport()
    asyncio.run(functions["remail_project_prices"](plugin, event))
    assert not functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    page = json.loads(
        asyncio.run(functions["remail_project_prices"](plugin, event, offset=100))
    )
    assert functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    assert (page["offset"], page["nextOffset"], page["truncated"]) == (100, 120, False)
    target = json.loads(
        asyncio.run(
            functions["remail_project_prices"](plugin, event, search="  Project120  ")
        )
    )
    assert [item["projectId"] for item in target["prices"]] == [120]
    before = len(calls)
    for invalid in (-1, True, 1.5, 10001):
        result = json.loads(
            asyncio.run(
                functions["remail_project_prices"](plugin, event, offset=invalid)
            )
        )
        assert result["ok"] is False
    assert len(calls) == before


def test_price_background_can_prove_present_targets_and_search_can_prove_no_match():
    functions = _tools()
    for query, expected in (("Project120", True), ("Project119", False)):
        plan = _fact_plan(
            intents=("price",),
            facts=(_fact("price", "project_prices", params={"projectQuery": query}),),
        )
        event = _event(plan)
        functions["_record_evidence"](
            event,
            "project_prices",
            functions["_project_price_view"](
                {"items": [_project(120)], "total": 120}, ()
            ),
            {"productTypes": [], "offset": 0, "background": True},
        )
        assert functions["_fact_is_satisfied"](event, plan.facts[0], plan) is expected
    plan = _fact_plan(
        intents=("price",),
        facts=(
            _fact("price", "project_prices", params={"projectQuery": "Project404"}),
        ),
    )
    event = _event(plan)
    plugin, _ = _transport()
    result = json.loads(
        asyncio.run(
            functions["remail_project_prices"](plugin, event, search="Project404")
        )
    )
    assert result["matched"] is False and result["truncated"] is False
    assert functions["_fact_is_satisfied"](event, plan.facts[0], plan)


def test_inventory_react_uses_visible_results_without_frozen_initial_entities():
    functions = _tools()
    for plan in (
        _fact_plan(intents=(), answer_mode="clarify"),
        _fact_plan(
            intents=("service",), entities={"projectQuery": "Project1", "projectId": 1}
        ),
    ):
        event = _event(plan)
        plugin, calls = _transport()
        asyncio.run(functions["remail_projects"](plugin, event, search="Project120"))
        inventory = json.loads(
            asyncio.run(functions["remail_project_inventory"](plugin, event, 120))
        )
        assert inventory["projectId"] == 120
        before = len(calls)
        denied = json.loads(
            asyncio.run(functions["remail_project_inventory"](plugin, event, 121))
        )
        assert denied["ok"] is False
        assert len(calls) == before


def test_inventory_evidence_must_still_match_its_requested_project_dependency():
    functions = _tools()
    plan = _fact_plan(
        intents=("inventory",),
        facts=(
            _fact("project", "projects", params={"projectQuery": "Project120"}),
            _fact("stock", "project_inventory", depends_on=("project",)),
        ),
    )
    event = _event(plan)
    plugin, _ = _transport()
    asyncio.run(functions["remail_projects"](plugin, event, search="Project119"))
    asyncio.run(functions["remail_project_inventory"](plugin, event, 119))
    assert not functions["_fact_is_satisfied"](event, plan.facts[1], plan)
    asyncio.run(functions["remail_projects"](plugin, event, search="Project120"))
    asyncio.run(functions["remail_project_inventory"](plugin, event, 120))
    assert functions["_fact_is_satisfied"](event, plan.facts[1], plan)
    asyncio.run(functions["remail_project_inventory"](plugin, event, 119))
    selected = functions["_evidence_data"](
        event, "project_inventory", plan, plan.facts[1]
    )
    assert selected["projectId"] == 120


def test_each_project_fact_uses_its_own_query_in_a_combined_plan():
    functions = _tools()
    plan = _fact_plan(
        intents=("project",),
        entities={"projectQuery": "Project119", "projectId": 119},
        facts=(
            _fact("first", "projects", params={"projectQuery": "Project119"}),
            _fact("second", "projects", params={"projectQuery": "Project120"}),
        ),
    )
    event = _event(plan)
    plugin, _ = _transport()
    asyncio.run(functions["remail_projects"](plugin, event, search="Project120"))
    assert not functions["_fact_is_satisfied"](event, plan.facts[0], plan)
    assert functions["_fact_is_satisfied"](event, plan.facts[1], plan)
