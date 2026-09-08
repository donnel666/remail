"""Isolated Proton WEB/PKL bridge. stdin/stdout are secret machine channels.

Never invoke proton.py: its connect/main persist pickle and read mail implicitly.
Install requirements-pkl.txt with --no-deps; CAPTCHA imports are deliberately blocked.
"""

import base64
import contextlib
import hashlib
import hmac
import io
import json
import pickle
import pickletools
import re
import sys
import time
import types
from datetime import datetime, timezone
from email import policy
from email.parser import BytesParser
from urllib.parse import urlsplit

MAX_PKL = 8 << 20
MAX_MESSAGE = 32 << 20
MAX_LINE = 32 << 20
PAGE_SIZE = 150
PAIR_FIELDS = {"is_primary", "is_user_key", "fingerprint_public", "fingerprint_private",
               "public_key", "private_key", "passphrase", "email"}
HEADER_NAMES = {"authority", "accept", "accept-language", "content-type", "origin", "referer",
                "user-agent", "x-pm-appversion", "x-pm-apiversion", "x-pm-locale", "authorization", "x-pm-uid"}
MODULUS_KEY = """-----BEGIN PGP PUBLIC KEY BLOCK-----

xjMEXAHLgxYJKwYBBAHaRw8BAQdAFurWXXwjTemqjD7CXjXVyKf0of7n9Ctm
L8v9enkzggHNEnByb3RvbkBzcnAubW9kdWx1c8J3BBAWCgApBQJcAcuDBgsJ
BwgDAgkQNQWFxOlRjyYEFQgKAgMWAgECGQECGwMCHgEAAPGRAP9sauJsW12U
MnTQUZpsbJb53d0Wv55mZIIiJL2XulpWPQD/V6NglBd96lZKBmInSXX/kXat
Sv+y0io+LR8i2+jV+AbOOARcAcuDEgorBgEEAZdVAQUBAQdAeJHUz1c9+KfE
kSIgcBRE3WuXC4oj5a2/U3oASExGDW4DAQgHwmEEGBYIABMFAlwBy4MJEDUF
hcTpUY8mAhsMAAD/XQD8DxNI6E78meodQI+wLsrKLeHn32iLvUqJbVDhfWSU
WO4BAMcm1u02t4VKw++ttECPt+HUgPUq5pqQWe5Q2cW4TMsE
=Y4Mw
-----END PGP PUBLIC KEY BLOCK-----"""


class BridgeError(Exception):
    def __init__(self, stage="protocol", category="protocol", http_status=0, api_code=0,
                 retryable=False, proxy_failure=False):
        super().__init__(category)
        self.event = dict(event="error", stage=stage, category=category, http_status=http_status,
                          api_code=api_code, retryable=retryable, proxy_failure=proxy_failure)


def need(condition, stage="session", category="protocol"):
    if not condition:
        raise BridgeError(stage, category)


def bounded_text(value, maximum=8192, empty=True, *, stage="session", category="protocol"):
    need(type(value) is str and len(value.encode("utf-8")) <= maximum and (empty or bool(value)), stage, category)
    return value


def identity(value, *, stage="session"):
    value = bounded_text(value, 320, False, stage=stage).strip().lower()
    need(re.fullmatch(r"[^@\s<>\x00-\x1f]+@[^@\s<>\x00-\x1f]+", value) is not None, stage)
    return value


def primitive_tree(value, active=None, budget=None, depth=0):
    """Reject cycles, aliases with exponential traversal, and nonprimitive objects."""
    active = set() if active is None else active
    budget = [100000] if budget is None else budget
    budget[0] -= 1
    need(budget[0] >= 0 and depth <= 16)
    if type(value) in (str, bytes):
        need(len(value) <= MAX_PKL)
    elif value is None or type(value) is bool:
        return
    elif type(value) in (dict, list):
        need(id(value) not in active and len(value) <= 10000)
        active.add(id(value))
        for key, child in value.items() if type(value) is dict else enumerate(value):
            if type(value) is dict:
                need(type(key) is str and len(key) <= 8192)
            primitive_tree(child, active, budget, depth + 1)
        active.remove(id(value))
    else:
        raise BridgeError("session")


class RestrictedUnpickler(pickle.Unpickler):
    def find_class(self, module, name):
        raise BridgeError("session")

    def persistent_load(self, pid):
        raise BridgeError("session")


