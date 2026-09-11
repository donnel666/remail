import { describe, expect, it } from "vitest";
import { preprocessProtoImportContent } from "./proto-import-preprocess";

describe("Proto import preprocessing", () => {
  it("normalizes email and line endings without changing password whitespace", () => {
    expect(preprocessProtoImportContent("\uFEFF User@Proton.Me ----  secret  \r\n\n", "abort")).toEqual({
      content: "user@proton.me----  secret  ", validCount: 1, skippedCount: 0,
    });
  });
  it("skips invalid and case-insensitive duplicate rows with accurate counts", () => {
    const result = preprocessProtoImportContent("a@proton.me----one\nA@PROTON.ME----two\nb@proton.me----bad----extra\nc@proton.me----ok", "skip");
    expect(result).toEqual({ content: "a@proton.me----one\nc@proton.me----ok", validCount: 2, skippedCount: 2 });
  });
  it("mixes both formats and preserves password and PKL text", () => {
    const result = preprocessProtoImportContent(" A@Proton.Me ----  first password  \r\nB@proton.me----  second password  ---- \tAAEC/w== \t\r\n", "abort");
    expect(result).toEqual({
      content: "a@proton.me----  first password  \nb@proton.me----  second password  ---- \tAAEC/w== \t",
      validCount: 2, skippedCount: 0,
    });
  });
  it("accepts full addresses from both supported suffixes in both formats", () => {
    for (const suffix of ["protonmail.com", "proton.me"]) {
      for (const pkl of ["", "---- \tAAEC/w== \t"]) {
        expect(preprocessProtoImportContent(" User@" + suffix.toUpperCase() + " ----  password  " + pkl, "abort")).toEqual({
          content: "user@" + suffix + "----  password  " + pkl, validCount: 1, skippedCount: 0,
        });
      }
    }
  });
  it("rejects bare names and every unsupported domain in both formats", () => {
    for (const email of ["bare", "user@other.example", "user@proto.me", "user@pm.me", "user@sub.proton.me", "user@proton.me.evil", "user@protonmail.com.evil"]) {
      for (const pkl of ["", "----AA=="]) {
        expect(preprocessProtoImportContent(email + "----password" + pkl, "abort")).toEqual({
          content: "", validCount: 0, skippedCount: 0, firstFailure: { line: 1, category: "invalid_format" },
        });
      }
    }
  });
  it("skips unsupported addresses without completing or merging them into valid rows", () => {
    expect(preprocessProtoImportContent("user----bare password\nuser@proton.me----first\nuser@protonmail.com----second----AA==\nuser@other.example----rejected", "skip")).toEqual({
      content: "user@proton.me----first\nuser@protonmail.com----second----AA==", validCount: 2, skippedCount: 2,
    });
  });
  it("deduplicates full emails case-insensitively while keeping the two suffixes distinct", () => {
    const content = "User@proton.me----first----AA==\nUSER@PROTON.ME----second\nuser@protonmail.com----third";
    expect(preprocessProtoImportContent(content, "skip")).toEqual({
      content: "user@proton.me----first----AA==\nuser@protonmail.com----third", validCount: 2, skippedCount: 1,
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
      expect(preprocessProtoImportContent("a@proton.me----password----" + pkl, "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
    }
    for (const pkl of ["AA==", "AAA=", "AAAA", "+/8="]) {
      expect(preprocessProtoImportContent("a@proton.me----password----" + pkl, "abort").validCount).toBe(1);
    }
    expect(preprocessProtoImportContent("a@proton.me----password----AA==----extra", "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
  });
  it("accepts exactly 8 MiB and rejects one decoded byte more without large character arrays", () => {
    const maxBytes = 8 * 1024 * 1024;
    const exact = "A".repeat(Math.floor(maxBytes / 3) * 4) + "AAA=";
    expect(preprocessProtoImportContent("a@proton.me----password----" + exact, "abort").validCount).toBe(1);
    // The encodings have the same character count; padding determines decoded size.
    const oversized = "A".repeat(exact.length);
    expect(preprocessProtoImportContent("a@proton.me----password----" + oversized, "abort").firstFailure).toEqual({ line: 1, category: "invalid_format" });
  });
  it("deduplicates emails across both formats with unchanged skip and abort behavior", () => {
    const content = "a@proton.me----first----AA==\nA@PROTON.ME----second\nb@proton.me----third\nB@PROTON.ME----fourth----AAA=";
    expect(preprocessProtoImportContent(content, "skip")).toEqual({
      content: "a@proton.me----first----AA==\nb@proton.me----third", validCount: 2, skippedCount: 2,
    });
    expect(preprocessProtoImportContent(content, "abort")).toEqual({
      content: "", validCount: 0, skippedCount: 0,
      firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 },
    });
  });
  it("keeps malformed PKL and credentials out of failure metadata", () => {
    const result = preprocessProtoImportContent("private@proton.me----sensitive password----sensitive PKL!", "abort");
    expect(result).toEqual({ content: "", validCount: 0, skippedCount: 0, firstFailure: { line: 1, category: "invalid_format" } });
  });
  it("aborts without producing upload data and rejects empty, oversized and malformed credentials", () => {
    expect(preprocessProtoImportContent("a@proton.me----one\na@proton.me----two", "abort")).toMatchObject({
      content: "", validCount: 0, firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 },
    });
    for (const password of ["", "x".repeat(513), "bad\0value", "bad\rvalue", "\ud800"]) {
      expect(preprocessProtoImportContent("a@proton.me----" + password, "abort").firstFailure?.category).toBe("invalid_format");
    }
    expect(preprocessProtoImportContent("a@proton.me----p----client----token", "abort").firstFailure?.category).toBe("invalid_format");
  });
});
