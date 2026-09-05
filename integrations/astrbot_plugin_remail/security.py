from __future__ import annotations

import re
import unicodedata
from urllib.parse import urlsplit, urlunsplit

_BINDING_COMMAND = re.compile(
    r"(?:^|\s)\W*/?(?:绑定|bind)(?:@[a-z0-9_]+)?(?=\s|$)", re.IGNORECASE
)
_DIAGNOSIS_COMMAND = re.compile(
    r"(?:^|\s)\W*/?(?:诊断|接码排查|查码)(?:@[a-z0-9_]+)?(?=\s|$)", re.IGNORECASE
)
_URL = re.compile(
    r"(?ix)(?<![\w@])(?:"
    r"(?:https?://|www\.)[^\s<>\"']+|"
    r"(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+"
    r"(?:[a-z]{2,63}|xn--[a-z0-9-]{2,59})"
    r"(?::\d{1,5})?(?:/[^\s<>\"']*)?"
    r")"
)
_URL_TRAILING = ".,;:!?)]}，。；：！？》】"
_CONTROL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
_PHONE_NUMBER = re.compile(r"(?<!\d)1[3-9]\d{9}(?!\d)")
_GOVERNMENT_ID = re.compile(
    r"(?<!\d)[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])"
    r"(?:0[1-9]|[12]\d|3[01])\d{3}[0-9Xx](?!\d)"
)
_DATABASE_URL = re.compile(
    r"(?i)\b(?:mysql|mariadb|postgres(?:ql)?|redis|mongodb(?:\+srv)?|sqlite)://\S+"
)
_URL_USERINFO = re.compile(r"(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]*:[^@\s/]+@[^\s]+")
_SYSTEM_KEY = re.compile(r"(?i)\bsk_[a-z0-9_-]{4,}\b")
_OPENAI_KEY = re.compile(r"(?i)(?<![a-z0-9_-])sk-[a-z0-9_-]{6,}(?![a-z0-9_-])")
_SERVICE_TOKEN = re.compile(
    r"(?i)(?<![a-z0-9_-])(?:rk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9_]{20,}|"
    r"github_pat_[a-z0-9_]{20,}|glpat-[a-z0-9_-]{20,}|xox[baprs]-[a-z0-9-]{20,})"
    r"(?![a-z0-9_-])"
)
_AWS_ACCESS_KEY_ID = re.compile(r"(?<![A-Z0-9])(?:AKIA|ASIA)[A-Z0-9]{16}(?![A-Z0-9])")
_JWT = re.compile(
    r"(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{5,}\."
    r"[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{8,}(?![A-Za-z0-9_-])"
)
_PEM_PRIVATE_KEY = re.compile(
    r"-----BEGIN (?P<kind>[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?)-----.*?"
    r"(?:-----END (?P=kind)-----|\Z)",
    re.DOTALL,
)
_STRUCTURED_CREDENTIAL_PREFIX = re.compile(
    r"(?ix)(?<![\w-])(?P<prefix>[\"']?(?P<label>(?:"
    r"[a-z0-9_.-]*(?:password|passwd|pwd|pass|passcode|passphrase|secret|token|cookie|"
    r"credentials?|authorization|api[ ._-]?key|private[ ._-]?key|"
    r"recovery[_-]?(?:code|key))[a-z0-9_-]*|"
    r"aws[ ._-]?(?:access[ ._-]?key[ ._-]?id|secret[ ._-]?access[ ._-]?key|session[ ._-]?token)|"
    r"(?:[\u3400-\u9fff]{0,8})?(?:密码|密碼|口令|密钥|令牌|验证码|恢复码|私钥)"
    r"))[\"']?\s*[:=：]\s*)"
)
_AUTHORIZATION = re.compile(
    r"(?ix)\b(?:basic|bearer)[ \t]+(?:"
    r"\[[^\]\r\n]{1,80}\]|<[^>\r\n]{1,80}>|\$\{[^}\r\n]{1,80}\}|"
    r"[^\s,，;；}\]\r\n]+)"
)
_CREDENTIAL_PREFIX = re.compile(
    r"(?ix)(?<![\w-])(?P<prefix>[\"']?(?P<label>"
    r"x[ ._-]?(?:system|api)[ ._-]?key|system[ ._-]?key|api[ ._-]?key|"
    r"aws[ ._-]?(?:access[ ._-]?key[ ._-]?id|secret[ ._-]?access[ ._-]?key|session[ ._-]?token)|"
    r"authorization|set[ _-]?cookie|cookie|credentials?|"
    r"(?:(?:my|new|old|login)[ _-]?)?(?:password|passwd|pwd|pass|passcode|passphrase)|"
    r"client[ _-]?secret|secret|(?:access|refresh|auth|id)?[ _-]?token|"
    r"verification[ _-]?code|otp|recovery[ _-]?(?:codes?|key)|"
    r"backup[ _-]?codes?|private[ _-]?key|(?:[\u3400-\u9fff]{0,8})?"
    r"(?:密码|密碼|口令|密钥|令牌|验证码|校验码|恢复码|备用码|私钥)"
    r")[\"']?(?:"
    r"[ \t]*(?P<separator>[:=：]|\bis\b|\bset[ \t]+to\b|是(?!否)|就是|为|设置为|"
    r"设置成|设为|设成|改成|改为|换成|修改成|定为)[ \t]*|"
    r"(?P<space_separator>[ \t]+)))"
)
_VALUE_DELIMITERS = frozenset(";；}]。！？\"'")
_LINE_VALUE_LABELS = frozenset(
    {
        "cookie",
        "setcookie",
        "recoverycode",
        "recoverycodes",
        "recoverykey",
        "backupcode",
        "backupcodes",
        "恢复码",
        "备用码",
    }
)
_NON_SECRET_DESCRIPTION = re.compile(
    r"(?ix)(?:"
    r"(?:怎么|如何|什么|为什么|为何|是否|能否|可否|无法|不能|不可|无需|不要|"
    r"没收到|未收到|收不到|一直没来|迟迟不来|没来|不来|失败|报错|验证失败|"
    r"忘记了|无效|不正确|被禁用|为空|必填|可选|加密|"
    r"保存|重置|修改|设置|配置|使用|登录|失效|过期|泄露|暴露|错误|格式|方式|"
    r"请求头|标头|管理(?:指南|文档|教程)|指南|教程|文档)[^a-z0-9]*|"
    r"(?:应|应该|需要|可以|不要)(?:放在|写入|保存在|配置在|通过|使用)"
    r"(?:[ \t]+(?:authorization|http|请求))?[ \t]*(?:请求头|header|参数|字段|格式|配置)|"
    r"(?:参数|字段|请求头|header)(?:应|应该|需要|可以)?(?:使用|填写|传入|传递|"
    r"放置|保存|配置)(?:[ \t]+(?:bearer|basic|authorization|api[ _-]?key|token))?"
    r"[ \t]*(?:格式|方式|请求头|header)|"
    r"(?:(?:how[ \t]+to|cannot|should|must)[ \t]+)?(?:be[ \t]+)?(?:not[ \t]+)?"
    r"(?:required|optional|encrypted|stored|reset|empty|missing|invalid|incorrect|"
    r"expired|unavailable|disabled|enabled|used|saved|changed|configured)"
    r"(?:[ \t]+(?:field|value))?|request[ _-]?header|header|scheme|format"
    r")[.?!。！？]?"
)
_DESCRIPTION_HINT = re.compile(
    r"(?ix)^(?:(?:怎么|如何|是否|能否|应当|应该|"
    r"应(?:放在|写入|保存|存放|设置|开启|启用|配置|使用|采用|通过)|"
    r"放在|写入|保存|存放于|设置|开启|启用|使用|采用|"
    r"参数|字段|请求头|属性|方案|格式|方式|规则|鉴权|认证|长度|至少|至多)|"
    r"\b(?:how|should|must|can|budget|parameter|field|header|scheme|format|type|"
    r"length|contain|contains|requirements?|rotation|policy|authentication|overview)\b)"
)
_DESCRIPTION_TRAILING_VALUE = re.compile(
    r"(?ix)(?:格式|方式|请求头|header|scheme)[ \t]+[a-z0-9._~+/=-]{8,}\s*$|"
    r"(?:怎么|如何|什么|重置|加密|保存|配置|使用)[a-z0-9._~+/=-]{3,}\s*$"
)
_PLACEHOLDER_NAME = (
    r"(?:YOUR[ ._-]?)?(?:API[ ._-]?KEY|SYSTEM[ ._-]?KEY|TOKEN|ACCESS[ ._-]?TOKEN|"
    r"REFRESH[ _-]?TOKEN|PASSWORD|PASSPHRASE|SECRET|CLIENT[ _-]?SECRET|COOKIE|"
    r"AUTHORIZATION|OTP|VERIFICATION[ _-]?CODE|RECOVERY[ _-]?CODES?|PRIVATE[ _-]?KEY|"
    r"CREDENTIALS?|AWS[ _-]?ACCESS[ _-]?KEY[ _-]?ID|AWS[ _-]?SECRET[ _-]?ACCESS[ _-]?KEY|"
    r"AWS[ _-]?SESSION[ _-]?TOKEN)"
)
_SAFE_PLACEHOLDER = re.compile(
    rf"(?ix)(?:<{_PLACEHOLDER_NAME}>|\$\{{{_PLACEHOLDER_NAME}\}}|"
    r"\[(?:REDACTED|已脱敏|敏感信息已隐藏|凭证已隐藏)\]|REDACTED|REPLACE[ _-]?ME)"
)
_HIDDEN_VALUE = "[敏感信息已隐藏]"


