import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

export interface CurrentUser {
  email: string;
  name: string;
}

interface AuthProviderState {
  currentUser: CurrentUser | null;
  loading: boolean;
  signIn: (user: Partial<CurrentUser>) => void;
  signOut: () => void;
}

const STORAGE_KEY = "remail-current-user";

const AuthContext = createContext<AuthProviderState | null>(null);

function readStoredUser() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as CurrentUser) : null;
  } catch {
    return null;
  }
}

function persistUser(user: CurrentUser | null) {
  try {
    if (user) localStorage.setItem(STORAGE_KEY, JSON.stringify(user));
    else localStorage.removeItem(STORAGE_KEY);
  } catch {}
}

function normalizeUser(user: Partial<CurrentUser>): CurrentUser {
  const email = user.email?.trim() || "user@remail.local";
  return {
    email,
    name: user.name?.trim() || email.split("@")[0] || "Remail User",
  };
}

function userFromMeResponse(value: unknown): CurrentUser | null {
  if (!value || typeof value !== "object") return null;
  const user = "user" in value ? (value as { user?: unknown }).user : value;
  if (!user || typeof user !== "object") return null;

  const record = user as Record<string, unknown>;
  const email = typeof record.email === "string" ? record.email : "";
  const name =
    (typeof record.nickname === "string" && record.nickname) ||
    (typeof record.name === "string" && record.name) ||
    email.split("@")[0];

  return email ? { email, name: name || email } : null;
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [currentUser, setCurrentUser] = useState<CurrentUser | null>(() =>
    readStoredUser()
  );
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    async function loadCurrentUser() {
      try {
        const response = await fetch("/v1/me", {
          credentials: "include",
          headers: { Accept: "application/json" },
        });
        if (response.status === 401) {
          setCurrentUser(null);
          persistUser(null);
          return;
        }
        if (!response.ok) return;
        const nextUser = userFromMeResponse(await response.json());
        if (!cancelled && nextUser) {
          setCurrentUser(nextUser);
          persistUser(nextUser);
        }
      } catch {
        // The standalone frontend can run before the IAM API is available.
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void loadCurrentUser();
    return () => {
      cancelled = true;
    };
  }, []);

  const signIn = useCallback((user: Partial<CurrentUser>) => {
    const nextUser = normalizeUser(user);
    setCurrentUser(nextUser);
    persistUser(nextUser);
  }, []);

  const signOut = useCallback(() => {
    setCurrentUser(null);
    persistUser(null);
    void fetch("/v1/sessions/current", {
      method: "DELETE",
      credentials: "include",
    }).catch(() => {});
  }, []);

  const value = useMemo(
    () => ({ currentUser, loading, signIn, signOut }),
    [currentUser, loading, signIn, signOut]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return context;
}
