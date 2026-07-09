import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  Outlet,
  useLocation,
  useNavigate,
} from "@tanstack/react-router";
import { LocaleProvider } from "@douyinfe/semi-ui";
import zhCN from "@douyinfe/semi-ui/lib/es/locale/source/zh_CN";
import enGB from "@douyinfe/semi-ui/lib/es/locale/source/en_GB";
import { useTranslation } from "react-i18next";
import { ThemeProvider } from "./context/theme-provider";
import { AuthProvider, hasPermissionKey, useAuth } from "./context/auth-provider";
import { ActivationGateProvider } from "./context/activation-gate";
import { AUTH_REQUIRED_EVENT } from "./lib/auth-flow";
import AppShell from "./components/layout/AppShell";
import {
  ROUTES_WITH_SIDEBAR,
  getSidebarRouteRequiredPermission,
} from "./components/layout/config/navigation";
import { getActivation } from "./lib/iam-api";
import { PlaceholderPage } from "./pages/PlaceholderPage";
import { ForbiddenPage, NotFoundPage, ServerErrorPage } from "./pages/ErrorPages";
import { preloadApiDocsAssets } from "./pages/api-docs/assets";

const pageLoaders = {
  home: () => import("./pages/Home"),
  activation: () => import("./pages/Activation"),
  dashboard: () => import("./pages/Dashboard"),
  projects: () => import("./pages/Projects"),
  pickup: () => import("./pages/Pickup"),
  apiDocs: () => import("./pages/ApiDocs"),
  qna: () => import("./pages/Qna"),
  login: () => import("./pages/Login"),
  register: () => import("./pages/Register"),
  passwordReset: () => import("./pages/PasswordReset"),
  account: () => import("./pages/Account"),
  consoleOverview: () => import("./pages/ConsoleOverview"),
  wallet: () => import("./pages/Wallet"),
  orders: () => import("./pages/Orders"),
  tickets: () => import("./pages/Tickets"),
  microsoftEmails: () => import("./pages/MicrosoftEmails"),
  domainEmails: () => import("./pages/DomainEmails"),
  adminProjects: () => import("./pages/AdminProjects"),
  proxyManagement: () => import("./pages/ProxyManagement"),
  invite: () => import("./pages/Invite"),
  recharge: () => import("./pages/Recharge"),
};

const Home = lazy(pageLoaders.home);
const Activation = lazy(pageLoaders.activation);
const Dashboard = lazy(pageLoaders.dashboard);
const Projects = lazy(pageLoaders.projects);
const Pickup = lazy(pageLoaders.pickup);
const ApiDocs = lazy(pageLoaders.apiDocs);
const Qna = lazy(pageLoaders.qna);
const Login = lazy(pageLoaders.login);
const Register = lazy(pageLoaders.register);
const PasswordReset = lazy(pageLoaders.passwordReset);
const Account = lazy(pageLoaders.account);
const ConsoleOverview = lazy(pageLoaders.consoleOverview);
const Wallet = lazy(pageLoaders.wallet);
const Orders = lazy(pageLoaders.orders);
const Tickets = lazy(pageLoaders.tickets);
const MicrosoftEmails = lazy(pageLoaders.microsoftEmails);
const DomainEmails = lazy(pageLoaders.domainEmails);
const AdminProjects = lazy(pageLoaders.adminProjects);
const ProxyManagement = lazy(pageLoaders.proxyManagement);
const Invite = lazy(pageLoaders.invite);
const Recharge = lazy(pageLoaders.recharge);

const ROUTE_PRELOAD_BATCH_SIZE = 4;
const ROUTE_PRELOAD_BATCH_DELAY_MS = 200;
let routeModulesPreloadStarted = false;
let apiDocsPreloadStarted = false;
type PageLoaderKey = keyof typeof pageLoaders;

const routePreloadPriority = [
  "dashboard",
  "projects",
  "apiDocs",
  "pickup",
  "wallet",
  "microsoftEmails",
  "domainEmails",
  "adminProjects",
  "proxyManagement",
  "consoleOverview",
  "invite",
  "recharge",
  "account",
  "orders",
  "tickets",
  "qna",
  "login",
  "register",
  "passwordReset",
  "activation",
  "home",
] satisfies PageLoaderKey[];

type RoutePreloadWindow = Window &
  typeof globalThis & {
    requestIdleCallback?: (
      callback: IdleRequestCallback,
      options?: IdleRequestOptions
    ) => number;
  };

