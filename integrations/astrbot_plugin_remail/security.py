from __future__ import annotations

import re
from urllib.parse import urlsplit, urlunsplit

_BINDING_COMMAND = re.compile(
    r"(?:^|\s)\W*/?(?:绑定|bind)(?:@[a-z0-9_]+)?(?=\s|$)", re.IGNORECASE
)
_DIAGNOSIS_COMMAND = re.compile(
    r"(?:^|\s)\W*/?(?:诊断|接码排查|查码)(?:@[a-z0-9_]+)?(?=\s|$)", re.IGNORECASE
)


def contains_binding_command(*values: str) -> bool:
    return any(_BINDING_COMMAND.search(value or "") for value in values)


def contains_sensitive_command(*values: str) -> bool:
    return any(
        contains_binding_command(value) or _DIAGNOSIS_COMMAND.search(value or "")
        for value in values
    )


def redact_message_text(value: str) -> str:
    """Hide arguments while preserving the command token AstrBot must match."""
    match = _BINDING_COMMAND.search(value or "") or _DIAGNOSIS_COMMAND.search(
        value or ""
    )
    return f"{match.group(0).strip()} [REDACTED]" if match else value


def redact_message_outline(*values: str) -> str:
    """Redact sensitive command arguments before AstrBot logs them."""
    outline = values[-1] if values else ""
    if contains_binding_command(*values):
        return "/绑定 [REDACTED]"
    if any(_DIAGNOSIS_COMMAND.search(value or "") for value in values):
        return "/诊断 [REDACTED]"
    return outline


def _positive_decimal(value: str) -> bool:
    return bool(value) and value[0] != "0" and all("0" <= char <= "9" for char in value)


def normalize_adapter_identity(
    adapter: str, subject: str, group_id: str
) -> tuple[str, str]:
    """Validate IDs supplied by AstrBot and normalize Telegram topic groups."""
    adapter = (adapter or "").strip().lower()
    subject = (subject or "").strip()
    group_id = (group_id or "").strip()
    if adapter == "aiocqhttp":
        if not _positive_decimal(subject):
            raise ValueError("QQ 适配器没有提供有效的真实 QQ 号。")
        if group_id and not _positive_decimal(group_id):
            raise ValueError("QQ 适配器没有提供有效的真实QQ群号。")
    elif adapter == "telegram":
        if not _positive_decimal(subject):
            raise ValueError("Telegram 适配器没有提供有效的用户 ID。")
        group_id = group_id.partition("#")[0]
        numeric_group = group_id.removeprefix("-")
        if group_id and not _positive_decimal(numeric_group):
            raise ValueError("Telegram 适配器没有提供有效的群 Chat ID。")
    elif not subject:
        raise ValueError("消息平台没有提供有效用户身份。")
    return subject, group_id


def adapter_channel(adapter: str) -> str:
    adapter = (adapter or "").strip().lower()
    try:
        return {"aiocqhttp": "qq", "telegram": "telegram"}[adapter]
    except KeyError as exc:
        raise ValueError("当前 AstrBot 适配器没有配置 ReMail 渠道。") from exc


def channel_system_keys(qq_key: str, telegram_key: str) -> dict[str, str]:
    qq_key, telegram_key = qq_key.strip(), telegram_key.strip()
    if qq_key and qq_key == telegram_key:
        raise ValueError("QQ 和 Telegram 不能使用同一把 ReMail System Key。")
    return {
        channel: key
        for channel, key in (("qq", qq_key), ("telegram", telegram_key))
        if key
    }


def validated_base_url(value: str) -> str:
    """Require TLS except for an explicitly loopback ReMail server."""
    parsed = urlsplit((value or "").strip().rstrip("/"))
    if parsed.scheme == "https" and parsed.netloc:
        return urlunsplit(parsed)
    if parsed.scheme == "http" and parsed.hostname in {"localhost", "127.0.0.1", "::1"}:
        return urlunsplit(parsed)
    raise ValueError(
        "ReMail base_url must use HTTPS, except for a loopback development server."
    )


def websocket_url(base_url: str) -> str:
    parsed = urlsplit(validated_base_url(base_url))
    scheme = "wss" if parsed.scheme == "https" else "ws"
    path = parsed.path.rstrip("/") + "/v1/bot/ws"
    return urlunsplit((scheme, parsed.netloc, path, "", ""))
