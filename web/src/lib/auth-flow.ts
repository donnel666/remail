export const LOGIN_NOTICE_KEY = "remail-login-notice";
export const LOGIN_RETURN_TO_KEY = "remail-login-return-to";
export const AUTH_REQUIRED_EVENT = "remail-auth-required";
export const AUTH_REQUIRED_NOTICE = "Authentication is required.";

const authCookieNames = ["sid", "csrf_token"];
const cookieDeletePaths = ["/", "/v1"];
const authPagePrefixes = ["/login", "/register", "/activation"];
const loginReturnToTTLMS = 30 * 60 * 1000;

interface LoginReturnToPayload {
  createdAt: number;
  path: string;
}

function expireCookie(name: string) {
  if (typeof document === "undefined") return;

  for (const path of cookieDeletePaths) {
    document.cookie = `${name}=; Max-Age=0; path=${path}; SameSite=Lax`;
  }
}

export function clearBrowserAuthState(options: { notice?: boolean } = {}) {
  if (typeof window === "undefined") return;

  for (const cookieName of authCookieNames) {
    expireCookie(cookieName);
  }

  if (options.notice ?? false) {
    window.sessionStorage.setItem(LOGIN_NOTICE_KEY, AUTH_REQUIRED_NOTICE);
  }
}

function isSafeReturnPath(path: string) {
  if (!path.startsWith("/") || path.startsWith("//")) return false;
  return !authPagePrefixes.some(
    (prefix) =>
      path === prefix ||
      path.startsWith(`${prefix}?`) ||
      path.startsWith(`${prefix}#`)
  );
}

function parseLoginReturnToPayload(value: string | null) {
  if (!value) return null;

  try {
    const payload = JSON.parse(value) as Partial<LoginReturnToPayload>;
    if (
      typeof payload.path !== "string" ||
      typeof payload.createdAt !== "number"
    ) {
      return null;
    }
    return payload as LoginReturnToPayload;
  } catch {
    return null;
  }
}

export function currentLoginReturnPath() {
  if (typeof window === "undefined") return "";
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

export function storeLoginReturnTo(path = currentLoginReturnPath()) {
  if (typeof window === "undefined") return;
  if (!isSafeReturnPath(path)) return;
  window.sessionStorage.setItem(
    LOGIN_RETURN_TO_KEY,
    JSON.stringify({
      createdAt: Date.now(),
      path,
    } satisfies LoginReturnToPayload)
  );
}

export function clearLoginReturnTo() {
  if (typeof window === "undefined") return;
  window.sessionStorage.removeItem(LOGIN_RETURN_TO_KEY);
}

export function consumeLoginReturnTo(fallback = "/dashboard") {
  if (typeof window === "undefined") return fallback;
  const payload = parseLoginReturnToPayload(
    window.sessionStorage.getItem(LOGIN_RETURN_TO_KEY)
  );
  window.sessionStorage.removeItem(LOGIN_RETURN_TO_KEY);
  if (!payload) return fallback;
  if (Date.now() - payload.createdAt > loginReturnToTTLMS) return fallback;
  return isSafeReturnPath(payload.path) ? payload.path : fallback;
}

export function notifyAuthRequired() {
  if (typeof window === "undefined") return;

  storeLoginReturnTo();
  clearBrowserAuthState({ notice: true });
  window.dispatchEvent(new CustomEvent(AUTH_REQUIRED_EVENT));
}