function scheduleRouteModulePreload() {
  if (import.meta.env.DEV) return;
  if (routeModulesPreloadStarted || typeof window === "undefined") return;
  routeModulesPreloadStarted = true;
  const browserWindow = window as RoutePreloadWindow;

  const runWhenVisible = (callback: () => void) => {
    if (document.visibilityState !== "hidden") {
      callback();
      return;
    }

    const handleVisibilityChange = () => {
      if (document.visibilityState === "hidden") return;
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      callback();
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
  };

  let cursor = 0;
  const loadNextBatch = () => {
    if (document.visibilityState === "hidden") {
      runWhenVisible(loadNextBatch);
      return;
    }

    const batch = routePreloadPriority.slice(
      cursor,
      cursor + ROUTE_PRELOAD_BATCH_SIZE
    );
    cursor += batch.length;
    if (batch.length === 0) return;

    void Promise.allSettled(batch.map((key) => pageLoaders[key]())).finally(() => {
      if (cursor < routePreloadPriority.length) {
        browserWindow.setTimeout(loadNextBatch, ROUTE_PRELOAD_BATCH_DELAY_MS);
      }
    });
  };

  const startPreload = () => {
    if (browserWindow.requestIdleCallback) {
      browserWindow.requestIdleCallback(() => loadNextBatch(), {
        timeout: 2000,
      });
      return;
    }

    browserWindow.setTimeout(loadNextBatch, 800);
  };

  if (document.readyState === "complete") {
    browserWindow.setTimeout(startPreload, 400);
    return;
  }

  browserWindow.addEventListener(
    "load",
    () => browserWindow.setTimeout(startPreload, 400),
    { once: true }
  );
}

function scheduleApiDocsPreload() {
  if (apiDocsPreloadStarted || typeof window === "undefined") return;
  apiDocsPreloadStarted = true;
  const browserWindow = window as RoutePreloadWindow;

  const preload = () => {
    preloadApiDocsAssets();
    void pageLoaders.apiDocs().catch((error: unknown) => {
      console.warn("Failed to preload API docs page", error);
    });
  };

  if (document.readyState === "complete") {
    browserWindow.setTimeout(preload, 400);
    return;
  }

  browserWindow.addEventListener(
    "load",
    () => browserWindow.setTimeout(preload, 400),
    { once: true }
  );
}

function AdminMicrosoftEmails() {
  return <PlaceholderPage titleKey="Admin Microsoft Emails" />;
}

function AdminDomainEmails() {
  return <PlaceholderPage titleKey="Admin Domain Emails" />;
}

function UserManagement() {
  return <PlaceholderPage titleKey="User Management" />;
}

function SystemSettings() {
  return <PlaceholderPage titleKey="System Settings" />;
}

function Loading() {
  return (
    <div className="flex h-screen items-center justify-center bg-background">
      <div className="h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
    </div>
  );
}

function SemiLocaleWrapper({ children }: { children: ReactNode }) {
  const { i18n } = useTranslation();
  const locale = i18n.language.startsWith("en") ? enGB : zhCN;
  return <LocaleProvider locale={locale}>{children}</LocaleProvider>;
}

const EXTRA_PROTECTED_ROUTES = ["/invite", "/recharge"];
const PROTECTED_ROUTES = Array.from(
  new Set([...ROUTES_WITH_SIDEBAR, ...EXTRA_PROTECTED_ROUTES])
);

function matchesRoute(pathname: string, route: string) {
  return pathname === route || pathname.startsWith(`${route}/`);
}

function RouteGate({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const { currentUser, loading: authLoading } = useAuth();
  const [activationNeeded, setActivationNeeded] = useState<boolean | null>(null);

  const pathname = location.pathname;
  const isProtectedRoute = useMemo(
    () => PROTECTED_ROUTES.some((route) => matchesRoute(pathname, route)),
    [pathname]
  );
  const requiredPermission = useMemo(
    () => getSidebarRouteRequiredPermission(pathname),
    [pathname]
  );

  const markActivated = useCallback(() => {
    setActivationNeeded(false);
  }, []);

  const activationGateValue = useMemo(
    () => ({ activationNeeded, markActivated }),
    [activationNeeded, markActivated]
  );

  useEffect(() => {
    let cancelled = false;

    void getActivation()
      .then((status) => {
        if (!cancelled) setActivationNeeded(status.needed);
      })
      .catch(() => {
        if (!cancelled) setActivationNeeded(false);
      });

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (activationNeeded === null) return;

    if (activationNeeded && pathname !== "/activation") {
      void navigate({ to: "/activation", replace: true });
      return;
    }

    if (!activationNeeded && pathname === "/activation") {
      void navigate({ to: "/login", replace: true });
      return;
    }

    if (!activationNeeded && !authLoading && !currentUser && isProtectedRoute) {
      void navigate({ to: "/login", replace: true });
      return;
    }

    if (
      !activationNeeded &&
      !authLoading &&
      currentUser &&
      requiredPermission &&
      !hasPermissionKey(currentUser, requiredPermission)
    ) {
      void navigate({ to: "/403", replace: true });
      return;
    }

    if (
      !activationNeeded &&
      currentUser &&
      (pathname === "/login" || pathname === "/register")
    ) {
      void navigate({ to: "/dashboard", replace: true });
    }
  }, [
    activationNeeded,
    authLoading,
    currentUser,
    isProtectedRoute,
    navigate,
    pathname,
    requiredPermission,
  ]);

  useEffect(() => {
    const handleAuthRequired = () => {
      if (pathname === "/login" || pathname === "/activation") return;
      void navigate({ to: "/login", replace: true });
    };

    window.addEventListener(AUTH_REQUIRED_EVENT, handleAuthRequired);
    return () => {
      window.removeEventListener(AUTH_REQUIRED_EVENT, handleAuthRequired);
    };
  }, [navigate, pathname]);

  let content = children;
  if (activationNeeded === null || (authLoading && isProtectedRoute)) {
    content = <Loading />;
  }

  return (
    <ActivationGateProvider value={activationGateValue}>
      {content}
    </ActivationGateProvider>
  );
}

const rootRoute = createRootRoute({
  component: () => (
    <RouteGate>
      <AppShell>
        <Suspense fallback={<Loading />}>
          <Outlet />
        </Suspense>
      </AppShell>
    </RouteGate>
  ),
  notFoundComponent: NotFoundPage,
  errorComponent: ({ reset }) => <ServerErrorPage onRetry={reset} />,
});

const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: "/", component: Home }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: "/activation",
    component: Activation,
  }),
  createRoute({ getParentRoute: () => rootRoute, path: "/login", component: Login }),
  createRoute({ getParentRoute: () => rootRoute, path: "/register", component: Register }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: "/password-reset",
    component: PasswordReset,
  }),
  createRoute({ getParentRoute: () => rootRoute, path: "/account", component: Account }),
  createRoute({ getParentRoute: () => rootRoute, path: "/docs", component: ApiDocs }),
  createRoute({ getParentRoute: () => rootRoute, path: "/qna", component: Qna }),
  createRoute({ getParentRoute: () => rootRoute, path: "/403", component: ForbiddenPage }),
  createRoute({ getParentRoute: () => rootRoute, path: "/404", component: NotFoundPage }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: "/500",
    component: () => <ServerErrorPage onRetry={() => window.location.reload()} />,
  }),
  createRoute({ getParentRoute: () => rootRoute, path: "/console", component: ConsoleOverview }),
  createRoute({ getParentRoute: () => rootRoute, path: "/dashboard", component: Dashboard }),
  createRoute({ getParentRoute: () => rootRoute, path: "/pickup", component: Pickup }),
  createRoute({ getParentRoute: () => rootRoute, path: "/projects", component: Projects }),
  createRoute({ getParentRoute: () => rootRoute, path: "/wallet", component: Wallet }),
  createRoute({ getParentRoute: () => rootRoute, path: "/orders", component: Orders }),
  createRoute({ getParentRoute: () => rootRoute, path: "/tickets", component: Tickets }),
  createRoute({ getParentRoute: () => rootRoute, path: "/microsoft", component: MicrosoftEmails }),
  createRoute({ getParentRoute: () => rootRoute, path: "/domains", component: DomainEmails }),
  createRoute({ getParentRoute: () => rootRoute, path: "/invite", component: Invite }),
  createRoute({ getParentRoute: () => rootRoute, path: "/recharge", component: Recharge }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: "/admin/microsoft",
    component: AdminMicrosoftEmails,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: "/admin/domains",
    component: AdminDomainEmails,
  }),
  createRoute({ getParentRoute: () => rootRoute, path: "/admin/projects", component: AdminProjects }),
  createRoute({ getParentRoute: () => rootRoute, path: "/admin/proxies", component: ProxyManagement }),
  createRoute({ getParentRoute: () => rootRoute, path: "/admin/users", component: UserManagement }),
  createRoute({ getParentRoute: () => rootRoute, path: "/admin/settings", component: SystemSettings }),
]);

const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

function App() {
  useEffect(() => {
    scheduleApiDocsPreload();
    scheduleRouteModulePreload();
  }, []);

  return (
    <ThemeProvider>
      <SemiLocaleWrapper>
        <AuthProvider>
          <RouterProvider router={router} />
        </AuthProvider>
      </SemiLocaleWrapper>
    </ThemeProvider>
  );
}

export default App;