def load_pickle(encoded):
    bounded_text(encoded, MAX_PKL * 4 // 3 + 8, False)
    try:
        raw = base64.b64decode(encoded, validate=True)
        need(0 < len(raw) <= MAX_PKL)
        allowed = {"PROTO", "FRAME", "EMPTY_DICT", "EMPTY_LIST", "MARK", "MEMOIZE", "SHORT_BINUNICODE",
                   "BINUNICODE", "BINUNICODE8", "SHORT_BINBYTES", "BINBYTES", "BINBYTES8", "NONE",
                   "NEWTRUE", "NEWFALSE", "SETITEM", "SETITEMS", "APPEND", "APPENDS", "BINGET",
                   "LONG_BINGET", "BINPUT", "LONG_BINPUT", "STOP"}
        stop = -1
        for opcode, _, position in pickletools.genops(raw):
            need(opcode.name in allowed)
            if opcode.name == "STOP":
                stop = position
        need(stop == len(raw) - 1)
        value = RestrictedUnpickler(io.BytesIO(raw)).load()
        validate_session(value)
        return value
    except BridgeError:
        raise
    except Exception:
        raise BridgeError("session") from None


def validate_session(value, recipient=None):
    primitive_tree(value)
    need(type(value) is dict and set(value) == {"pgp", "account_addresses", "headers", "cookies"})
    pgp = value["pgp"]
    need(type(pgp) is dict and set(pgp) == {"pairs_keys", "aes256_keys"})
    pairs = pgp["pairs_keys"]
    need(type(pairs) is list and 1 <= len(pairs) <= 4096)
    has_user = False
    key_emails = set()
    for pair in pairs:
        need(type(pair) is dict and set(pair) == PAIR_FIELDS)
        need(type(pair["is_user_key"]) is bool and type(pair["is_primary"]) is bool)
        for key in ("fingerprint_public", "fingerprint_private", "public_key", "private_key", "passphrase", "email"):
            if pair[key] is not None:
                bounded_text(pair[key], (1 << 20) if key.endswith("key") else 8192)
        need(bool(pair["private_key"]) and pair["private_key"].startswith("-----BEGIN PGP PRIVATE KEY BLOCK-----"))
        need(type(pair["passphrase"]) is str and pair["passphrase"] != "")
        need(bool(pair["fingerprint_private"]))
        email = identity(pair["email"])
        has_user = has_user or pair["is_user_key"]
        if not pair["is_user_key"]:
            key_emails.add(email)
    need(has_user)
    cache = pgp["aes256_keys"]
    need(type(cache) is dict and len(cache) <= 100)
    for name, secret in cache.items():
        bounded_text(name, 8192)
        need(type(secret) is bytes and len(secret) in (16, 24, 32))
    addresses = value["account_addresses"]
    need(type(addresses) is list and 1 <= len(addresses) <= 4096)
    seen_ids, seen_emails = set(), set()
    for address in addresses:
        need(type(address) is dict and set(address) == {"id", "email", "name"})
        aid = bounded_text(address["id"], 1024, False)
        email = identity(address["email"])
        bounded_text(address["name"], 8192)
        need(aid not in seen_ids and email not in seen_emails)
        seen_ids.add(aid)
        seen_emails.add(email)
    headers = value["headers"]
    need(type(headers) is dict and 1 <= len(headers) <= len(HEADER_NAMES))
    for name, val in headers.items():
        need(name == name.lower() and name in HEADER_NAMES)
        bounded_text(val, 16384)
        need(not any(ord(c) < 32 or ord(c) == 127 for c in val))
    need(bool(headers.get("x-pm-uid")) and bool(headers.get("authorization")))
    cookies = value["cookies"]
    need(type(cookies) is dict and 1 <= len(cookies) <= 512)
    for name, val in cookies.items():
        need(re.fullmatch(r"[!#$%&'*+\-.^_`|~0-9A-Za-z]{1,256}", name) is not None)
        bounded_text(val, 16384)
        need(not any(ord(c) < 32 or ord(c) == 127 for c in val))
    if recipient is not None:
        target = identity(recipient)
        need(target in seen_emails and target in key_emails, "session", "identity_mismatch")
    return value


def dump_session(client, recipient):
    value = {"pgp": {"pairs_keys": [pair.to_dict() for pair in client.pgp.pairs_keys],
                     "aes256_keys": dict(list(client.pgp.aes256_keys.items())[:100])},
             "account_addresses": [{"id": a.id, "email": a.email, "name": a.name} for a in client.account_addresses],
             "headers": {k.lower(): v for k, v in client.session.headers.items()},
             "cookies": client.session.cookies.get_dict()}
    validate_session(value, recipient)
    raw = pickle.dumps(value, protocol=4)
    need(len(raw) <= MAX_PKL)
    addresses = sorted(({"id": a["id"], "email": identity(a["email"])} for a in value["account_addresses"]), key=lambda a: a["id"])
    keys = sorted(json.dumps(pair, sort_keys=True, separators=(",", ":")) for pair in value["pgp"]["pairs_keys"])
    fingerprint = hashlib.sha256(json.dumps([addresses, keys], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return dict(event="session", pkl=base64.b64encode(raw).decode("ascii"), uid=value["headers"]["x-pm-uid"],
                addresses=addresses, key_fingerprint=fingerprint)


def restore_session(client, value, recipient, models):
    validate_session(value, recipient)
    client.pgp.pairs_keys = [models.PgpPairKeys(**pair) for pair in value["pgp"]["pairs_keys"]]
    client.pgp.aes256_keys = value["pgp"]["aes256_keys"]
    client.account_addresses = [models.AccountAddress(**a) for a in value["account_addresses"]]
    client.session.headers.clear()
    client.session.headers.update(value["headers"])
    client.session.cookies.clear()
    for name, val in value["cookies"].items():
        client.session.cookies.set(name, val)


def request_stage(action, method, url):
    parsed = urlsplit(url)
    need(parsed.scheme == "https" and parsed.port in (None, 443) and not parsed.username and not parsed.password
         and not parsed.fragment, "transport")
    host, path = parsed.hostname, parsed.path
    need("%" not in path and ".." not in path and "\\" not in path, "transport")
    exact = {
        ("login", "POST", "account.proton.me", "/api/auth/v4/sessions"): "login",
        ("login", "POST", "mail.proton.me", "/api/core/v4/auth/cookies"): "cookies",
        ("login", "POST", "account.proton.me", "/api/core/v4/auth/info"): "auth_info",
        ("login", "POST", "account.proton.me", "/api/core/v4/auth"): "auth",
        ("login", "POST", "account.proton.me", "/api/auth/v4/sessions/forks"): "cookies",
        ("login", "GET", "account.proton.me", "/api/core/v4/users"): "keys",
        ("login", "GET", "account.proton.me", "/api/core/v4/keys/salts"): "keys",
        ("login", "GET", "api.protonmail.ch", "/api/core/v4/addresses"): "addresses",
        ("refresh", "POST", "mail.proton.me", "/api/auth/refresh"): "refresh",
        ("fetch", "GET", "mail.proton.me", "/api/mail/v4/messages"): "list",
    }
    stage = exact.get((action, method.upper(), host, path))
    if stage is None and method.upper() == "GET" and host == "mail.proton.me":
        if action == "login" and re.fullmatch(r"/api/auth/v4/sessions/forks/[A-Za-z0-9_=-]+", path):
            stage = "cookies"
        elif action == "fetch" and re.fullmatch(r"/api/mail/v4/messages/[A-Za-z0-9_=-]+", path):
            stage = "read"
    need(stage is not None, "transport")
    return stage


def response_error(stage, status, code, action):
    if status == 429:
        return BridgeError(stage, "rate_limited", status, code, True)
    if status >= 500 or status == 408:
        return BridgeError(stage, "request", status, code, True)
    if code in (9001, 12087, 10004):
        return BridgeError(stage, "action_required", status, code)
    if code == 2028:
        return BridgeError(stage, "rate_limited", status, code, True)
    if action in ("fetch", "refresh") and (status == 401 or code in (8002, 6003, 10013)):
        return BridgeError(stage, "session_revoked", status, code)
    if (stage == "auth_info" and code == 6003) or (stage == "auth" and code in (6003, 8002)):
        return BridgeError(stage, "invalid_credentials", status, code)
    if code in (5001, 5003):
        return BridgeError(stage, "protocol", status, code)
    return BridgeError(stage, "request", status, code, status >= 500 or status in (0, 408))


def block_manual(*args, **kwargs):
    raise BridgeError("auth", "action_required")


def build_client(action, proxy):
    """Import the pinned client only after replacing Session; no library logging."""
    try:
        import requests
        from curl_cffi import requests as curl_requests
        from pgpy import PGPKey, PGPMessage, PGPSignature
        stub = types.ModuleType("protonmail.utils.captcha_auto_solver_utils")
        stub.get_captcha_puzzle_coordinates = block_manual
        stub.solve_challenge = block_manual
        sys.modules[stub.__name__] = stub

        class ChromeSession(curl_requests.Session):
            def __init__(self, *args, **kwargs):
                kwargs.setdefault("impersonate", "chrome")
                kwargs.setdefault("trust_env", False)
                super().__init__(*args, **kwargs)
                self.calls = 0
                self.login_data = {}

            def request(self, method, url, **kwargs):
                stage = request_stage(action, method, url)
                self.calls += 1
                need(self.calls <= (100000 if action == "fetch" else 20), "transport")
                kwargs.update(timeout=30, allow_redirects=False, verify=True, stream=True)
                try:
                    response = super().request(method, url, **kwargs)
                    parts, size = [], 0
                    try:
                        for chunk in response.iter_content():
                            size += len(chunk)
                            need(size <= MAX_MESSAGE, stage)
                            parts.append(chunk)
                    finally:
                        response.close()
                    response.content = b"".join(parts)
                except BridgeError:
                    raise
                except Exception:
                    raise BridgeError(stage, "request", retryable=True, proxy_failure=bool(proxy)) from None
                try:
                    data = response.json()
                except Exception:
                    data = {}
                code = data.get("Code", 0) if type(data) is dict else 0
                code = code if type(code) is int and 0 <= code <= 1000000 else 0
                # Stop HTTP failures before the library can refresh/retry a 401,
                # including an HTML response with no Proton JSON envelope.
                if not 200 <= response.status_code < 300:
                    raise response_error(stage, response.status_code, code, action)
                need(type(data) is dict and type(data.get("Code")) is int, stage)
                if code not in (1000, 1001):
                    raise response_error(stage, response.status_code, code, action)
                if action == "login":
                    if "User" in data:
                        self.login_data["user"] = data["User"]
                    if "Addresses" in data:
                        self.login_data["addresses"] = data["Addresses"]
                return response

        requests.Session = ChromeSession
        from protonmail import ProtonMail
        from protonmail import models
        import protonmail.client as proton_client
        proton_client.Session = ChromeSession

        class SecureProtonMail(ProtonMail):
            def _parse_info_before_login(self, info, password):
                try:
                    need(type(info) is dict and info.get("Version") in (3, 4), "auth_info")
                    modulus = bounded_text(info["Modulus"], 16384, False, stage="auth_info")
                    need(modulus.startswith("-----BEGIN PGP SIGNED MESSAGE-----")
                         and modulus.strip().endswith("-----END PGP SIGNATURE-----")
                         and modulus.count("-----BEGIN PGP SIGNATURE-----") == 1
                         and modulus.count("-----END PGP SIGNATURE-----") == 1, "auth_info")
                    signed = PGPMessage.from_blob(modulus)
                    pinned, _ = PGPKey.from_blob(MODULUS_KEY)
                    need(bool(pinned.verify(signed)), "auth_info")
                    need(len(base64.b64decode(signed.message, validate=True)) == 256, "auth_info")
                    need(len(base64.b64decode(info["Salt"], validate=True)) == 10, "auth_info")
                    need(len(base64.b64decode(info["ServerEphemeral"], validate=True)) == 256, "auth_info")
                    bounded_text(info["SRPSession"], 1024, False, stage="auth_info")
                    return super()._parse_info_before_login(info, password)
                except BridgeError:
                    raise
                except Exception:
                    raise BridgeError("auth_info") from None

            def _login_process(self, auth):
                try:
                    proof = base64.b64decode(auth["ServerProof"], validate=True)
                    need(hmac.compare_digest(proof, self.user.expected_server_proof), "auth")
                    self.user.verify_session(proof)
                    need(self.user.authenticated(), "auth")
                    twofa = auth.get("2FA", {})
                    if auth.get("TwoFactor") or (type(twofa) is dict and twofa.get("Enabled")) or auth.get("PasswordMode") == 2:
                        block_manual()
                    return True
                except BridgeError:
                    raise
                except Exception:
                    raise BridgeError("auth") from None

            def _captcha_processing(self, *args, **kwargs):
                block_manual()

            def read_message(self, message_id, mark_as_read=False):
                # Do not call the library's attachment/MIME parser: it drops nested
                # bodies and the original recipient count needed by history matching.
                need(mark_as_read is False, "read")
                raw = self._get("mail", "mail/v4/messages/" + message_id).json().get("Message")
                need(type(raw) is dict and type(raw.get("Body")) is str, "read")
                need(raw.get("ID") == message_id and belongs(raw, self.receiving_address), "read")
                try:
                    cleartext = self.pgp.decrypt(raw["Body"])
                    if isinstance(cleartext, (bytes, bytearray)):
                        cleartext = bytes(cleartext).decode("utf-8")
                    bounded_text(cleartext, MAX_MESSAGE, stage="decrypt", category="decryption")
                    raw = dict(raw)
                    raw["Body"] = readable_body(cleartext, raw.get("MIMEType", ""))
                    return raw
                except BridgeError:
                    raise
                except Exception:
                    raise BridgeError("decrypt", "decryption") from None

            def validate_keys(self, recipient):
                # Keep the Go implementation's address-token signature and key
                # usability checks; the upstream Python client omits these checks.
                try:
                    accounts = self.session.login_data.get("addresses")
                    need(type(accounts) is list and accounts, "addresses")
                    remote_user_keys = self.session.login_data.get("user", {}).get("Keys", [])
                    user_keys = []
                    for pair in self.pgp.pairs_keys:
                        if pair.is_user_key:
                            need(any(k.get("PrivateKey") == pair.private_key and k.get("Active", 1)
                                     for k in remote_user_keys), "keys", "action_required")
                        key, _ = PGPKey.from_blob(pair.private_key)
                        with key.unlock(pair.passphrase):
                            need(key.is_unlocked, "keys", "action_required")
                        if pair.is_user_key:
                            user_keys.append(key.pubkey)
                    usable = set()
                    for address in accounts:
                        email = identity(address["Email"], stage="addresses")
                        need(type(address.get("Status")) is int and 0 <= address["Status"] <= 2, "addresses")
                        if address.get("Status") != 1 or not address.get("Receive", 1):
                            continue
                        for remote in address.get("Keys", []):
                            if not remote.get("Active", 1):
                                continue
                            pair = next((p for p in self.pgp.pairs_keys if not p.is_user_key and identity(p.email) == email
                                         and p.private_key == remote.get("PrivateKey")), None)
                            if pair is None:
                                continue
                            if remote.get("Token"):
                                signature = PGPSignature.from_blob(remote["Signature"])
                                need(any(bool(key.verify(pair.passphrase, signature)) for key in user_keys), "keys")
                            usable.add(email)
                    need(identity(recipient) in usable, "addresses", "identity_mismatch")
                    self.account_addresses = [a for a in self.account_addresses if identity(a.email) in usable]
                    self.pgp.pairs_keys = [p for p in self.pgp.pairs_keys if p.is_user_key or identity(p.email) in usable]
                except BridgeError:
                    raise
                except Exception:
                    raise BridgeError("keys", "action_required") from None

        return SecureProtonMail(proxy=proxy or None, logging_level=0, logging_func=lambda *args: None), models
    except BridgeError:
        raise
    except Exception:
        raise BridgeError("dependency", "protocol") from None


def readable_body(body, mime_type):
    media = mime_type.split(";", 1)[0].strip().lower()
    if not media.startswith("multipart/") and media != "message/rfc822":
        return body
    try:
        entity = BytesParser(policy=policy.default).parsebytes(body.encode("utf-8"))
        plain, html = [], []
        budget = [MAX_MESSAGE]

        def walk(part, depth):
            need(depth <= 20 and not part.defects, "mime", "decryption")
            if part.get_content_disposition() == "attachment" or part.get_filename() or part.get_param("name"):
                return
            if part.is_multipart():
                for child in part.iter_parts():
                    walk(child, depth + 1)
            elif part.get_content_type() in ("text/plain", "text/html"):
                payload = part.get_payload(decode=True)
                need(type(payload) is bytes, "mime", "decryption")
                budget[0] -= len(payload)
                need(budget[0] >= 0, "mime", "decryption")
                text = payload.decode(part.get_content_charset() or "utf-8")
                (html if part.get_content_type() == "text/html" else plain).append(text)

        walk(entity, 0)
        return "\n".join(html or plain)
    except BridgeError:
        raise
    except Exception:
        raise BridgeError("mime", "decryption") from None


def recipient_list(raw, name, *, stage="read"):
    values = raw.get(name, [])
    if values is None:
        values = []
    need(type(values) is list and len(values) <= 10000, stage)
    result = []
    for item in values:
        need(type(item) is dict, stage)
        result.append({"Name": bounded_text(item.get("Name", ""), 8192, stage=stage),
                       "Address": bounded_text(item.get("Address", ""), 8192, stage=stage)})
    return result


def belongs(raw, address, *, stage="read"):
    present = False
    for name in ("ToList", "CCList", "BCCList"):
        for item in recipient_list(raw, name, stage=stage):
            email = item["Address"].strip().lower()
            present = present or bool(email)
            if email == address["email"]:
                return True
    return not present and raw.get("AddressID") == address["id"]


def known_message(raw, folder, known):
    mid = raw["ID"].strip().lower()
    external = bounded_text(raw.get("ExternalID", ""), 8192, stage="list").strip().strip("<>").lower()
    return mid in known or (external and "internet:" + external in known) or "provider:proton:" + folder.lower() + ":" + mid in known


def message_record(client, meta, folder, address):
    raw = client.read_message(meta["ID"], mark_as_read=False)
    need(type(raw) is dict and raw.get("ID") == meta["ID"] and type(raw.get("Time")) is int and raw["Time"] > 0
         and belongs(raw, address), "read")
    to = recipient_list(raw, "ToList")
    sender = raw.get("Sender", {})
    need(type(sender) is dict, "read")
    record = {name: bounded_text(raw.get(name, ""), MAX_MESSAGE if name in ("Body", "Header") else 65536, stage="read")
              for name in ("ID", "AddressID", "Subject", "Body", "MIMEType", "Header", "ExternalID")}
    record.update(Folder=folder, Sender={"Name": bounded_text(sender.get("Name", ""), stage="read"), "Address": bounded_text(sender.get("Address", ""), stage="read")},
                  ToList=to, CCList=recipient_list(raw, "CCList"), BCCList=recipient_list(raw, "BCCList"),
                  OriginalToCount=len(to), ReceivedAt=datetime.fromtimestamp(raw["Time"], timezone.utc).isoformat().replace("+00:00", "Z"))
    return record


def timestamp(value, default):
    if value in (None, "", "0001-01-01T00:00:00Z"):
        return default
    need(type(value) is str and len(value) <= 64, "input")
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
        need(result.tzinfo is not None, "input")
        return result.timestamp()
    except BridgeError:
        raise
    except Exception:
        raise BridgeError("input") from None


def fetch(client, request, state, emit):
    recipient = identity(request.get("recipient", ""), stage="input")
    address = next(a for a in state["account_addresses"] if identity(a["email"]) == recipient)
    address = dict(address, email=recipient)
    client.receiving_address = address
    since, until = timestamp(request.get("since_at"), 0), timestamp(request.get("until_at"), time.time())
    need(0 <= since <= until, "input")
    full = request.get("full_history", False)
    limit = request.get("max_messages", 30) or 30
    need(type(full) is bool and type(limit) is int and 1 <= limit, "input")
    limit = min(limit, 1000)
    ids = request.get("known_message_ids", [])
    need(type(ids) is list and len(ids) <= 100000, "input")
    known = set() if full else {bounded_text(mid, 2048, stage="input").strip().lower() for mid in ids}
    candidates, seen = [], set()
    complete = True

    def emit_batch(messages):
        batch, size = [], 0
        for item in messages:
            item_size = len(json.dumps(item, ensure_ascii=False, separators=(",", ":")).encode()) + 1
            need(item_size < MAX_LINE - 100, "read")
            if batch and (len(batch) >= 100 or size + item_size > MAX_LINE - 100):
                emit(dict(event="messages", messages=batch))
                batch, size = [], 0
            batch.append(item)
            size += item_size
        if batch:
            emit(dict(event="messages", messages=batch))

    for label, folder in (("0", "Inbox"), ("4", "Junk")):
        page, count, page_ids = 0, 0, set()
        while True:
            query = dict(Page=page, PageSize=PAGE_SIZE, LabelID=label, Sort="Time", Desc=1, AddressID=address["id"], End=int(until))
            if since:
                query["Begin"] = int(since)
            response = client._get("mail", "mail/v4/messages", params=query).json()
            messages = response.get("Messages")
            need(type(messages) is list and len(messages) <= PAGE_SIZE and not response.get("Stale"), "list")
            boundary = False
            for meta in messages:
                need(type(meta) is dict and type(meta.get("ID")) is str and re.fullmatch(r"[A-Za-z0-9_=-]{1,1024}", meta["ID"])
                     and type(meta.get("Time")) is int and meta["Time"] > 0 and meta["ID"] not in page_ids, "list")
                page_ids.add(meta["ID"])
                if not since <= meta["Time"] <= until or not belongs(meta, address, stage="list") or meta["ID"] in seen:
                    continue
                if known_message(meta, folder, known):
                    boundary, complete = True, False
                    break
                seen.add(meta["ID"])
                count += 1
                if full:
                    # Emit as we decrypt: one page may contain 150 large bodies.
                    # Do not hold that entire page in Python memory.
                    emit_batch([message_record(client, meta, folder, address)])
                else:
                    candidates.append((meta, folder))
                    if count >= limit:
                        break
            if boundary or (not full and count >= limit):
                complete = False
                break
            if len(messages) < PAGE_SIZE:
                total = response.get("Total")
                need(total is None or (type(total) is int and total >= 0 and len(page_ids) >= total), "list")
                break
            page += 1
    if not full:
        candidates.sort(key=lambda item: (-item[0]["Time"], item[0]["ID"]))
        if len(candidates) > limit:
            complete = False
        emit_batch(message_record(client, meta, folder, address) for meta, folder in candidates[:limit])
    emit(dict(event="result", complete=complete))


def run(request, emit):
    need(type(request) is dict, "input")
    action = request.get("action")
    need(action in ("login", "fetch", "refresh"), "input")
    recipient = identity(request.get("email") if action == "login" else request.get("recipient", request.get("email", "")), stage="input")
    proxy = bounded_text(request.get("proxy_url", ""), 4096, stage="input")
    if proxy:
        parsed = urlsplit(proxy)
        need(parsed.scheme in ("http", "https", "socks5", "socks5h") and parsed.hostname and parsed.port
             and not parsed.query and not parsed.fragment and parsed.path in ("", "/"), "input")
    state = None
    if action != "login":
        need("password" not in request or not request["password"], "input")
        state = load_pickle(request.get("pkl", ""))
        validate_session(state, recipient)
    else:
        password = bounded_text(request.get("password", ""), 4096, False, stage="input")
    client, models = build_client(action, proxy)
    try:
        if action == "login":
            client.login(recipient, password, getter_2fa_code=block_manual, login_type=models.LoginType.WEB)
            need(client.user is not None and client.user.authenticated(), "auth")
            client.validate_keys(recipient)
            emit(dump_session(client, recipient))
            emit(dict(event="result", complete=True))
        else:
            restore_session(client, state, recipient, models)
            if action == "fetch":
                fetch(client, request, state, emit)
            else:
                uid = state["headers"]["x-pm-uid"]
                client._post("mail", "auth/refresh")
                event = dump_session(client, recipient)
                need(event["uid"] == uid, "refresh")
                emit(event)
                emit(dict(event="result", complete=True))
    finally:
        client.session.close()


def main():
    output = sys.stdout

    def emit(event):
        line = json.dumps(event, ensure_ascii=False, separators=(",", ":"))
        need(len(line.encode()) < MAX_LINE, "protocol")
        output.write(line + "\n")
        output.flush()

    try:
        # A corrupt compressed PGP packet must not exhaust the worker host.
        import resource
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
        resource.setrlimit(resource.RLIMIT_AS, (768 << 20, 768 << 20))
        resource.setrlimit(resource.RLIMIT_CPU, (900, 900))
        line = sys.stdin.buffer.readline(MAX_LINE + 1)
        need(0 < len(line) <= MAX_LINE and line.endswith(b"\n"), "input")
        request = json.loads(line)
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            run(request, emit)
        return 0
    except BridgeError as error:
        emit(error.event)
    except BaseException:
        emit(BridgeError("protocol").event)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
