import { describe, expect, it } from "vitest";
import { preprocessProtoImportContent } from "./proto-import-preprocess";

describe("Proto import preprocessing", () => {
  it("normalizes email and line endings without changing password whitespace", () => {
    expect(preprocessProtoImportContent("\uFEFF User@Example.com ----  secret  \r\n\n", "abort")).toEqual({
      content: "user@example.com----  secret  ", validCount: 1, skippedCount: 0,
    });
  });
  it("skips invalid and case-insensitive duplicate rows with accurate counts", () => {
    const result = preprocessProtoImportContent("a@example.com----one\nA@EXAMPLE.COM----two\nb@example.com----bad----extra\nc@example.com----ok", "skip");
    expect(result).toEqual({ content: "a@example.com----one\nc@example.com----ok", validCount: 2, skippedCount: 2 });
  });
  it("aborts without producing upload data and rejects empty, oversized and malformed credentials", () => {
    expect(preprocessProtoImportContent("a@example.com----one\na@example.com----two", "abort")).toMatchObject({
      content: "", validCount: 0, firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 },
    });
    for (const password of ["", "x".repeat(513), "bad\0value", "bad\rvalue", "\ud800"]) {
      expect(preprocessProtoImportContent("a@example.com----" + password, "abort").firstFailure?.category).toBe("invalid_format");
    }
    expect(preprocessProtoImportContent("a@example.com----p----client----token", "abort").firstFailure?.category).toBe("invalid_format");
  });
});
