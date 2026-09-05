from __future__ import annotations

import re
import secrets
from dataclasses import dataclass
from typing import Any

from .security import normalize_security_text

SUCCESS_DIAGNOSIS_CODES = frozenset(
    {
        "order_not_found",
        "pickup_not_requested",
        "resource_abnormal_refunded",
        "pickup_grace_period",
        "project_mismatch",
        "cause_not_confirmed",
    }
)

_DIAGNOSIS_MESSAGES = {
    "order_not_found": "当前账号下没有找到该邮箱对应的订单。 请确认邮箱后重试。",
    "pickup_not_requested": "验证码邮件已经到达，但尚未完成领取。 请回到对应订单重新获取验证码。",
    "resource_abnormal_refunded": "邮箱资源异常，系统已自动退款。 请在工作台查看退款记录后重新下单。",
    "pickup_grace_period": "验证码正在处理中。 请稍后重新获取。",
    "project_mismatch": "邮箱实际已经收到邮件，但该邮件不匹配你购买的项目。",
    "cause_not_confirmed": "暂未发现明确异常。 请稍后重试；持续无结果时联系人工客服。",
}
_BINDING_MESSAGES = {
    "binding_required": (
        "当前账号尚未绑定 ReMail。\n"
        "请先私聊机器人发送 /绑定 <ReMail邮箱> <密码> 完成绑定。"
    ),
    "account_unavailable": "当前绑定的 ReMail 账号不可用，请重新绑定或联系客服。",
}

_ALLOWED_PAYLOAD_FIELDS = frozenset(
    {
        "message",
        "bindingRequired",
        "accountUnavailable",
        "diagnosisCode",
        "result",
        "mailReceived",
        "projectMismatch",
        "projectId",
        "projectName",
    }
)
_FLAG_FIELDS = (
    "bindingRequired",
    "accountUnavailable",
    "mailReceived",
    "projectMismatch",
)
_PROJECT_NAME = re.compile(r"[\w .+&()（）'’·-]{1,120}")
_PROJECT_NAME_FORBIDDEN = re.compile(
    r"(?:另\s*一\s*个|其\s*他)\s*项\s*目|"
    r"(?:邮\s*件\s*)?(?:主\s*题|标\s*题|正\s*文|原\s*文)|"
    r"发\s*件\s*人|发\s*送\s*方|寄\s*件\s*人|验\s*证\s*码|校\s*验\s*码|"
    r"\b(?:another\s+projects?|other\s+projects?|subject|sender|from|body|message)\b",
    re.IGNORECASE,
)


@dataclass(frozen=True, slots=True)
class DiagnosisFact:
    diagnosis_code: str
    safe_message: str
    purchased_project_id: int | None = None
    purchased_project_name: str = ""


@dataclass(frozen=True, slots=True)
class DiagnosisSeal:
    token: str
    text: str


def _project_name(value: Any) -> str:
    if not isinstance(value, str) or any(char in value for char in "\r\n\t"):
        return ""
    text = " ".join(normalize_security_text(value).split())
    if not _PROJECT_NAME.fullmatch(text) or _PROJECT_NAME_FORBIDDEN.search(text):
        return ""
    return text


def _flags_are_boolean(payload: dict[str, Any]) -> bool:
    return all(
        key not in payload or type(payload[key]) is bool  # noqa: E721
        for key in _FLAG_FIELDS
    )


def _flag_is_false_or_missing(payload: dict[str, Any], key: str) -> bool:
    return key not in payload or payload[key] is False