def contains_binding_command(*values: str) -> bool:
    return any(_BINDING_COMMAND.search(value or "") for value in values)


def contains_sensitive_command(*values: str) -> bool:
    return any(
        contains_binding_command(value) or _DIAGNOSIS_COMMAND.search(value or "")
        for value in values
    )


def normalize_security_text(value: str) -> str:
    """Canonicalize text before security matching and remove invisible controls."""
    normalized = unicodedata.normalize("NFKC", value or "")
    normalized = "".join(
        char for char in normalized if unicodedata.category(char) != "Cf"
    )
    return _CONTROL.sub("", normalized)


def _line_end(text: str, start: int) -> int:
    ends = [index for char in "\r\n" if (index := text.find(char, start)) >= 0]
    return min(ends, default=len(text))


def _credential_value_end(text: str, start: int, *, whole_line: bool) -> int:
    line_end = _line_end(text, start)
    if start >= line_end:
        return start
    closing = {"<": ">", "[": "]", "$": "}"}.get(text[start])
    if closing and (text[start] != "$" or text.startswith("${", start)):
        end = text.find(closing, start + 1, line_end)
        if end >= 0:
            return end + 1
    quote = text[start] if text[start] in "\"'" else ""
    if quote:
        escaped = False
        for index in range(start + 1, line_end):
            char = text[index]
            if char == quote and not escaped:
                return index + 1
            escaped = char == "\\" and not escaped
            if char != "\\":
                escaped = False
        return line_end
    if whole_line:
        return line_end
    end = next(
        (index for index in range(start, line_end) if text[index] in _VALUE_DELIMITERS),
        line_end,
    )
    while end > start and text[end - 1].isspace():
        end -= 1
    return end


