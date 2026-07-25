import { describe, expect, it } from "vitest";
import { validateRegistrationEmail } from "./registration-email";

describe("validateRegistrationEmail", () => {
  it("leaves the runtime domain whitelist to the server", () => {
    expect(validateRegistrationEmail("1515445804@qq.com")).toBeNull();
    expect(validateRegistrationEmail("User@QQ.COM")).toBeNull();
    for (const email of [
      "first.last@gmail.com",
      "user_name@gmail.com",
      "user+tag@gmail.com",
    ]) {
      expect(validateRegistrationEmail(email)).toBe(
        "Email local part must contain only letters and digits."
      );
    }
    expect(validateRegistrationEmail("user@example.com")).toBeNull();
    expect(validateRegistrationEmail("user@sub.qq.com")).toBeNull();
  });
});
