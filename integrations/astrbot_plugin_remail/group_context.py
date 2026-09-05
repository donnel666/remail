"""Read the current authorized group's public text as untrusted weak background.

Wire formats: NapNeko/NapCatQQ, packages/napcat-onebot/action/group/
GetGroupNotice.ts and GetGroupEssence.ts; Telegram Bot API getChat/ChatFullInfo.
No member lookup, message lookup, history fetch, media fetch, or platform writes.
"""

from __future__ import annotations

import asyncio
import re
from collections.abc import Mapping
from datetime import datetime, timezone
from itertools import zip_longest
from typing import Any

from .feedback import (
    MAX_ITEM_CHARS,
    MAX_REPORT_CHARS,
    sanitize_report,
)
from .security import (
    contains_credentials,
    normalize_adapter_identity,
    normalize_security_text,
    redact_personal_data,
)


MAX_ITEMS = 100
MAX_TOTAL_CHARS = 12_000
REQUEST_TIMEOUT_SECONDS = 5
# Protect explicitly public numeric notation during privacy checks only; keep
# the original text in the output. Bare IDs/codes still receive the usual checks.
_PUBLIC_LITERAL = re.compile(
    r"(?ix)(?<![a-z0-9_])(?:"
    r"(?:19|20)\d{2}(?:年(?:\s*\d{1,2}月(?:\s*\d{1,2}日)?)?|[-/]\d{1,2}[-/]\d{1,2})(?!\d)|"
    r"year\s*[:：]?\s*(?:19|20)\d{2}(?!\d)|"
    r"(?:v(?:ersion)?\s*|版本\s*[:：]?\s*v?)\d{1,4}(?:\.\d{1,4}){0,3}(?:[-+][a-z0-9.-]{1,30})?(?![a-z0-9_])|"
    r"HTTP(?:/\d(?:\.\d)?)?\s*(?:status(?:\s+code)?|状态码|响应码)?\s*[:：]?\s*[1-5]\d{2}(?!\d)|"
    r"(?:HTTP/?[1-3]|TLS\s*[1-3]\.\d|OAuth\s*2(?:\.0)?|IMAP4|POP3|IPv[46]|UTF-?8|SHA-?(?:256|512))(?![a-z0-9_])|"
    r"\d{1,9}(?:,\d{3})*(?:\.\d+)?\s*(?:积分|元|人民币|美元|港元|个|条|封|份|次|人|件|项|张|批|组|种|"
    r"毫秒|秒钟?|分钟|小时|天|日|周|个月|年|%|points?|credits?|CNY|RMB|USDT|USD|HKD|ms|seconds?|minutes?|hours?|days?)(?![a-z])|"
    r"(?:[¥￥$]|RMB\s*|CNY\s*|USDT\s*|USD\s*)\d{1,9}(?:,\d{3})*(?:\.\d+)?(?![a-z0-9_])"
    r")"
)
_PLACEHOLDER = (
    r"(?:<(?:YOUR_)?(?:EMAIL|MAILBOX|SENDER|RECIPIENT|SUBJECT|BODY|CODE|VERIFICATION_CODE|"
    r"OTP|PASSWORD|TOKEN|API_KEY|SYSTEM_KEY|USER_ID|GROUP_ID|ORDER_ID|PROJECT_ID|PROJECT|"
    r"邮箱|发件人|收件人|邮件主题|邮件内容|验证码|密码|密钥|项目|项目ID)>|"
    r"\$\{(?:EMAIL|TOKEN|API_KEY|PASSWORD)\}|\[(?:REDACTED|已脱敏|敏感信息已隐藏)\])"
)
_MAIL_FIELD = (
    r"(?:(?:邮件|郵件|email|e-mail)\s*(?:正文|内容|內容|主题|主題|标题|標題)|"
    r"正文|主题|主題|标题|標題|发件人|寄件人|寄件者|收件人|"
    r"\b(?:from|to|sender|recipient|subject|body|message-id|return-path)\b)"
)
_PUBLIC_EXAMPLE = re.compile(
    rf"(?ix)[\"']?(?:{_MAIL_FIELD}|验证码|账号|账户|用户名)[\"']?\s*[:=：]\s*"
    rf"[\"']?(?:{_PLACEHOLDER}|string|integer|boolean|number|date-time|null)[\"']?(?=\s*(?:$|[,，;；。!?！？}}]))"
)
_PUBLIC_FIELD_MEANING = re.compile(
    rf"(?ix){_MAIL_FIELD}\s*(?:是|为)\s*(?:公开\s*)?(?:API|接口)?\s*的?\s*"
    r"(?:字段|属性|参数)(?:名称)?(?=\s*(?:$|[,，;；。!?！？]))"
)
_PUBLIC_ID_MEANING = re.compile(
    r"(?:订单号|订单编号|账号|账户|用户名)\s*(?:是\s*(?:指|什么|谁|否)|为\s*(?:什么|何))"
)
_PUBLIC_COMMAND = re.compile(
    rf"(?ix)(?<!\S)/(?:诊断|接码排查|查码|绑定|bind)"
    rf"(?:\s+{_PLACEHOLDER})*(?=\s*(?:$|[,，;；。!?！？)）]))"
)
_COMMAND_WORD = re.compile(
    r"(?i)(?<!\S)(?:绑定|bind|诊断|接码排查|查码)(?=[a-z\u3400-\u9fff])"
)
_MAIL_ASSIGNMENT = re.compile(
    rf"(?ix)[\"']?{_MAIL_FIELD}[\"']?\s*"
    r"(?:[:=：]|是(?!指|什么|谁|否)|为(?!什么|何)|来自|如下)\s*\S+|"
    r"(?:发件人|寄件人|寄件者|收件人)\s+[a-z0-9][^\s,，;；。]*"
)
_PRIVATE_ASSIGNMENT = re.compile(
    r"(?ix)"
    r"\b(?:qq|tg|telegram)(?:\s*(?:user|group|chat))?[ _-]?(?:id|号)?\s*[:=：]\s*-?\d+|"
    r"(?:群号|QQ号|用户ID|群ID|聊天ID)\s*[:=：]?\s*-?\d+|"
    r"\b(?:code|otp|order[ _-]?(?:id|no|number)|account|username)\s*(?:[:=：]|\bis\b)?\s*"
    r"(?=[a-z0-9_-]{4,})(?=[a-z0-9_-]*\d)[a-z0-9_-]+|"
    r"(?:验证码|驗證碼|校验码|驗證代碼)\s*(?:[:=：]|是|为)?\s*[a-z0-9_-]{4,}|"
    r"(?:订单号|订单编号|账号|账户|用户名)\s*"
    r"(?:[:=：]|是(?!\s*(?:指|什么|谁|否))|为(?!\s*(?:什么|何)))\s*\S+"
)
# Discard the whole item when it resembles private mail, an identity or a code.
# Explicit field redaction alone misses pasted mail without field labels.
_PRIVATE_TEXT = re.compile(
    r"(?ix)"
    r"\[CQ:|!\[|<(?:img|audio|video|iframe|svg|object|embed)\b|\b(?:data|file):|"
    r"@[\w\u3400-\u9fff]+|[\w.+-]+\s*(?:@|\[\s*at\s*\]|\(\s*at\s*\))\s*[\w.-]+|"
    r"(?<!\d)\d{4,}(?!\d)|"
    r"\b(?:dear\s+\w+|your\s+(?:verification|security|login|one.time)\s+code|"
    r"verify\s+your|confirm\s+your|reset\s+your\s+password|welcome\s+to|"
    r"this\s+(?:email|e-mail)|do\s+not\s+share|unsubscribe)\b|"
    r"亲爱的|親愛的|您好[，,！!]|此(?:电子|電子)?邮件|欢迎注册|歡迎註冊|"
    r"验证您的|驗證您的|登录链接|登入連結|"
    r"点击.{0,20}(?:确认|验证|登录|重置)|"
    r"\b[\w.-]+\s+(?:at|\[at\])\s+[\w.-]+\s+(?:dot|\[dot\])\s+\w+\b|"
    r"\b(?=[a-z0-9]{4,16}\b)(?=[a-z0-9]*\d)(?=[a-z0-9]*[a-z])[a-z0-9]{4,16}\b|"
    r"(?<![\w])[a-z0-9_+/=-]{24,}(?![\w])"
)


