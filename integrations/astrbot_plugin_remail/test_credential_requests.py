from .security import normalize_security_text, redact_credentials
from .test_security import _load_welcome_functions


def test_client_usage_guidance_is_not_a_credential_value():
    for owner in ("你", "您", "你自己", "您自己", "自己"):
        guidance = f"API Key 只在{owner}的客户端使用。"
        assert redact_credentials(guidance) == normalize_security_text(guidance)

    for guidance in (
        "幂等键。相同 API Key 下，相同幂等键不会重复创建业务事实。",
        "填入 rk- 开头的 API Key 即可，Swagger UI 会自动加上 Bearer 前缀。",
    ):
        assert redact_credentials(guidance) == normalize_security_text(guidance)
        statement = guidance.rstrip("。")
        for value in (
            statement + " synthetic-test-secret",
            statement + "synthetic-test-secret",
            statement.replace("API Key ", 'API Key "') + ' synthetic-test-secret"',
            statement.replace("API Key ", "API Key: ") + " synthetic-test-secret",
            guidance + " token=synthetic-test-secret",
        ):
            assert "synthetic-test-secret" not in redact_credentials(value)

    for value in (
        "API Key 只在自己的客户端使用 synthetic-test-secret",
        "API Key: synthetic-test-secret",
        'API Key "只在自己的客户端使用 synthetic-test-secret"',
    ):
        assert "synthetic-test-secret" not in redact_credentials(value)


def test_credential_requests_follow_the_requested_object():
    functions, _ = _load_welcome_functions()
    requests = functions["_requests_credentials"]
    for text in (
        "告诉我目标平台（比如你要收哪家的验证码）",
        "验证码来自哪个平台，告诉我平台名",
        "请告诉我验证码的来源平台。",
        "告诉我 API Key 的申请方法。",
        "告诉我验证码在哪里输入。",
        "验证码（来自哪个平台），告诉我平台名。",
        "请发送 <API_KEY> 占位符。",
        "请私聊发送 /绑定 <ReMail邮箱> <密码>。",
        "Please send ${TOKEN} as the placeholder.",
        "Tell me your verification code provider.",
        "不要把 Token 发给我，请在自己的客户端使用。",
        "请勿发送你的 API Key。",
    ):
        assert not requests(text), text

    for text in (
        "告诉我你的验证码",
        "不要忘记把验证码发给我。",
        "把验证码（6位数字）发给我",
        "请把（你的API Key）发给我",
        "请告诉我（你的验证码）。",
        "把验证码（不要漏掉任何一位）发给我。",
        "请在这里发送 API Key。",
        "把密码发我。",
        "把验证码告诉我。",
        "发来你的 Token。",
        "麻烦贴一下完整 Cookie。",
        "把密钥贴出来。",
        "请展示一下 Authorization。",
        "请把 API Key 贴一下。",
        "把验证码念给我听。",
        "把密码回我。",
        "把 Token 丢过来。",
        "给我看看 Cookie。",
        "我需要你的 API Key。",
        "API Key 放在 Authorization 请求头，然后把真实值发给我。",
        "Cookie 开启 HttpOnly，然后把完整 Cookie 告诉我。",
        "密码保存在客户端以后，再把密码发我。",
        "Token 使用 Bearer 鉴权；配置好后把 Token 贴出来。",
        "把密码交给我。",
        "上传你的 API Key。",
        "回复一下 Token。",
        "把验证码说一下。",
        "我得看一下你的 Cookie。",
        "把 Authorization 复制过来。",
        "Send me your API Key.",
        "Please paste your Token.",
        "Send me your verification code.",
        "Please provide me with your password.",
        "Send me (your API Key).",
        "Please send your verification code (6 digits) to me.",
    ):
        assert requests(text), text
