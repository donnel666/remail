import { describe, expect, it } from "vitest";

import { preprocessMicrosoftImportContent } from "./microsoft-import-preprocess";

describe("preprocessMicrosoftImportContent", () => {
  it("preserves password whitespace while trimming other fields", () => {
    const result = preprocessMicrosoftImportContent(
      "  first@outlook.com----  password with spaces  \r\nsecond@hotmail.com---- trailing ---- client ---- refresh ---- aux@example.com  ",
      "abort"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(2);
    expect(result.content).toBe(
      "first@outlook.com----  password with spaces  \nsecond@hotmail.com---- trailing ----client----refresh----aux@example.com"
    );
  });

  it("skips pseudo binding fields created by delimiters inside credentials", () => {
    const result = preprocessMicrosoftImportContent(
      [
        "valid1@outlook.com----pass1----client1----refresh1",
        "invalid-three@outlook.com----pass2----not-an-email",
        `invalid-five@outlook.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----${"r".repeat(404)}`,
        "valid2@outlook.fr----pass3",
      ].join("\n"),
      "skip"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(2);
    expect(result.skippedCount).toBe(2);
    expect(result.content).toBe(
      [
        "valid1@outlook.com----pass1----client1----refresh1",
        "valid2@outlook.fr----pass3",
      ].join("\n")
    );
  });

  it("aborts on a pseudo binding field when requested", () => {
    const result = preprocessMicrosoftImportContent(
      `invalid@outlook.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----${"r".repeat(404)}`,
      "abort"
    );

    expect(result.content).toBe("");
    expect(result.validCount).toBe(0);
    expect(result.firstFailure).toMatchObject({
      line: 1,
      category: "invalid_format",
      email: "invalid@outlook.com",
    });
  });

  it("skips values that cannot fit the persistence schema", () => {
    const result = preprocessMicrosoftImportContent(
      [
        "invalid-email----password",
        `password-too-long@outlook.com----${"p".repeat(513)}`,
        `client-too-long@outlook.com----password----${"c".repeat(256)}----refresh`,
        `refresh-too-long@outlook.com----password----client----${"r".repeat(1025)}`,
        `binding-too-long@outlook.com----password----${"a".repeat(310)}@example.com`,
        "valid@outlook.com----password",
      ].join("\n"),
      "skip"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(1);
    expect(result.skippedCount).toBe(5);
    expect(result.content).toBe("valid@outlook.com----password");
  });

  it("skips non-Microsoft primary domains but accepts any binding address", () => {
    const result = preprocessMicrosoftImportContent(
      [
        "keep@outlook.com----pw----recovery@icloud.com", // outlook + non-MS binding: kept
        "keep2@outlook.co.th----pw", // whitelisted country variant: kept
        "keep3@hotmail.com----pw", // exact hotmail.com: kept
        "drop@icloud.com----pw", // non-MS primary: skipped
        "drop2@alumni.sysu.edu.cn----pw", // non-MS primary: skipped
        "drop3@hotmail.co.uk----pw", // excluded hotmail variant: skipped
        "drop4@live.com----pw", // excluded live.com: skipped
        "drop5@outlook.co.uk----pw", // real outlook variant NOT in the 32-list: skipped
      ].join("\n"),
      "skip"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(3);
    expect(result.skippedCount).toBe(5);
    expect(result.content).toBe(
      [
        "keep@outlook.com----pw----recovery@icloud.com",
        "keep2@outlook.co.th----pw",
        "keep3@hotmail.com----pw",
      ].join("\n")
    );
  });

  it("reports a non_microsoft_domain failure on abort", () => {
    const result = preprocessMicrosoftImportContent(
      "nope@icloud.com----pw",
      "abort"
    );

    expect(result.content).toBe("");
    expect(result.validCount).toBe(0);
    expect(result.firstFailure).toMatchObject({
      line: 1,
      category: "non_microsoft_domain",
      email: "nope@icloud.com",
    });
  });
});
