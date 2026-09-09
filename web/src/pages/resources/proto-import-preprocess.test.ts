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
  it("mixes both formats and preserves password and PKL text", () => {
    const result = preprocessProtoImportContent(" A@Example.com ----  first password  \r\nB@example.com----  second password  ---- \tAAEC/w== \t\r\n", "abort");
    expect(result).toEqual({
      content: "a@example.com----  first password  \nb@example.com----  second password  ---- \tAAEC/w== \t",
      validCount: 2, skippedCount: 0,
    });
  });
  it("completes bare accounts in both formats without changing full-email domains or secrets", () => {
    const result = preprocessProtoImportContent(" First ----  first password  \r\nSecond----  second password  ---- \tAAEC/w== \t\r\nFull@Other.Example----  third password  ----AAA=\nexplicit@proto.me----last", "abort");
    expect(result).toEqual({
      content: "first@proton.me----  first password  \nsecond@proton.me----  second password  ---- \tAAEC/w== \t\nfull@other.example----  third password  ----AAA=\nexplicit@proto.me----last",
      validCount: 4, skippedCount: 0,
    });
  });
  it("deduplicates a bare account against its completed email but not a different domain", () => {
    const content = "User----first----AA==\nUSER@PROTON.ME----second\nuser@other.example----third";
    expect(preprocessProtoImportContent(content, "skip")).toEqual({
      content: "user@proton.me----first----AA==\nuser@other.example----third", validCount: 2, skippedCount: 1,
    });
    expect(preprocessProtoImportContent(content, "abort")).toEqual({
      content: "", validCount: 0, skippedCount: 0,
      firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 },
    });
  });
  it("does not turn malformed rows into displayed email addresses", () => {
    for (const content of ["private-account-fragment", "----password", "bare account----password", "bad@----password", "bad@@host----password", "bare----private password----private PKL!", "bare----password----AA==----private extra"]) {
      expect(preprocessProtoImportContent(content, "abort")).toEqual({
        content: "", validCount: 0, skippedCount: 0, firstFailure: { line: 1, category: "invalid_format" },
      });
    }
  });
  it("requires nonempty canonical standard Base64 with no internal whitespace", () => {
    for (const pkl of ["", " \t ", "A", "AA", "AAA", "====", "A===", "AA=A", "AA==AAAA", "AAAA=", "AB==", "AAB=", "AA-_", "AA A", "AA\tA", "AA\rA", "AA\0A", "AA\ud800A"]) {
      expect(preprocessProtoImportContent("a@example.com----password----" + pkl, "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
    }
    for (const pkl of ["AA==", "AAA=", "AAAA", "+/8="]) {
      expect(preprocessProtoImportContent("a@example.com----password----" + pkl, "abort").validCount).toBe(1);
    }
    expect(preprocessProtoImportContent("a@example.com----password----AA==----extra", "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
  });
  it("accepts exactly 8 MiB and rejects one decoded byte more without large character arrays", () => {
    const maxBytes = 8 * 1024 * 1024;
    const exact = "A".repeat(Math.floor(maxBytes / 3) * 4) + "AAA=";
    expect(preprocessProtoImportContent("a@example.com----password----" + exact, "abort").validCount).toBe(1);
    // The encodings have the same character count; padding determines decoded size.
    const oversized = "A".repeat(exact.length);
    expect(preprocessProtoImportContent("a@example.com----password----" + oversized, "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
  });
  it("deduplicates emails across both formats with unchanged skip and abort behavior", () => {
    const content = "a@example.com----first----AA==\nA@EXAMPLE.COM----second\nb@example.com----third\nB@EXAMPLE.COM----fourth----AAA=";
    expect(preprocessProtoImportContent(content, "skip")).toEqual({
      content: "a@example.com----first----AA==\nb@example.com----third", validCount: 2, skippedCount: 2,
    });
    expect(preprocessProtoImportContent(content, "abort")).toEqual({
      content: "", validCount: 0, skippedCount: 0,
      firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 },
    });
  });
  it("keeps malformed PKL and credentials out of failure metadata", () => {
    const result = preprocessProtoImportContent("private@example.com----sensitive password----sensitive PKL!", "abort");
    expect(result).toEqual({ content: "", validCount: 0, skippedCount: 0, firstFailure: { line: 1, category: "invalid_format" } });
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