def _unquoted(value: str) -> str:
    stripped = value.strip()
    if len(stripped) >= 2 and stripped[0] == stripped[-1] and stripped[0] in "\"'":
        return stripped[1:-1]
    return stripped


def _is_safe_placeholder(value: str) -> bool:
    candidate = _unquoted(value)
    if _SAFE_PLACEHOLDER.fullmatch(candidate):
        return True
    parts = candidate.split(None, 1)
    return (
        len(parts) == 2
        and parts[0].casefold() in {"basic", "bearer"}
        and bool(_SAFE_PLACEHOLDER.fullmatch(parts[1]))
    )


def _is_non_secret_description(value: str) -> bool:
    candidate = _unquoted(value).strip()
    if _DESCRIPTION_TRAILING_VALUE.search(candidate):
        return False
    return bool(
        _NON_SECRET_DESCRIPTION.fullmatch(candidate)
        or _DESCRIPTION_HINT.search(candidate)
    )


def _hidden_credential_value(value: str) -> str:
    stripped = value.strip()
    if stripped[:1] in "\"'":
        quote = stripped[0]
        return f"{quote}{_HIDDEN_VALUE}{quote if stripped.endswith(quote) else ''}"
    return _HIDDEN_VALUE


def _redact_credential_assignments(text: str) -> str:
    parts: list[str] = []
    cursor = 0
    while match := _CREDENTIAL_PREFIX.search(text, cursor):
        label = re.sub(r"[ _-]", "", match.group("label").casefold())
        separator = str(match.group("separator") or "").casefold()
        end = _credential_value_end(
            text,
            match.end(),
            whole_line=label in _LINE_VALUE_LABELS,
        )
        if end <= match.end():
            parts.append(text[cursor : match.end()])
            cursor = match.end()
            continue
        value = text[match.end() : end]
        semantic_separator = bool(match.group("space_separator")) or separator in {
            "is",
            "是",
            "为",
        }
        safe = _is_safe_placeholder(value) or (
            semantic_separator and _is_non_secret_description(value)
        )
        parts.extend(
            (
                text[cursor : match.start()],
                match.group("prefix"),
                value if safe else _hidden_credential_value(value),
            )
        )
        cursor = end
    parts.append(text[cursor:])
    return "".join(parts)


def _redact_structured_credential_assignments(text: str) -> str:
    parts: list[str] = []
    cursor = 0
    while match := _STRUCTURED_CREDENTIAL_PREFIX.search(text, cursor):
        label = re.sub(r"[ _-]", "", match.group("label").casefold())
        end = _credential_value_end(
            text,
            match.end(),
            whole_line="cookie" in label or "recovery" in label or "恢复码" in label,
        )
        if end <= match.end():
            parts.append(text[cursor : match.end()])
            cursor = match.end()
            continue
        value = text[match.end() : end]
        parts.extend(
            (
                text[cursor : match.start()],
                match.group("prefix"),
                value
                if _is_safe_placeholder(value)
                else _hidden_credential_value(value),
            )
        )
        cursor = end
    parts.append(text[cursor:])
    return "".join(parts)