def _field(value: Any, key: str) -> Any:
    return value.get(key) if isinstance(value, Mapping) else getattr(value, key, None)


def _text(value: Any) -> tuple[str, bool]:
    if isinstance(value, list):
        if len(value) > MAX_ITEMS or any(
            _field(segment, "type") in {"at", "reply", "node", "forward"}
            for segment in value
        ):
            return "", False
        # Concatenate before checking so split email addresses/credentials cannot evade it.
        parts = [
            _field(_field(segment, "data"), "text")
            for segment in value
            if _field(segment, "type") == "text"
        ]
        if any(not isinstance(part, str) for part in parts):
            return "", False
        if sum(map(len, parts)) > MAX_REPORT_CHARS:
            return "", False
        value = "".join(parts)
    if not isinstance(value, str) or len(value) > MAX_REPORT_CHARS:
        return "", False
    value = " ".join(normalize_security_text(value).split())
    # Only fixed placeholder examples and argument-free public commands are
    # exempt; their real values and any private text elsewhere still get checked.
    reference = _PUBLIC_COMMAND.sub(" P ", _PUBLIC_EXAMPLE.sub(" P ", value))
    reference = _COMMAND_WORD.sub("操作", _PUBLIC_FIELD_MEANING.sub(" P ", reference))
    reference = _PUBLIC_ID_MEANING.sub("字段说明", reference)
    reference = " ".join(
        re.sub(_PLACEHOLDER, "<TOKEN>", reference, flags=re.IGNORECASE).split()
    )
    checked = " ".join(_PUBLIC_LITERAL.sub(" P ", reference).split())
    if (
        contains_credentials(reference)
        or redact_personal_data(reference) != reference
        or _PRIVATE_ASSIGNMENT.search(reference)
        or _MAIL_ASSIGNMENT.search(reference)
        or _PRIVATE_TEXT.search(checked)
        or sanitize_report(checked) != checked
    ):
        return "", False
    truncated = len(value) > MAX_ITEM_CHARS
    return value[: MAX_ITEM_CHARS - 1].rstrip() + "…" if truncated else value, truncated


