from pathlib import Path

from .persona import CRITIC_SYSTEM_PROMPT, PERSONA_SYSTEM_PROMPT
from .knowledge import INTERNAL_MODULE_KNOWLEDGE, PUBLIC_DISCLOSURE_RULES
from .workflow import INTENT_SYSTEM_PROMPT, PLANNER_SYSTEM_PROMPT, PUBLIC_BUSINESS_RULES


ROOT = Path(__file__).parents[2]


def test_business_background_is_embedded_and_personality_is_style_only() -> None:
    personality = (ROOT / "docs/remail-fae-system-prompt.txt").read_text(
        encoding="utf-8"
    )
    user = (ROOT / "docs/remail-fae-user-prompt.txt").read_text(encoding="utf-8")
    runtime = (ROOT / "integrations/astrbot_plugin_remail/main.py").read_text(
        encoding="utf-8"
    )

    assert personality.startswith("<remail_personality_v1>")
    assert user.strip() == "{{prompt}}"
    assert "高冷表示" in personality
    assert "这份人格只定义表达风格" in personality
    assert "FactPlan" not in personality
    assert PUBLIC_BUSINESS_RULES in PLANNER_SYSTEM_PROMPT
    assert PUBLIC_BUSINESS_RULES in INTENT_SYSTEM_PROMPT
    assert PUBLIC_DISCLOSURE_RULES in INTENT_SYSTEM_PROMPT
    assert PUBLIC_DISCLOSURE_RULES in PLANNER_SYSTEM_PROMPT
    assert INTERNAL_MODULE_KNOWLEDGE in PLANNER_SYSTEM_PROMPT
    assert INTERNAL_MODULE_KNOWLEDGE not in INTENT_SYSTEM_PROMPT
    assert INTERNAL_MODULE_KNOWLEDGE not in PUBLIC_BUSINESS_RULES
    assert INTERNAL_MODULE_KNOWLEDGE not in PERSONA_SYSTEM_PROMPT
    assert INTERNAL_MODULE_KNOWLEDGE not in CRITIC_SYSTEM_PROMPT
    for term in (
        "服务模式",
        "质保",
        "激活窗口",
        "积分",
        "售后",
        "订单生命周期",
        "必须绑定",
        "当前实际项目",
    ):
        assert term in PUBLIC_BUSINESS_RULES
    for term in (
        "_prepare_fae_context",
        "_configured_personality",
        "require_bound_service_user",
        "request.system_prompt =",
        "PUBLIC_BUSINESS_RULES",
    ):
        assert term in runtime
    assert "personalityStyle" in PERSONA_SYSTEM_PROMPT
    assert "policy.business" in CRITIC_SYSTEM_PROMPT
    assert "code_diagnosis" in runtime and "seal_diagnosis_fact" in runtime
    for stale_fact in (
        "https://catfk.com",
        "https://wzyp.cn",
        "手续费更低",
        "标准接码窗口为 10 分钟",
        "标准质保期为 24 小时",
    ):
        assert stale_fact not in personality
        assert stale_fact not in PUBLIC_BUSINESS_RULES
        assert stale_fact not in runtime


def test_planning_instructions_are_chinese_with_unchanged_protocol_values() -> None:
    assert "独立的意图识别节点" in INTENT_SYSTEM_PROMPT
    assert "独立的 Plan 节点" in PLANNER_SYSTEM_PROMPT
    for prompt in (INTENT_SYSTEM_PROMPT, PLANNER_SYSTEM_PROMPT):
        assert "You are the independent planning stage" not in prompt
        assert "This is the admission intent step" not in prompt
        assert "保留空 facts" in prompt or "facts 可为空" in prompt
        assert "不直接回答用户，不调用工具" in prompt
        for name in ("answer_mode", "dependsOn", "group_sensitive", "project_inventory", "recharge_quote"):
            assert name in prompt
