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
import { AuthProvider, useAuth } from "./context/auth-provider";
import { ActivationGateProvider } from "./context/activation-gate";
import AppShell from "./components/layout/AppShell";
import {
  ROUTES_WITH_SIDEBAR,
  getSidebarRouteRequiredRoleLevel,
} from "./components/layout/config/navigation";
import { getActivation } from "./lib/iam-api";
import { PlaceholderPage } from "./pages/PlaceholderPage";
import { ForbiddenPage, NotFoundPage, ServerErrorPage } from "./pages/ErrorPages";

const Home = lazy(() => import("./pages/Home"));
const Activation = lazy(() => import("./pages/Activation"));
const Dashboard = lazy(() => import("./pages/Dashboard"));
const Projects = lazy(() => import("./pages/Projects"));
const Pickup = lazy(() => import("./pages/Pickup"));
const ApiDocs = lazy(() => import("./pages/ApiDocs"));
const Qna = lazy(() => import("./pages/Qna"));
const Login = lazy(() => import("./pages/Login"));
const Register = lazy(() => import("./pages/Register"));
const PasswordReset = lazy(() => import("./pages/PasswordReset"));
const Account = lazy(() => import("./pages/Account"));
const ApiKeys = lazy(() => import("./pages/ApiKeys"));
const ConsoleOverview = lazy(() => import("./pages/ConsoleOverview"));
const Wallet = lazy(() => import("./pages/Wallet"));
const Orders = lazy(() => import("./pages/Orders"));
const Tickets = lazy(() => import("./pages/Tickets"));
const MicrosoftEmails = lazy(() => import("./pages/MicrosoftEmails"));
const DomainEmails = lazy(() => import("./pages/DomainEmails"));
const AdminProjects = lazy(() => import("./pages/AdminProjects"));
const ProxyManagement = lazy(() => import("./pages/ProxyManagement"));
const Invite = lazy(() => import("./pages/Invite"));
const Recharge = lazy(() => import("./pages/Recharge"));

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

const EXTRA_PROTECTED_ROUTES = ["/apikeys", "/invite", "/recharge"];
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
  const requiredRoleLevel = useMemo(
    () => getSidebarRouteRequiredRoleLevel(pathname),
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
      currentUser.roleLevel < requiredRoleLevel
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
    requiredRoleLevel,
  ]);

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
  createRoute({ getParentRoute: () => rootRoute, path: "/apikeys", component: ApiKeys }),
  createRoute({ getParentRoute: () => rootRoute, path: "/api-docs", component: ApiDocs }),
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
