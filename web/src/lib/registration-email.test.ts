import { describe, expect, it } from "vitest";
import { validateRegistrationEmail } from "./registration-email";

describe("validateRegistrationEmail", () => {
  it("allows only alphanumeric local parts and blocks exact domains", () => {
    expect(validateRegistrationEmail("user.name@example.com")).toBe(
      "Email local part must contain only letters and digits."
    );
    expect(validateRegistrationEmail("user@qq.com.")).toBe(
      "Registration with this email domain is not allowed."
    );
    expect(validateRegistrationEmail("user@sub.qq.com")).toBeNull();
  });
});
