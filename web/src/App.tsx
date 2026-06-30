import { Suspense, lazy } from "react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  Outlet,
} from "@tanstack/react-router";
import { ThemeProvider } from "./context/theme-provider";
import AppShell from "./components/layout/AppShell";

const Home = lazy(() => import("./pages/Home"));
const Dashboard = lazy(() => import("./pages/Dashboard"));
const Projects = lazy(() => import("./pages/Projects"));
const ApiDocs = lazy(() => import("./pages/ApiDocs"));
const Qna = lazy(() => import("./pages/Qna"));
const Login = lazy(() => import("./pages/Login"));
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

const rootRoute = createRootRoute({
  component: () => (
    <ThemeProvider>
      <AppShell>
        <Suspense fallback={<Loading />}>
          <Outlet />
        </Suspense>
      </AppShell>
    </ThemeProvider>
  ),
});

const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: "/", component: Home }),
  createRoute({ getParentRoute: () => rootRoute, path: "/login", component: Login }),
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
