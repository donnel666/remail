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
import { ThemeProvider } from "./context/theme-provider";
import { AuthProvider, useAuth } from "./context/auth-provider";
import { ActivationGateProvider } from "./context/activation-gate";
import AppShell from "./components/layout/AppShell";
import { ROUTES_WITH_SIDEBAR } from "./components/layout/config/navigation";
import { getActivation } from "./lib/iam-api";

const Home = lazy(() => import("./pages/Home"));
const Activation = lazy(() => import("./pages/Activation"));
const Dashboard = lazy(() => import("./pages/Dashboard"));
const Projects = lazy(() => import("./pages/Projects"));
const ApiDocs = lazy(() => import("./pages/ApiDocs"));
const Qna = lazy(() => import("./pages/Qna"));
const Login = lazy(() => import("./pages/Login"));
const Register = lazy(() => import("./pages/Register"));
const PasswordReset = lazy(() => import("./pages/PasswordReset"));
const Account = lazy(() => import("./pages/Account"));
const ApiKeys = lazy(() => import("./pages/ApiKeys"));
const Financial = lazy(() => import("./pages/Financial"));
const Orders = lazy(() => import("./pages/Orders"));
const MyEmails = lazy(() => import("./pages/MyEmails"));
const AfterSales = lazy(() => import("./pages/AfterSales"));
const Resources = lazy(() => import("./pages/Resources"));
const Invite = lazy(() => import("./pages/Invite"));
const Recharge = lazy(() => import("./pages/Recharge"));

function Loading() {
  return (
    <div className="flex h-screen items-center justify-center bg-background">
      <div className="h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
    </div>
  );
}

const PROTECTED_ROUTES = [...ROUTES_WITH_SIDEBAR, "/account", "/apikeys"];

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
    <ThemeProvider>
      <AuthProvider>
        <RouteGate>
          <AppShell>
            <Suspense fallback={<Loading />}>
              <Outlet />
            </Suspense>
          </AppShell>
        </RouteGate>
      </AuthProvider>
    </ThemeProvider>
  ),
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
  createRoute({ getParentRoute: () => rootRoute, path: "/dashboard", component: Dashboard }),
  createRoute({ getParentRoute: () => rootRoute, path: "/projects", component: Projects }),
  createRoute({ getParentRoute: () => rootRoute, path: "/financial", component: Financial }),
  createRoute({ getParentRoute: () => rootRoute, path: "/orders", component: Orders }),
  createRoute({ getParentRoute: () => rootRoute, path: "/my-emails", component: MyEmails }),
  createRoute({ getParentRoute: () => rootRoute, path: "/after-sales", component: AfterSales }),
  createRoute({ getParentRoute: () => rootRoute, path: "/resources", component: Resources }),
  createRoute({ getParentRoute: () => rootRoute, path: "/invite", component: Invite }),
  createRoute({ getParentRoute: () => rootRoute, path: "/recharge", component: Recharge }),
]);

const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

function App() {
  return <RouterProvider router={router} />;
}

export default App;
