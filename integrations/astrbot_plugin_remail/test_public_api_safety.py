from .security import contains_credentials, normalize_security_text, redact_credentials


def test_public_api_examples_keep_placeholders_but_redact_actual_credentials():
    # The 0.7.6 API repair was valid JSON but these examples triggered its hard gate.
    public_examples = (
        "Authorization: Bearer <YOUR_API_KEY>",
        "curl -H 'Authorization: Bearer <YOUR_API_KEY>' \\\n  https://example.com/v1/open/orders",
        'curl -H "Authorization: Bearer <YOUR_API_KEY>" \\\n  https://example.com/v1/open/orders',
        "--data-urlencode 'token=<SERVICE_TOKEN>'",
        "token=${SERVICE_TOKEN}",
        "Authorization: Bearer <你的API Key>",
        "Authorization: Bearer rk-...",
        "curl -H 'Authorization: Bearer rk-...' \\\n  https://example.com",
        "`Authorization: Bearer <YOUR_API_KEY>`",
        "**Authorization: Bearer <YOUR_API_KEY>**",
        "Bearer `<SERVICE_TOKEN>`",
        "Bearer '<SERVICE_TOKEN>'",
        "Token: `<SERVICE_TOKEN>`",
        "API Key: `<YOUR_API_KEY>`",
        "API Key 只在你自己的客户端中使用，不要发到聊天中。",
        "**取件接口使用邮箱和服务凭证，不使用账号 API Key：**",
        "**API Key:**\n请在本地配置。",
        "`API Key:`",
    )
    for text in public_examples:
        assert redact_credentials(text) == normalize_security_text(text), text
        assert not contains_credentials(text), text

    for text, secret in (
        ("Authorization: Bearer real-secret-value", "real-secret-value"),
        (
            "curl -H 'Authorization: Bearer real-secret-value' \\\n  https://example.com",
            "real-secret-value",
        ),
        ("token=<ATTACKER_SECRET>", "ATTACKER_SECRET"),
        ("token=<SERVICE_TOKEN=real-secret>", "real-secret"),
        ("token=<SERVICE_TOKEN>real-secret", "real-secret"),
        ("Bearer <SERVICE_TOKEN>real-secret", "real-secret"),
        ("API Key: <你的真实密钥>", "你的真实密钥"),
        ("Token: `real-secret-value`", "real-secret-value"),
        ("Bearer `real-secret-value`", "real-secret-value"),
        ("Bearer 'real-secret-value'", "real-secret-value"),
        ("Bearer 'real-secret-value", "real-secret-value"),
        ("**Authorization: Bearer real-secret-value**", "real-secret-value"),
        ("token=rk-actual-secret-value...", "rk-actual-secret-value"),
        ("rk-12345678901234567890", "rk-12345678901234567890"),
        ("password=**", "password=**"),
        ("**password:** actual-secret", "actual-secret"),
        ("API Key 只在你自己的客户端中使用 real-secret-value", "real-secret-value"),
    ):
        assert contains_credentials(text), text
        assert secret not in redact_credentials(text), text
