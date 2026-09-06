from types import SimpleNamespace

from .workflow import structured_response_text


def test_structured_response_candidates_preserve_content_and_fail_closed():
    valid = '{"answer":"公开答复","usedEvidence":[],"seals":[]}'
    required = ("answer", "usedEvidence", "seals")

    def read(reasoning=valid, **updates):
        response = {
            "role": "assistant",
            "completion_text": "",
            "reasoning_content": reasoning,
            **updates,
        }
        return structured_response_text(
            SimpleNamespace(**response), required_keys=required
        )

    for reasoning in (valid, f"```json\n{valid}\n```", f" \n{valid}\n "):
        assert read(reasoning) == (valid, "reasoning_json")
    assert read(completion_text="broken JSON") == ("broken JSON", "content")
    assert read(completion_text="  answer  ", role="tool") == (
        "  answer  ",
        "content",
    )
    assert read(completion_text=None) == (valid, "reasoning_json")
    assert read(raw_completion={"choices": [{"finish_reason": "stop"}]}) == (
        valid,
        "reasoning_json",
    )
    assert read(raw_completion=SimpleNamespace(status="completed")) == (
        valid,
        "reasoning_json",
    )
    assert structured_response_text(
        {"role": "assistant", "reasoning_content": '{"answer":"ok","note":1}'},
        required_keys=("answer",),
        optional_keys=("note",),
    ) == ('{"answer":"ok","note":1}', "reasoning_json")
    for reasoning in (None, "", " \n "):
        assert read(reasoning) == ("", "empty_completion")
    for reasoning in (
        "thinking: " + valid,
        valid + "\nfinished",
        valid + valid,
        "```json\n" + valid + "\n```\n" + valid,
        "[" + valid + "]",
        '{"answer":"ok","usedEvidence":[]}',
        valid[:-1] + ',"extra":1}',
        valid[:-1] + ',"answer":"replacement"}',
        valid.replace('"公开答复"', '{"x":1,"x":2}'),
        *(valid.replace('"公开答复"', value) for value in ("NaN", "Infinity", "1e999")),
        "{" * 2000,
        42,
    ):
        assert read(reasoning) == ("", "invalid_reasoning_json")
    assert structured_response_text(
        {"role": "assistant", "reasoning_content": valid},
        required_keys=required,
        max_chars=len(valid) - 1,
    ) == ("", "invalid_reasoning_json")
    for updates in (
        {"role": "tool"},
        {"role": "err"},
        {"is_chunk": True},
        {"tools_call_name": ["lookup"]},
        {"tools_call_args": [{}]},
        {"tools_call_ids": ["call_1"]},
        {"finish_reason": "length"},
        {"raw_completion": {"object": "chat.completion.chunk"}},
        {"raw_completion": {"status": "incomplete"}},
        {"raw_completion": {"status": "failed"}},
        {"raw_completion": {"error": {"code": "error"}}},
        {"raw_completion": {"incomplete_details": {"reason": "max_output_tokens"}}},
        {"raw_completion": {"choices": [{"finish_reason": "content_filter"}]}},
        {"raw_completion": {"choices": [{"message": {"tool_calls": [{}]}}]}},
        {
            "raw_completion": {
                "choices": [{"message": {"function_call": {"name": "x"}}}]
            }
        },
        {"raw_completion": {"choices": [{"message": {"refusal": "refused"}}]}},
        {"raw_completion": {"choices": [{}, {}]}},
        {"raw_completion": {"output": [{"type": "function_call"}]}},
        {"raw_completion": {"output": [{"type": "message", "status": "incomplete"}]}},
    ):
        assert read(**updates) == ("", "ineligible_reasoning")
