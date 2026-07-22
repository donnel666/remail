const ALLOWED_REGISTRATION_DOMAINS = new Set([
  "qq.com",
  "foxmail.com",
  "gmail.com",
  "proton.me",
  "protonmail.com",
  "pm.me",
  "mail.com",
]);

/** Local part: letters/digits only. Domain must be on the supported free-mail list. */
export function validateRegistrationEmail(email: string): string | null {
  const normalized = email.trim().toLowerCase();
  const at = normalized.lastIndexOf("@");
  if (at <= 0 || at === normalized.length - 1) {
    return "Email local part must contain only letters and digits.";
  }
  const local = normalized.slice(0, at);
  const host = normalized.slice(at + 1);
  if (!local || !host || host.includes(" ")) {
    return "Email local part must contain only letters and digits.";
  }
  if (!/^[a-z0-9]+$/i.test(local)) {
    return "Email local part must contain only letters and digits.";
  }
  if (!ALLOWED_REGISTRATION_DOMAINS.has(host)) {
    return "Registration with this email domain is not allowed.";
  }
  return null;
}
