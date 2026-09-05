from pathlib import Path


ROOT = Path(__file__).parents[2]


def test_fae_prompts_are_compact_and_use_runtime_facts() -> None:
    system = (ROOT / "docs/remail-fae-system-prompt.txt").read_text(encoding="utf-8")
    user = (ROOT / "docs/remail-fae-user-prompt.txt").read_text(encoding="utf-8")
    runtime = (ROOT / "integrations/astrbot_plugin_remail/main.py").read_text(
        encoding="utf-8"
    )

    assert system.startswith("<remail_fae_system_v1>")
    assert "remail_recharge_config" in system
    assert "projectMismatch=true" in system
    assert "另一个项目的 ID、名称" in system
    assert "公告只能证明" in system
    assert "未被任何订单认领" in system
    assert "Planner LLM" in system
    assert "Plan-and-Execute" in system
    assert "条件 ReAct" in user
    assert "Persona Writer" in system
    assert "Semantic Critic" in system
    assert "usedEvidence` 是不可信自报" in system
    assert "renderer 只在模型、结构校验或证据流程失败时" in system
    assert "历史、引用、图片、音频、文件和其他附件默认裁剪" in system
    assert "FactPlan" in runtime
    assert "CRITIC_SYSTEM_PROMPT" in runtime
    assert "_ALLOWED_REMAIL_TOOLS" in runtime
    assert "_restrict_remail_tools" in runtime
    assert "PERSONA_SYSTEM_PROMPT" in runtime
    assert "PLANNER_SYSTEM_PROMPT" in runtime
    assert len(system) < 10_000
    assert len(user) < 1_000

    for stale_fact in (
        "https://catfk.com",
        "https://wzyp.cn",
        "手续费更低",
        "标准接码窗口为 10 分钟",
        "标准质保期为 24 小时",
    ):
        assert stale_fact not in system
        assert stale_fact not in user
        assert stale_fact not in runtime