def normalize_diagnosis_payload(
    payload: Any, *, verified: bool = True
) -> DiagnosisFact | None:
    """Return a minimal trusted fact, or None for any inconsistent payload."""
    if verified is not True or not isinstance(payload, dict):
        return None
    if set(payload) - _ALLOWED_PAYLOAD_FIELDS or not _flags_are_boolean(payload):
        return None

    raw_message = payload.get("message")
    if (
        not isinstance(raw_message, str)
        or not raw_message.strip()
        or len(raw_message) > 4000
    ):
        return None
    diagnosis_code = payload.get("diagnosisCode", "")
    result = payload.get("result", "")
    if not isinstance(diagnosis_code, str) or not isinstance(result, str):
        return None
    diagnosis_code = diagnosis_code.strip()
    result = result.strip()

    binding_required = payload.get("bindingRequired") is True
    account_unavailable = payload.get("accountUnavailable") is True
    if binding_required or account_unavailable:
        if binding_required == account_unavailable:
            return None
        if (
            diagnosis_code
            or result
            or "projectId" in payload
            or "projectName" in payload
        ):
            return None
        if not _flag_is_false_or_missing(
            payload, "mailReceived"
        ) or not _flag_is_false_or_missing(payload, "projectMismatch"):
            return None
        binding_code = "binding_required" if binding_required else "account_unavailable"
        return DiagnosisFact(
            diagnosis_code=binding_code,
            safe_message=_BINDING_MESSAGES[binding_code],
        )

    if diagnosis_code not in SUCCESS_DIAGNOSIS_CODES:
        return None
    if not _flag_is_false_or_missing(
        payload, "bindingRequired"
    ) or not _flag_is_false_or_missing(payload, "accountUnavailable"):
        return None

    project_id = payload.get("projectId")
    project_name = _project_name(payload.get("projectName"))
    if diagnosis_code == "order_not_found":
        if "projectId" in payload or "projectName" in payload:
            return None
        project_id = None
    elif (
        type(project_id) is not int  # noqa: E721
        or project_id <= 0
        or project_id > 2**63 - 1
        or not project_name
    ):
        return None

    if diagnosis_code == "project_mismatch":
        if (
            result != "project_mismatch"
            or payload.get("mailReceived") is not True
            or payload.get("projectMismatch") is not True
        ):
            return None
    elif (
        result
        or not _flag_is_false_or_missing(payload, "mailReceived")
        or not _flag_is_false_or_missing(payload, "projectMismatch")
    ):
        return None

    return DiagnosisFact(
        diagnosis_code=diagnosis_code,
        safe_message=_DIAGNOSIS_MESSAGES[diagnosis_code],
        purchased_project_id=project_id,
        purchased_project_name=project_name,
    )


def render_diagnosis_fact(fact: DiagnosisFact) -> str:
    """Render the factual section without consulting an LLM draft."""
    if fact.diagnosis_code == "project_mismatch":
        project = (
            f"你购买的是 {fact.purchased_project_name} 项目。"
            if fact.purchased_project_name
            else ""
        )
        return (
            f"{project}邮箱实际已经收到邮件，但该邮件不匹配你购买的项目，说明项目买错了。"
            "请按实际目标业务重新选择正确项目；其他项目及邮件内容不会在这里展示。"
        )
    project = (
        f"该订单对应的是 {fact.purchased_project_name} 项目。"
        if fact.purchased_project_name
        else ""
    )
    return " ".join(part for part in (project, fact.safe_message) if part)


def diagnosis_fact_payload(fact: DiagnosisFact) -> dict[str, Any]:
    """Return the only diagnosis fields that may go back to the main Agent."""
    code = fact.diagnosis_code
    if code in _BINDING_MESSAGES:
        return {"diagnosisCode": code, "message": _BINDING_MESSAGES[code]}
    if code not in SUCCESS_DIAGNOSIS_CODES:
        raise ValueError("invalid diagnosis fact")
    result: dict[str, Any] = {
        "diagnosisCode": code,
        "message": _DIAGNOSIS_MESSAGES[code],
    }
    if code != "order_not_found":
        project_name = _project_name(fact.purchased_project_name)
        if (
            type(fact.purchased_project_id) is not int  # noqa: E721
            or fact.purchased_project_id <= 0
            or not project_name
        ):
            raise ValueError("invalid diagnosis project")
        result.update(
            projectId=fact.purchased_project_id,
            projectName=project_name,
        )
    if code == "project_mismatch":
        result.update(
            result="project_mismatch",
            mailReceived=True,
            projectMismatch=True,
        )
    return result


def seal_diagnosis_fact(fact: DiagnosisFact) -> DiagnosisSeal:
    """Create a per-response opaque placeholder bound to immutable text."""
    token = f"[[REMAIL_DIAGNOSIS_{secrets.token_urlsafe(24)}]]"
    return DiagnosisSeal(token=token, text=render_diagnosis_fact(fact))


def apply_diagnosis_seal(answer: Any, seal: DiagnosisSeal) -> str:
    """Replace one exact seal; omission or duplication falls back to facts only."""
    text = answer if isinstance(answer, str) else ""
    if text.count(seal.token) != 1:
        return seal.text
    return text.replace(seal.token, seal.text)


__all__ = [
    "DiagnosisFact",
    "DiagnosisSeal",
    "SUCCESS_DIAGNOSIS_CODES",
    "apply_diagnosis_seal",
    "diagnosis_fact_payload",
    "normalize_diagnosis_payload",
    "render_diagnosis_fact",
    "seal_diagnosis_fact",
]
