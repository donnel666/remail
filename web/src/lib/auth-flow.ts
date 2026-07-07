export const LOGIN_NOTICE_KEY = "remail-login-notice";
export const AUTH_REQUIRED_EVENT = "remail-auth-required";
export const AUTH_REQUIRED_NOTICE = "Authentication is required.";

const authCookieNames = ["sid", "csrf_token"];
const cookieDeletePaths = ["/", "/v1"];

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

export function notifyAuthRequired() {
  if (typeof window === "undefined") return;

  clearBrowserAuthState({ notice: true });
  window.dispatchEvent(new CustomEvent(AUTH_REQUIRED_EVENT));
}
