from __future__ import annotations

import re
from urllib.parse import urlsplit, urlunsplit

_BINDING_COMMAND = re.compile(
    r"(?:^|\s)\W*/?(?:绑定|bind)(?:@[a-z0-9_]+)?(?=\s|$)", re.IGNORECASE
)


def contains_binding_command(*values: str) -> bool:
    return any(_BINDING_COMMAND.search(value or "") for value in values)


def redact_message_outline(*values: str) -> str:
    """Redact credential-bearing binding commands before AstrBot logs them."""
    outline = values[-1] if values else ""
    return "/绑定 [REDACTED]" if contains_binding_command(*values) else outline


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
