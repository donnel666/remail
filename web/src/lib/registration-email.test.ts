import { describe, expect, it } from "vitest";
import { validateRegistrationEmail } from "./registration-email";

describe("validateRegistrationEmail", () => {
  it("allows only alphanumeric local parts on exact supported domains", () => {
    for (const domain of [
      "qq.com",
      "foxmail.com",
      "gmail.com",
      "proton.me",
      "protonmail.com",
      "pm.me",
      "mail.com",
    ]) {
      expect(validateRegistrationEmail(`user@${domain}`)).toBeNull();
    }
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
    expect(validateRegistrationEmail("user@example.com")).toBe(
      "Registration with this email domain is not allowed."
    );
    expect(validateRegistrationEmail("user@google.com")).toBe(
      "Registration with this email domain is not allowed."
    );
    expect(validateRegistrationEmail("user@sub.qq.com")).toBe(
      "Registration with this email domain is not allowed."
    );
    expect(validateRegistrationEmail("user@qq.com.")).toBe(
      "Registration with this email domain is not allowed."
    );
  });
});