def _redact_authorization(match: re.Match[str]) -> str:
    scheme, value = match.group(0).split(None, 1)
    if _SAFE_PLACEHOLDER.fullmatch(value) or _is_non_secret_description(value):
        return match.group(0)
    return f"{scheme} {_HIDDEN_VALUE}"


def redact_credentials(value: str) -> str:
    """Redact credential-shaped values after Unicode canonicalization."""
    text = normalize_security_text(value)
    text = _PEM_PRIVATE_KEY.sub("[私钥已隐藏]", text)
    text = _DATABASE_URL.sub("[数据库地址已隐藏]", text)
    text = _URL_USERINFO.sub("[含凭证地址已隐藏]", text)
    text = _redact_structured_credential_assignments(text)
    text = _redact_credential_assignments(text)
    text = _AUTHORIZATION.sub(_redact_authorization, text)
    text = _SYSTEM_KEY.sub("[System Key已隐藏]", text)
    text = _OPENAI_KEY.sub("[API Key已隐藏]", text)
    text = _SERVICE_TOKEN.sub("[Token已隐藏]", text)
    text = _AWS_ACCESS_KEY_ID.sub("[AWS Access Key已隐藏]", text)
    text = _JWT.sub("[Token已隐藏]", text)
    return text


def contains_credentials(value: str) -> bool:
    normalized = normalize_security_text(value)
    return redact_credentials(normalized) != normalized


def redact_personal_data(value: str) -> str:
    """Hide common personal identifiers before model or user-facing output."""
    text = normalize_security_text(value)
    text = _GOVERNMENT_ID.sub("[身份证号已隐藏]", text)
    return _PHONE_NUMBER.sub("[手机号已隐藏]", text)


def redact_message_text(value: str) -> str:
    """Hide arguments while preserving the command token AstrBot must match."""
    match = _BINDING_COMMAND.search(value or "") or _DIAGNOSIS_COMMAND.search(
        value or ""
    )
    if match:
        return f"{match.group(0).strip()} [REDACTED]"
    redacted = redact_credentials(value)
    return redacted if redacted != normalize_security_text(value) else value


def redact_message_outline(*values: str) -> str:
    """Redact sensitive command arguments before AstrBot logs them."""
    outline = values[-1] if values else ""
    if contains_binding_command(*values):
        return "/绑定 [REDACTED]"
    if any(_DIAGNOSIS_COMMAND.search(value or "") for value in values):
        return "/诊断 [REDACTED]"
    return redact_message_text(outline)


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


def _moderation_text(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value).casefold()
    return "".join(char for char in normalized if unicodedata.category(char) != "Cf")


def keyword_blacklist_match(text: str, values: object) -> bool:
    """Case-insensitive substring matching with basic Unicode normalization."""
    if not isinstance(text, str) or not isinstance(values, (list, tuple, set)):
        return False
    haystack = _moderation_text(text)
    for value in list(values)[:200]:
        if not isinstance(value, str):
            continue
        keyword = _moderation_text(value.strip())
        if keyword and keyword in haystack:
            return True
    return False


def _normalized_domain(value: str) -> str:
    candidate = value.strip().casefold().removeprefix("*.").lstrip(".")
    try:
        parsed = urlsplit(candidate if "://" in candidate else f"//{candidate}")
        host = (parsed.hostname or "").rstrip(".")
    except ValueError:
        return ""
    if parsed.scheme and parsed.scheme not in {"http", "https"}:
        return ""
    try:
        host = host.encode("idna").decode("ascii").casefold()
    except UnicodeError:
        return ""
    if not host or len(host) > 253:
        return ""
    if ":" not in host and any(
        not label
        or len(label) > 63
        or not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?", label)
        for label in host.split(".")
    ):
        return ""
    return host


def has_disallowed_url(text: str, allowed_domains: object) -> bool:
    """Return true when an HTTP(S) URL does not match an allowed domain."""
    if not isinstance(text, str):
        return False
    values = allowed_domains if isinstance(allowed_domains, (list, tuple, set)) else []
    allowed = {
        domain
        for value in list(values)[:200]
        if isinstance(value, str) and (domain := _normalized_domain(value))
    }
    for match in _URL.finditer(text):
        host = _normalized_domain(match.group(0).rstrip(_URL_TRAILING))
        if host and not any(
            host == domain or host.endswith(f".{domain}") for domain in allowed
        ):
            return True
    return False


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