def _timestamp(value: Any) -> datetime | None:
    try:
        if isinstance(value, datetime):
            parsed = value
        elif type(value) in (int, float) and value > 0:
            parsed = datetime.fromtimestamp(value, timezone.utc)
        else:
            return None
        return parsed.astimezone(timezone.utc) if parsed.tzinfo is not None else None
    except (ValueError, OverflowError, OSError):
        return None


def _project(
    kind: str,
    rows: list[tuple[Any, Any]],
    time_basis: str,
    now: datetime,
    max_age_days: int,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "status": "ready",
        "items": [],
        "truncated": len(rows) > MAX_ITEMS,
        "filteredByAge": 0,
        "filteredContent": 0,
    }
    for raw_text, raw_time in rows[:MAX_ITEMS]:
        published = _timestamp(raw_time)
        age = (now - published).total_seconds() / 86400 if published else None
        if max_age_days and (age is None or age < 0 or age > max_age_days):
            result["filteredByAge"] += 1
            continue
        text, text_truncated = _text(raw_text)
        if not text:
            result["filteredContent"] += 1
            continue
        result["truncated"] = result["truncated"] or text_truncated
        result["items"].append(
            {
                "kind": kind,
                "text": text,
                "publishedAt": published.isoformat() if published else None,
                "timeBasis": time_basis if published else "unknown",
                "ageDays": round(age, 3) if age is not None else None,
                "timeStatus": "unknown"
                if age is None
                else "future"
                if age < 0
                else "dated",
                "textTruncated": text_truncated,
            }
        )
    return result


def _unavailable(status: str = "unavailable") -> dict[str, Any]:
    return {"status": status, "items": [], "truncated": False}


async def _qq_source(
    call: Any, kind: str, group_id: str, self_id: str, max_age_days: int
) -> dict[str, Any]:
    if not callable(call):
        return _unavailable("unsupported")
    notice = kind == "group_notice"
    try:
        payload = await asyncio.wait_for(
            call(
                "_get_group_notice" if notice else "get_essence_msg_list",
                group_id=group_id,
                self_id=self_id,
            ),
            timeout=REQUEST_TIMEOUT_SECONDS,
        )
        if isinstance(payload, Mapping):
            if payload.get("status", "ok") != "ok" or payload.get("retcode", 0) != 0:
                return _unavailable()
            payload = payload.get("data")
        if not isinstance(payload, list):
            return _unavailable()
        if any(not isinstance(row, Mapping) for row in payload[:MAX_ITEMS]):
            return _unavailable()
        # ponytail: these NapCat actions have no paging parameter; inspect 100 returned rows.
        rows = [
            (_field(_field(row, "message"), "text"), _field(row, "publish_time"))
            if notice
            else (_field(row, "content"), _field(row, "operator_time"))
            for row in payload[:MAX_ITEMS]
        ]
        result = _project(
            kind,
            rows,
            "published" if notice else "featured",
            datetime.now(timezone.utc),
            max_age_days,
        )
        result["truncated"] = result["truncated"] or len(payload) > MAX_ITEMS
        return result
    except Exception:
        # Exception messages can contain private platform payloads or credentials.
        return _unavailable()


