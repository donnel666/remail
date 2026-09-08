"""Offline checks; python -B -m unittest discover -s this-directory -p '*_test.py'."""

import base64
import copy
from email.message import EmailMessage
from email import policy
import importlib.util
import io
import json
from pathlib import Path
import pickle
import re
import subprocess
import sys
import types
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("pkl_bridge", Path(__file__).with_name("pkl_bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


def state():
    def pair(user):
        return {"is_primary": True, "is_user_key": user, "fingerprint_public": None,
                "fingerprint_private": "A" * 40, "public_key": None,
                "private_key": "-----BEGIN PGP PRIVATE KEY BLOCK-----\nfixture\n-----END PGP PRIVATE KEY BLOCK-----",
                "passphrase": "offline-only", "email": "one@example.com"}
    return {"pgp": {"pairs_keys": [pair(True), pair(False)], "aes256_keys": {}},
            "account_addresses": [{"id": "addr", "email": "one@example.com", "name": ""}],
            "headers": {"authorization": "Bearer offline-only", "x-pm-uid": "offline-uid"},
            "cookies": {"AUTH-offline-uid": "offline-cookie"}}


def encode(value):
    return base64.b64encode(pickle.dumps(value, protocol=4)).decode()


def meta(mid="m1", timestamp=100, recipients=None, **kwargs):
    return dict(ID=mid, AddressID="addr", Time=timestamp, ToList=recipients if recipients is not None else [{"Address": "one@example.com", "Name": ""}], **kwargs)


class FakeClient:
    def __init__(self, pages, details=None):
        self.pages, self.details = pages, details or {}
        self.reads, self.queries = [], []

    def _get(self, base, endpoint, params):
        self.queries.append((base, endpoint, params))
        return types.SimpleNamespace(json=lambda: self.pages.get((params["LabelID"], params["Page"]), {"Messages": []}))

    def read_message(self, mid, mark_as_read):
        self.reads.append((mid, mark_as_read))
        return dict(self.details.get(mid, meta(mid)), Body="body", Subject="subject")


class PickleTests(unittest.TestCase):
    def test_round_trip_restricted_schema_and_identity(self):
        value = bridge.load_pickle(encode(state()))
        self.assertEqual(value, state())
        bridge.validate_session(value, "ONE@example.com")
        with self.assertRaises(bridge.BridgeError):
            bridge.validate_session(value, "two@example.com")

    def test_global_reduce_persistent_trailing_and_recursive_pickle_rejected(self):
        class Execute:
            def __reduce__(self):
                return (eval, ("1 + 1",))
        cyclic = []
        cyclic.append(cyclic)
        candidates = [encode(Execute()), encode(cyclic), base64.b64encode(pickle.dumps(state()) + b"trailing").decode(),
                      base64.b64encode(b"Pforbidden\n.").decode(), "!!!"]
        for candidate in candidates:
            with self.subTest(candidate_type=type(candidate).__name__), self.assertRaises(bridge.BridgeError):
                bridge.load_pickle(candidate)

    def test_schema_prevents_header_injection_nonprimitive_and_identity_alias(self):
        variants = []
        value = state(); value["headers"]["authorization"] = "Bearer x\r\nHost: evil"; variants.append(value)
        value = state(); value["headers"]["host"] = "evil.example"; variants.append(value)
        value = state(); value["pgp"]["pairs_keys"][1]["email"] = "one+alias@example.com"; variants.append(value)
        value = state(); value["account_addresses"].append(copy.deepcopy(value["account_addresses"][0])); variants.append(value)
        value = state(); value["pgp"]["aes256_keys"]["cache"] = 123; variants.append(value)
        for value in variants:
            with self.assertRaises(bridge.BridgeError):
                bridge.validate_session(value, "one@example.com")

    def test_fingerprint_changes_with_keys_not_cookie_tokens(self):
        value = state()
        def fake(value):
            return types.SimpleNamespace(
                pgp=types.SimpleNamespace(pairs_keys=[types.SimpleNamespace(to_dict=lambda p=p: p) for p in value["pgp"]["pairs_keys"]], aes256_keys={}),
                account_addresses=[types.SimpleNamespace(**a) for a in value["account_addresses"]],
                session=types.SimpleNamespace(headers=value["headers"], cookies=types.SimpleNamespace(get_dict=lambda: value["cookies"])))
        before = bridge.dump_session(fake(value), "one@example.com")
        value["cookies"]["AUTH-offline-uid"] = "rotated"
        value["headers"]["authorization"] = "Bearer rotated"
        after = bridge.dump_session(fake(value), "one@example.com")
        self.assertEqual(before["key_fingerprint"], after["key_fingerprint"])
        self.assertNotEqual(before["pkl"], after["pkl"])
        value["pgp"]["pairs_keys"][1]["passphrase"] = "changed"
        self.assertNotEqual(before["key_fingerprint"], bridge.dump_session(fake(value), "one@example.com")["key_fingerprint"])


class ProtocolTests(unittest.TestCase):
    def test_allowlist_denies_cross_action_sends_and_redirect_hosts(self):
        self.assertEqual(bridge.request_stage("fetch", "GET", "https://mail.proton.me/api/mail/v4/messages"), "list")
        self.assertEqual(bridge.request_stage("refresh", "POST", "https://mail.proton.me/api/auth/refresh"), "refresh")
        for action, method, url in [("fetch", "POST", "https://mail.proton.me/api/auth/refresh"),
                                    ("fetch", "PUT", "https://mail.proton.me/api/mail/v4/messages/read"),
                                    ("login", "POST", "https://account.proton.me/api/core/v4/auth/2fa"),
                                    ("fetch", "GET", "http://mail.proton.me/api/mail/v4/messages"),
                                    ("fetch", "GET", "https://evil.example/api/mail/v4/messages"),
                                    ("fetch", "GET", "https://mail.proton.me/api/mail/v4/messages/%2e%2e")]:
            with self.subTest(action=action, method=method), self.assertRaises(bridge.BridgeError):
                bridge.request_stage(action, method, url)

    def test_failure_categories_do_not_expose_original_response(self):
        for stage, status, code, action, expected in [("list", 401, 0, "fetch", "session_revoked"),
                                                     ("refresh", 422, 8002, "refresh", "session_revoked"),
                                                     ("auth", 422, 8002, "login", "invalid_credentials"),
                                                     ("auth", 422, 9001, "login", "action_required"),
                                                     ("list", 429, 0, "fetch", "rate_limited"),
                                                     ("auth", 429, 8002, "login", "rate_limited"),
                                                     ("auth", 503, 8002, "login", "request"),
                                                     ("list", 503, 8002, "fetch", "request"),
                                                     ("refresh", 408, 10013, "refresh", "request"),
                                                     ("auth_info", 422, 8002, "login", "request"),
                                                     ("auth_info", 422, 6003, "login", "invalid_credentials"),
                                                     ("auth", 500, 0, "login", "request")]:
            event = bridge.response_error(stage, status, code, action).event
            self.assertEqual(event["category"], expected)
            self.assertEqual(set(event), {"event", "stage", "category", "http_status", "api_code", "retryable", "proxy_failure"})

    def test_main_bad_input_only_safe_ndjson_and_no_stderr(self):
        result = subprocess.run([sys.executable, "-B", str(Path(__file__).with_name("pkl_bridge.py"))],
                                input=b'{"password":"must-never-echo"}\n', capture_output=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stderr, b"")
        self.assertNotIn(b"must-never-echo", result.stdout)
        self.assertEqual(json.loads(result.stdout)["event"], "error")


class OperationTests(unittest.TestCase):
    def fixture(self):
        initial = state()

        class Cookies(dict):
            def get_dict(self):
                return dict(self)

            def set(self, name, value):
                self[name] = value

        def pair(**values):
            return types.SimpleNamespace(**values, to_dict=lambda: values)

        models = types.SimpleNamespace(PgpPairKeys=pair, AccountAddress=types.SimpleNamespace,
                                       LoginType=types.SimpleNamespace(WEB="web"))
        client = FakeClient({})
        client.session = types.SimpleNamespace(headers={}, cookies=Cookies(), close=lambda: None)
        client.pgp = types.SimpleNamespace(pairs_keys=[], aes256_keys={})
        client.account_addresses = []
        client.user = types.SimpleNamespace(authenticated=lambda: True)
        client.validate_keys = lambda recipient: None
        client.logins = []

        def login(email, password, **kwargs):
            client.logins.append((email, password, kwargs["login_type"]))
            bridge.restore_session(client, initial, email, models)

        def post(base, endpoint):
            self.assertEqual((base, endpoint), ("mail", "auth/refresh"))
            client.session.cookies.set("AUTH-offline-uid", "rotated")

        client.login, client._post = login, post
        return client, models

    def test_login_emits_one_pickle_then_result_and_fetch_never_logs_in(self):
        client, models = self.fixture()
        events = []
        with patch.object(bridge, "build_client", return_value=(client, models)):
            bridge.run(dict(action="login", email="one@example.com", password="  exact password  "), events.append)
        self.assertEqual([e["event"] for e in events], ["session", "result"])
        self.assertEqual(client.logins, [("one@example.com", "  exact password  ", "web")])
        saved = events[0]
        client, models = self.fixture()
        events = []
        with patch.object(bridge, "build_client", return_value=(client, models)):
            bridge.run(dict(action="fetch", pkl=saved["pkl"], recipient="one@example.com"), events.append)
        self.assertFalse(client.logins)
        self.assertEqual(events, [{"event": "result", "complete": True}])

    def test_refresh_reuses_pickle_and_keeps_key_fingerprint(self):
        client, models = self.fixture()
        bridge.restore_session(client, state(), "one@example.com", models)
        before = bridge.dump_session(client, "one@example.com")
        events = []
        with patch.object(bridge, "build_client", return_value=(client, models)):
            bridge.run(dict(action="refresh", pkl=before["pkl"], recipient="one@example.com"), events.append)
        self.assertFalse(client.logins)
        self.assertEqual([e["event"] for e in events], ["session", "result"])
        self.assertEqual(before["key_fingerprint"], events[0]["key_fingerprint"])
        self.assertNotEqual(before["pkl"], events[0]["pkl"])

    def test_invalid_pickle_fails_before_constructing_network_client(self):
        with patch.object(bridge, "build_client") as build:
            with self.assertRaises(bridge.BridgeError):
                bridge.run(dict(action="fetch", pkl="invalid", recipient="one@example.com"), lambda event: None)
            build.assert_not_called()


class FetchTests(unittest.TestCase):
    def request(self, **kwargs):
        return dict(recipient="one@example.com", until_at="1970-01-01T00:05:00Z", **kwargs)

    def test_folder_merge_sort_exact_recipient_original_to_count(self):
        to = [{"Address": "one@example.com"}, {"Address": "second@example.com"}]
        client = FakeClient({("0", 0): {"Messages": [meta("older", 100, to), meta("alias", 200, [{"Address": "one+alias@example.com"}])]},
                             ("4", 0): {"Messages": [meta("newer", 150)]}},
                            {"older": meta("older", 100, to), "newer": meta("newer", 150)})
        events = []
        bridge.fetch(client, self.request(), state(), events.append)
        messages = [m for event in events if event["event"] == "messages" for m in event["messages"]]
        self.assertEqual([m["ID"] for m in messages], ["newer", "older"])
        self.assertEqual(messages[1]["OriginalToCount"], 2)
        self.assertEqual(messages[0]["Folder"], "Junk")
        self.assertEqual(messages[0]["ReceivedAt"], "1970-01-01T00:02:30Z")
        self.assertTrue(all(flag is False for _, flag in client.reads))
        self.assertEqual(events[-1], {"event": "result", "complete": True})

    def test_known_ids_and_limit_never_stop_full_history(self):
        pages = {("0", 0): {"Messages": [meta("new", 200), meta("known", 100)]}}
        normal, history = [], []
        bridge.fetch(FakeClient(pages), self.request(known_message_ids=["provider:proton:inbox:known"], max_messages=1), state(), normal.append)
        bridge.fetch(FakeClient(pages), self.request(known_message_ids=["provider:proton:inbox:known"], max_messages=1, full_history=True), state(), history.append)
        self.assertFalse(normal[-1]["complete"])
        self.assertTrue(history[-1]["complete"])
        self.assertEqual(sum(len(e.get("messages", [])) for e in history), 2)

    def test_incomplete_stale_repeated_and_wrong_detail_fail_not_complete(self):
        bad_pages = [{"Messages": [], "Total": 1}, {"Messages": [], "Stale": 1},
                     {"Messages": [meta(), meta()]}, {}]
        for response in bad_pages:
            events = []
            with self.assertRaises(bridge.BridgeError):
                bridge.fetch(FakeClient({("0", 0): response}), self.request(full_history=True), state(), events.append)
            self.assertFalse(any(e["event"] == "result" for e in events))
        client = FakeClient({("0", 0): {"Messages": [meta()]}}, {"m1": meta(recipients=[{"Address": "foreign@example.com"}])})
        with self.assertRaises(bridge.BridgeError):
            bridge.fetch(client, self.request(), state(), lambda event: None)

    def test_readable_mime_skips_attachment_subtrees_and_keeps_inline_rfc822(self):
        body = ('Content-Type: multipart/mixed; boundary=b\r\n\r\n'
                '--b\r\nContent-Type: message/rfc822\r\n\r\nContent-Type: text/plain\r\n\r\ninline body\r\n'
                '--b\r\nContent-Type: message/rfc822\r\nContent-Disposition: attachment; filename="hidden.eml"\r\n\r\n'
                'Content-Type: text/plain\r\n\r\nsecret attachment\r\n--b--\r\n')
        result = bridge.readable_body(body, "multipart/mixed")
        self.assertIn("inline body", result)
        self.assertNotIn("secret attachment", result)
        with self.assertRaises(bridge.BridgeError):
            bridge.readable_body('Content-Type: multipart/mixed; boundary=missing\r\n\r\nbroken', "multipart/mixed")

    def test_remote_bad_fields_are_not_misclassified_as_a_broken_pickle(self):
        address = {"id": "addr", "email": "one@example.com"}
        for field in ("Subject", "Header", "Body", "MIMEType", "ExternalID"):
            raw = dict(meta(), **{field: None})
            client = types.SimpleNamespace(read_message=lambda *args, value=raw, **kwargs: value)
            with self.subTest(field=field), self.assertRaises(bridge.BridgeError) as failure:
                bridge.message_record(client, meta(), "Inbox", address)
            self.assertEqual(failure.exception.event["stage"], "read")
        for item in ({"Name": None, "Address": "one@example.com"}, {"Name": "", "Address": None}):
            with self.assertRaises(bridge.BridgeError) as failure:
                bridge.belongs(meta(recipients=[item]), address, stage="list")
            self.assertEqual(failure.exception.event["stage"], "list")
        with self.assertRaises(bridge.BridgeError) as failure:
            bridge.known_message(dict(meta(), ExternalID=None), "Inbox", set())
        self.assertEqual(failure.exception.event["stage"], "list")


@unittest.skipIf(importlib.util.find_spec("pgpy") is None, "optional pinned bridge dependencies not installed")
class InstalledDependencyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from pgpy import PGPKey, PGPMessage, PGPUID
        from pgpy.constants import CompressionAlgorithm, HashAlgorithm, KeyFlags, PubKeyAlgorithm, SymmetricKeyAlgorithm
        client, models = bridge.build_client("fetch", "")
        client.session.close()
        PgpPairKeys = models.PgpPairKeys

        cls.passphrase = "offline protected key password"

        def key(name):
            value = PGPKey.new(PubKeyAlgorithm.RSAEncryptOrSign, 2048)
            value.add_uid(PGPUID.new(name, email="one@example.com"),
                          usage={KeyFlags.Sign, KeyFlags.EncryptCommunications, KeyFlags.EncryptStorage},
                          hashes=[HashAlgorithm.SHA256], ciphers=[SymmetricKeyAlgorithm.AES256],
                          compression=[CompressionAlgorithm.Uncompressed])
            value.protect(cls.passphrase, SymmetricKeyAlgorithm.AES256, HashAlgorithm.SHA256)
            return value

        cls.user_key = key("Offline User Key")
        cls.address_key = key("Offline Mailbox Key")
        cls.wrong_key = key("Wrong Mailbox Key")
        cls.crypto_state = state()
        cls.crypto_state["pgp"]["pairs_keys"] = [
            PgpPairKeys(is_primary=True, is_user_key=is_user,
                        fingerprint_public=str(value.fingerprint), fingerprint_private=str(value.fingerprint),
                        public_key=str(value.pubkey), private_key=str(value), passphrase=cls.passphrase,
                        email="one@example.com").to_dict()
            for value, is_user in ((cls.user_key, True), (cls.address_key, False))
        ]

        def encrypted(text):
            return cls.address_key.pubkey.encrypt(
                PGPMessage.new(text, compression=CompressionAlgorithm.Uncompressed),
                cipher=SymmetricKeyAlgorithm.AES256)

        cls.plain_body = "中文验证码 135790\nMultiple words stay intact."
        cls.plain_encrypted = str(encrypted(cls.plain_body))
        mime = EmailMessage(policy=policy.SMTP)
        mime.set_content("纯文本版本，不应覆盖 HTML 正文。", charset="utf-8")
        mime.add_alternative("<html><body>中文验证 <b>246810</b> Multiple words</body></html>", subtype="html", charset="utf-8")
        mime.add_attachment("This secret attachment must never become message body.", filename="hidden.txt")
        cls.mime_encrypted = str(encrypted(mime.as_string()))
        damaged = bytearray(bytes(encrypted(cls.plain_body)))
        damaged[-1] ^= 1  # Valid packet structure, but the encrypted MDC no longer verifies.
        cls.corrupt_encrypted = str(PGPMessage.from_blob(bytes(damaged)))
        cls.bad_mime_encrypted = str(encrypted("Content-Type: multipart/mixed; boundary=missing\r\n\r\nincomplete MIME"))

    def crypto_fetch(self, encoded_state=None, corrupt=None, full_history=True):
        """Only HTTP is mocked: real PKL parsing, SDK key restore, PGP and MIME run."""
        from curl_cffi import requests as curl_requests

        recipients = [{"Name": "One Display Name", "Address": "one@example.com"},
                      {"Name": "Second Reader Name", "Address": "second@example.com"}]
        messages = {
            "plain": dict(meta("plain", 100, recipients), Subject="中文 Subject with multiple words",
                          MIMEType="text/plain", Header="X-Fixture: multiple words\r\n",
                          ExternalID="<plain-fixture@example.test>",
                          Sender={"Name": "Example Sender Name", "Address": "sender@example.net"},
                          CCList=[{"Name": "Copy Reader Name", "Address": "copy@example.net"}],
                          Body=self.plain_encrypted),
            "mime": dict(meta("mime", 150), Subject="MIME 中文验证", MIMEType="multipart/mixed",
                         Sender={"Name": "MIME Sender Name", "Address": "sender@example.net"},
                         ExternalID="<mime-fixture@example.test>", Body=self.mime_encrypted),
        }
        if corrupt is not None:
            messages["mime"]["Body"] = corrupt
        calls = []

        def http(session, method, url, **kwargs):
            # These assertions also prove the fresh client has the PKL's actual
            # headers/cookies and cannot silently log in or mark a message read.
            self.assertEqual(method.upper(), "GET")
            self.assertEqual(session.headers["authorization"], "Bearer offline-only")
            self.assertEqual(session.headers["x-pm-uid"], "offline-uid")
            self.assertEqual(session.cookies.get_dict()["AUTH-offline-uid"], "offline-cookie")
            self.assertFalse(kwargs["allow_redirects"])
            self.assertTrue(kwargs["verify"])
            path = urlsplit(url).path
            calls.append(path)
            if path == "/api/mail/v4/messages":
                self.assertEqual(kwargs["params"]["AddressID"], "addr")
                label = kwargs["params"]["LabelID"]
                self.assertIn(label, ("0", "4"))
                ids = ("plain",) if label == "0" else ("mime",)
                data = {"Code": 1000, "Messages": [{k: v for k, v in messages[mid].items() if k != "Body"} for mid in ids], "Total": 1}
            else:
                self.assertIn(path, ("/api/mail/v4/messages/plain", "/api/mail/v4/messages/mime"))
                data = {"Code": 1000, "Message": messages[path.rsplit("/", 1)[1]]}
            body = json.dumps(data, ensure_ascii=False).encode()
            return types.SimpleNamespace(status_code=200, content=b"", close=lambda: None,
                                         iter_content=lambda: iter([body]), json=lambda: json.loads(body),
                                         cookies=types.SimpleNamespace(get_dict=lambda: {}))

        wire = io.StringIO()

        def emit(event):
            wire.write(json.dumps(event, ensure_ascii=False) + "\n")

        with patch.object(curl_requests.Session, "request", autospec=True, side_effect=http):
            try:
                bridge.run({"action": "fetch", "pkl": encoded_state or encode(self.crypto_state),
                            "recipient": "one@example.com", "until_at": "1970-01-01T00:05:00Z",
                            "full_history": full_history}, emit)
            except bridge.BridgeError as error:
                emit(error.event)
        return [json.loads(line) for line in wire.getvalue().splitlines()], calls

    def test_real_protected_keys_round_trip_pickle_decrypt_chinese_and_mime(self):
        self.assertTrue(self.address_key.is_protected)
        self.assertFalse(self.address_key.is_unlocked)
        encoded = encode(self.crypto_state)
        self.assertEqual(bridge.load_pickle(encoded), self.crypto_state)
        for full in (False, True):
            with self.subTest(full_history=full):
                events, calls = self.crypto_fetch(encoded, full_history=full)
                self.assertEqual(events[-1], {"event": "result", "complete": True})
                self.assertEqual(set(event["event"] for event in events), {"messages", "result"})
                records = {m["ID"]: m for event in events if event["event"] == "messages" for m in event["messages"]}
                self.assertEqual(records["plain"]["Body"], self.plain_body)
                self.assertEqual(records["plain"]["Subject"], "中文 Subject with multiple words")
                self.assertEqual(records["plain"]["Sender"]["Name"], "Example Sender Name")
                self.assertEqual(records["plain"]["ToList"][0]["Name"], "One Display Name")
                self.assertEqual(records["plain"]["OriginalToCount"], 2)
                self.assertEqual(records["plain"]["CCList"][0]["Address"], "copy@example.net")
                self.assertEqual(records["plain"]["ExternalID"], "<plain-fixture@example.test>")
                self.assertEqual(records["plain"]["Header"], "X-Fixture: multiple words\r\n")
                self.assertEqual(records["mime"]["Folder"], "Junk")
                self.assertIn("中文验证", records["mime"]["Subject"])
                self.assertIn("中文验证", records["mime"]["Body"])
                self.assertIn("246810", records["mime"]["Body"])
                self.assertNotIn("secret attachment", records["mime"]["Body"])
                self.assertNotIn("纯文本版本", records["mime"]["Body"])
                self.assertEqual(len(calls), 4)

    def test_real_wrong_private_key_and_passphrase_cannot_complete(self):
        for change in ({"private_key": str(self.wrong_key)}, {"passphrase": "incorrect passphrase"}):
            with self.subTest(changed_field=next(iter(change))):
                value = copy.deepcopy(self.crypto_state)
                value["pgp"]["pairs_keys"][1].update(change)
                events, _ = self.crypto_fetch(encode(value))
                self.assertEqual(events[-1]["event"], "error")
                self.assertEqual(events[-1]["category"], "decryption")
                self.assertFalse(any(event["event"] == "result" for event in events))

    def test_real_corrupt_ciphertext_and_decrypted_bad_mime_never_complete(self):
        for corrupt in (self.corrupt_encrypted, self.bad_mime_encrypted):
            with self.subTest(kind="ciphertext" if corrupt == self.corrupt_encrypted else "mime"):
                events, _ = self.crypto_fetch(corrupt=corrupt)
                self.assertEqual(events[0]["event"], "messages")
                self.assertEqual(events[0]["messages"][0]["Body"], self.plain_body)
                self.assertEqual(events[-1]["event"], "error")
                self.assertEqual(events[-1]["category"], "decryption")
                self.assertFalse(any(event["event"] == "result" for event in events))

    def test_pinned_modulus_signature_and_constant_time_server_proof(self):
        client, _ = bridge.build_client("login", "")
        self.addCleanup(client.session.close)
        source = Path(__file__).with_name("client_test.go").read_text()
        modulus = re.search(r"const signedTestModulus = `([^`]+)`", source).group(1)
        info = {"Version": 4, "Modulus": modulus, "Salt": base64.b64encode(b"0123456789").decode(),
                "ServerEphemeral": base64.b64encode((2).to_bytes(256, "little")).decode(), "SRPSession": "offline"}
        result = client._parse_info_before_login(info, "offline-fixture-password")
        self.assertEqual(len(result), 3)
        server_proof = base64.b64encode(client.user.expected_server_proof).decode()
        self.assertTrue(client._login_process({"ServerProof": server_proof, "TwoFactor": 0, "PasswordMode": 1}))
        with self.assertRaises(bridge.BridgeError):
            client._login_process({"ServerProof": base64.b64encode(b"bad").decode()})
        with self.assertRaises(bridge.BridgeError):
            client._parse_info_before_login(dict(info, Modulus=modulus.replace("W2z5", "X2z5", 1)), "offline-fixture-password")

    def test_html_401_is_intercepted_before_library_refresh(self):
        from curl_cffi import requests as curl_requests
        client, _ = bridge.build_client("fetch", "")
        self.addCleanup(client.session.close)
        response = types.SimpleNamespace(status_code=401, content=b"", close=lambda: None,
                                         iter_content=lambda: iter([b"not json"]),
                                         json=lambda: json.loads(b"not json"))
        with patch.object(curl_requests.Session, "request", return_value=response) as request:
            with self.assertRaises(bridge.BridgeError) as raised:
                client._get("mail", "mail/v4/messages")
            self.assertEqual(raised.exception.event["category"], "session_revoked")
            self.assertEqual(request.call_count, 1)


if __name__ == "__main__":
    unittest.main()
