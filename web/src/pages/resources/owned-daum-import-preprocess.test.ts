import { describe, expect, it } from "vitest";
import { preprocessOwnedDaumImport } from "./owned-daum-import-preprocess";

describe("owned Daum import preprocessing", () => {
  it("preserves the three credential formats and independent auxiliary metadata", () => {
    const content = "\ufeff USER@daum.net ----  app  \r\nsecond@daum.net---- login ---- app2 \r\nthird@daum.net----login----app---- https://remail.example/v1/pickup?email=helper%40outlook.com&token=synthetic ----ACCOUNT@kakao.example ";
    const result = preprocessOwnedDaumImport(content, "abort");
    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(3);
    expect(result.content.split("\n")).toEqual([
      "user@daum.net----  app  ", "second@daum.net---- login ---- app2 ",
      "third@daum.net----login----app----https://remail.example/v1/pickup?email=helper%40outlook.com&token=synthetic----account@kakao.example",
    ]);
  });

  it.each(["hanmail.net", "hanmail2.net", "outlook.com"])("rejects %s before upload without echoing the address in diagnostics", (suffix) => {
    const result = preprocessOwnedDaumImport(`user@${suffix}----app`, "abort");
    expect(result).toMatchObject({ content: "", validCount: 0, firstFailure: { line: 1, category: "non_daum_domain" } });
    expect(JSON.stringify(result)).not.toContain(suffix);
  });

  it("skips invalid and duplicate rows while abort rejects the entire input", () => {
    const content = "user@daum.net----app\nUSER@daum.net----other\ninvalid\nlast@daum.net----login----app";
    expect(preprocessOwnedDaumImport(content, "skip")).toEqual({ content: "user@daum.net----app\nlast@daum.net----login----app", validCount: 2, skippedCount: 2 });
    expect(preprocessOwnedDaumImport(content, "abort")).toMatchObject({ content: "", validCount: 0, firstFailure: { line: 2, category: "duplicate_email", firstLine: 1 } });
  });

  it("rejects ambiguous URLs, empty credentials and oversized addresses", () => {
    for (const content of [
      "user@daum.net----", "user@daum.net----login----app----url",
      "user@daum.net----login----app----https://remail.example/pickup?email=aux%40example.com&token=x&token=y----kakao@example.com",
      `${"a".repeat(246)}@daum.net----app`,
    ]) expect(preprocessOwnedDaumImport(content, "abort").firstFailure?.category).toBe("invalid_format");
  });
});
