import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  getMe,
  login as loginRequest,
  logout as logoutRequest,
  type LoginRequest,
  type UserResponse,
} from "@/lib/iam-api";
import { AUTH_REQUIRED_EVENT, clearBrowserAuthState } from "@/lib/auth-flow";

export interface CurrentUser {
  id: number;
  email: string;
  nickname: string;
  name: string;
  role: UserResponse["role"];
  userGroup: UserResponse["userGroup"];
  permissions: string[];
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  lastLoginAt?: string | null;
}

interface AuthProviderState {
  currentUser: CurrentUser | null;
  loading: boolean;
  login: (payload: LoginRequest) => Promise<CurrentUser>;
  logout: () => Promise<void>;
  refreshCurrentUser: () => Promise<CurrentUser | null>;
}

const AuthContext = createContext<AuthProviderState | null>(null);

function toCurrentUser(user: UserResponse): CurrentUser {
  const nickname = user.nickname?.trim() || "";
  const name = nickname || user.email.split("@")[0] || user.email;
  return {
    id: user.id,
    email: user.email,
    nickname,
    name,
    role: user.role,
    userGroup: user.userGroup,
    permissions: user.permissions ?? [],
    enabled: user.enabled,
    createdAt: user.createdAt,
    updatedAt: user.updatedAt,
    lastLoginAt: user.lastLoginAt,
  };
}

export function permissionKey(resource: string, action: string) {
  return `${resource}:${action}`;
}

export function hasPermission(
  user: Pick<CurrentUser, "permissions"> | null | undefined,
  resource: string,
  action: string
) {
  return hasPermissionKey(user, permissionKey(resource, action));
}

export function hasPermissionKey(
  user: Pick<CurrentUser, "permissions"> | null | undefined,
  key: string
) {
  return Boolean(user?.permissions.includes(key));
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [currentUser, setCurrentUser] = useState<CurrentUser | null>(null);
  const [loading, setLoading] = useState(true);

  const refreshCurrentUser = useCallback(async () => {
    try {
      const response = await getMe();
      const nextUser = toCurrentUser(response.user);
      setCurrentUser(nextUser);
      return nextUser;
    } catch {
      clearBrowserAuthState();
      setCurrentUser(null);
      return null;
    }
  }, []);

  useEffect(() => {
    let cancelled = false;

    void refreshCurrentUser().finally(() => {
      if (!cancelled) setLoading(false);
    });

    return () => {
      cancelled = true;
    };
  }, [refreshCurrentUser]);

  useEffect(() => {
    const handleAuthRequired = () => {
      clearBrowserAuthState({ notice: true });
      setCurrentUser(null);
    };

    window.addEventListener(AUTH_REQUIRED_EVENT, handleAuthRequired);
    return () => {
      window.removeEventListener(AUTH_REQUIRED_EVENT, handleAuthRequired);
    };
  }, []);

  const login = useCallback(async (payload: LoginRequest) => {
    const response = await loginRequest(payload);
    const nextUser = toCurrentUser(response.user);
    setCurrentUser(nextUser);
    return nextUser;
  }, []);

  const logout = useCallback(async () => {
    try {
      await logoutRequest();
    } finally {
      clearBrowserAuthState();
      setCurrentUser(null);
    }
  }, []);

  const value = useMemo(
    () => ({ currentUser, loading, login, logout, refreshCurrentUser }),
    [currentUser, loading, login, logout, refreshCurrentUser]
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
