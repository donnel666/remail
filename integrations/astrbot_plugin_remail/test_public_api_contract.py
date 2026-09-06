import json

from .persona import build_critic_payload
from .sources import (
    PublicAPIContract,
    api_example_urls,
    evidence_block,
    evidence_text,
    public_api_contract,
)


def test_public_contract_preserves_schema_but_does_not_promote_plain_text():
    contract = {
        "sourceValid": True,
        "servers": [{"url": "https://api.example.test"}],
        "operations": [
            {
                "method": "GET",
                "path": "/v1/open/orders/{orderNo}",
                "parameters": [
                    {
                        "name": "limit",
                        "in": "query",
                        "schema": {"type": "integer", "maximum": 100},
                    },
                    {
                        "name": "token",
                        "in": "query",
                        "schema": {"type": "string"},
                        "examples": {
                            "demo": {
                                "summary": "示意凭证",
                                "value": "synthetic-token-value",
                            }
                        },
                    },
                ],
            }
        ],
        "components": {
            "schemas": {
                "APIKeyProfile": {
                    "type": "object",
                    "properties": {
                        "apiKey": {
                            "type": "string",
                            "description": "API Key 只在自己的客户端使用。",
                        },
                        "code": {"type": "string", "pattern": "^[０-９]+$"},
                        "emailSuffix": {
                            "type": "string",
                            "description": "gmail_variant 选择谷歌变种商品。",
                        },
                        "password": {
                            "type": "string",
                            "example": "synthetic-private-value",
                        },
                        "otp": {"type": "integer", "example": 765432},
                        "verificationCode": {"type": "integer", "default": 654321},
                        "limit": {"type": "integer", "default": 10000},
                        "type": {"type": "string", "enum": ["gmail_variant"]},
                    },
                }
            }
        },
    }
    cleaned = public_api_contract(json.dumps(contract, ensure_ascii=False))
    props = json.loads(cleaned)["components"]["schemas"]["APIKeyProfile"]["properties"]
    assert props["code"]["pattern"] == "^[０-９]+$"
    assert (
        props["apiKey"]
        == contract["components"]["schemas"]["APIKeyProfile"]["properties"]["apiKey"]
    )
    assert "synthetic-private-value" not in cleaned
    assert "synthetic-token-value" not in cleaned and "示意凭证" in cleaned
    assert "765432" not in cleaned and "654321" not in cleaned
    assert props["limit"]["default"] == 10000
    block = evidence_block("api_documentation", cleaned)
    assert isinstance(block, PublicAPIContract)
    payload = build_critic_payload(
        question="如何下单？",
        candidate_answer="按公开契约填写参数。",
        evidence={"api": block},
    )
    assert json.loads(evidence_text(payload.evidence[0][1])) == json.loads(cleaned)
    allowed = "https://api.example.test/v1/open/orders/<ORDER_NO>?limit=10"
    assert api_example_urls(allowed, [block]) == (allowed,)
    assert api_example_urls(allowed, [str(block)]) == ()
    for unknown in (
        "https://evil.example.test/v1/open/orders/123?limit=10",
        "https://api.example.test/v1/private/123",
        "https://api.example.test/v1/open/orders/123?admin=true",
        "https://api.example.test/v1/open/orders/123?admin=",
        "https://api.example.test/v1/open/orders/123?admin",
        "https://api.example.test/v1/open/orders/123#secret",
    ):
        assert api_example_urls(unknown, [block]) == ()