async def load_group_context(
    event: Any, *, authorized: bool = False, max_age_days: int = 0
) -> dict[str, Any]:
    """Only event-derived group IDs are accepted; 0 keeps old/undated weak text."""
    now = datetime.now(timezone.utc)
    valid_age = type(max_age_days) is int and max_age_days >= 0
    result: dict[str, Any] = {
        "weak": True,
        "untrusted": True,
        "currentFact": False,
        "fetchedAt": now.isoformat(),
        "maxAgeDays": max_age_days if valid_age else 0,
        "items": [],
        "sources": {},
        "truncated": False,
    }
    if authorized is not True:
        result["status"] = "not_authorized"
        return result
    if not valid_age:
        raise ValueError("max_age_days must be a nonnegative integer")
    try:
        message_type = event.get_message_type()
        if str(getattr(message_type, "value", message_type)).casefold() not in {
            "group",
            "groupmessage",
        }:
            result["status"] = "not_group"
            return result
        adapter = str(event.get_platform_name()).strip().lower()
        if adapter not in {"aiocqhttp", "telegram"}:
            result["status"] = "unsupported"
            return result
        _, group_id = normalize_adapter_identity(
            adapter, str(event.get_sender_id()), str(event.get_group_id())
        )
        if (
            not group_id
            or len(group_id) > 20
            or (adapter == "telegram" and not group_id.startswith("-"))
        ):
            result["status"] = "not_group"
            return result
    except (AttributeError, TypeError, ValueError):
        result["status"] = "unavailable"
        return result

    sources: dict[str, Any] = {}
    if adapter == "aiocqhttp":
        # The event queue has no aiocqhttp request context; explicitly select
        # the receiving connection on its shared CQHttp client.
        try:
            get_self_id = getattr(event, "get_self_id", None)
            raw_self_id = get_self_id() if callable(get_self_id) else None
            self_id = str(raw_self_id if raw_self_id is not None else "").strip()
            message_self_id = _field(getattr(event, "message_obj", None), "self_id")
            message_self_id = str(
                message_self_id if message_self_id is not None else ""
            ).strip()
            self_id = self_id or message_self_id
            self_id, _ = normalize_adapter_identity(adapter, self_id, "")
            if len(self_id) > 20 or (message_self_id and self_id != message_self_id):
                raise ValueError("invalid bot route")
        except (AttributeError, TypeError, ValueError):
            result["status"] = "unavailable"
            result["sources"] = {
                kind: {"status": "unavailable", "truncated": False}
                for kind in ("group_notice", "group_essence")
            }
            return result
        call = getattr(getattr(event, "bot", None), "call_action", None)
        values = await asyncio.gather(
            *(
                _qq_source(call, kind, group_id, self_id, max_age_days)
                for kind in ("group_notice", "group_essence")
            )
        )
        sources = dict(zip(("group_notice", "group_essence"), values))
    else:
        sources = {
            kind: _unavailable() for kind in ("group_description", "group_pinned")
        }
        sources["group_essence"] = _unavailable("unsupported")
        get_chat = getattr(getattr(event, "client", None), "get_chat", None)
        if callable(get_chat):
            try:
                chat = await asyncio.wait_for(
                    get_chat(chat_id=int(group_id)), timeout=REQUEST_TIMEOUT_SECONDS
                )
                now = datetime.now(timezone.utc)
                if str(_field(chat, "id")) != group_id or _field(chat, "type") not in {
                    "group",
                    "supergroup",
                }:
                    raise ValueError("unexpected chat")
                description = _field(chat, "description")
                sources["group_description"] = _project(
                    "group_description",
                    [(description, None)] if description else [],
                    "unknown",
                    now,
                    max_age_days,
                )
                pinned = _field(chat, "pinned_message")
                rows = []
                if pinned is not None:
                    if str(_field(_field(pinned, "chat"), "id")) != group_id:
                        raise ValueError("unexpected pinned chat")
                    text = _field(pinned, "text")
                    if any(
                        _field(entity, "type") in {"mention", "text_mention"}
                        for entity in (_field(pinned, "entities") or ())
                    ):
                        text = None
                    rows = [(text, _field(pinned, "date"))]
                sources["group_pinned"] = _project(
                    "group_pinned", rows, "sent", now, max_age_days
                )
                sources["group_pinned"]["coverage"] = "latest_pinned_message_only"
            except Exception:
                pass
        else:
            sources["group_description"] = _unavailable("unsupported")
            sources["group_pinned"] = _unavailable("unsupported")

    # Interleave categories so one large source cannot crowd out the other.
    used_chars = 0
    for batch in zip_longest(*(source["items"] for source in sources.values())):
        for item in batch:
            if item is None:
                continue
            if (
                len(result["items"]) >= MAX_ITEMS
                or used_chars + len(item["text"]) > MAX_TOTAL_CHARS
            ):
                sources[item["kind"]]["truncated"] = True
                continue
            result["items"].append(item)
            used_chars += len(item["text"])
    result["sources"] = {
        kind: {key: value for key, value in source.items() if key != "items"}
        for kind, source in sources.items()
    }
    result["truncated"] = any(source["truncated"] for source in sources.values())
    result["fetchedAt"] = datetime.now(timezone.utc).isoformat()
    result["status"] = (
        "ready"
        if all(source["status"] == "ready" for source in sources.values())
        else "partial"
        if any(source["status"] == "ready" for source in sources.values())
        else "unavailable"
    )
    return result
