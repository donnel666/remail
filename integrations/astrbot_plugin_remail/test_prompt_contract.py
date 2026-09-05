from pathlib import Path

from .persona import CRITIC_SYSTEM_PROMPT, PERSONA_SYSTEM_PROMPT
from .workflow import PLANNER_SYSTEM_PROMPT, PUBLIC_BUSINESS_RULES


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
