import { describe, expect, it } from "vitest";

import { preprocessMicrosoftImportContent } from "./microsoft-import-preprocess";

describe("preprocessMicrosoftImportContent", () => {
  it("preserves password whitespace while trimming other fields", () => {
    const result = preprocessMicrosoftImportContent(
      "  first@example.com----  password with spaces  \r\nsecond@example.com---- trailing ---- client ---- refresh ---- aux@example.com  ",
      "abort"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(2);
    expect(result.content).toBe(
      "first@example.com----  password with spaces  \nsecond@example.com---- trailing ----client----refresh----aux@example.com"
    );
  });

  it("skips pseudo binding fields created by delimiters inside credentials", () => {
    const result = preprocessMicrosoftImportContent(
      [
        "valid1@example.com----pass1----client1----refresh1",
        "invalid-three@example.com----pass2----not-an-email",
        `invalid-five@example.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----${"r".repeat(404)}`,
        "valid2@example.com----pass3",
      ].join("\n"),
      "skip"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(2);
    expect(result.skippedCount).toBe(2);
    expect(result.content).toBe(
      [
        "valid1@example.com----pass1----client1----refresh1",
        "valid2@example.com----pass3",
      ].join("\n")
    );
  });

  it("aborts on a pseudo binding field when requested", () => {
    const result = preprocessMicrosoftImportContent(
      `invalid@example.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----${"r".repeat(404)}`,
      "abort"
    );

    expect(result.content).toBe("");
    expect(result.validCount).toBe(0);
    expect(result.firstFailure).toMatchObject({
      line: 1,
      category: "invalid_format",
      email: "invalid@example.com",
    });
  });

  it("skips values that cannot fit the persistence schema", () => {
    const result = preprocessMicrosoftImportContent(
      [
        "invalid-email----password",
        `password-too-long@example.com----${"p".repeat(513)}`,
        `client-too-long@example.com----password----${"c".repeat(256)}----refresh`,
        `refresh-too-long@example.com----password----client----${"r".repeat(1025)}`,
        `binding-too-long@example.com----password----${"a".repeat(310)}@example.com`,
        "valid@example.com----password",
      ].join("\n"),
      "skip"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(1);
    expect(result.skippedCount).toBe(5);
    expect(result.content).toBe("valid@example.com----password");
  });
});
