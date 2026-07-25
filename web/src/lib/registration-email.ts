/** Local part is checked client-side; the runtime domain whitelist lives on the server. */
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
  return null;
}
