import { useEffect, useState } from "react";
import { useLocation } from "@tanstack/react-router";
import { AppHeader } from "./components/app-header";
import { Sidebar } from "./components/sidebar";
import { CHROMELESS_ROUTES, ROUTES_WITH_SIDEBAR } from "./config/navigation";

function matchesRoute(pathname: string, route: string) {
  return pathname === route || pathname.startsWith(`${route}/`);
}

export default function AppShell({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const isChromeless = CHROMELESS_ROUTES.some((route) =>
    matchesRoute(location.pathname, route)
  );
  const hasSidebar =
    !isChromeless &&
    ROUTES_WITH_SIDEBAR.some((route) => matchesRoute(location.pathname, route));

  useEffect(() => setMobileSidebarOpen(false), [location.pathname]);

  if (isChromeless) {
    return <>{children}</>;
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <AppHeader
        hasSidebar={hasSidebar}
        mobileSidebarOpen={mobileSidebarOpen}
        onMobileSidebarToggle={() => setMobileSidebarOpen((open) => !open)}
      />
      <div className="mx-auto flex w-full max-w-full flex-1">
        {hasSidebar && (
          <Sidebar
            mobileOpen={mobileSidebarOpen}
            onMobileNavigate={() => setMobileSidebarOpen(false)}
          />
        )}
        <main className="min-w-0 flex-1 overflow-y-auto">{children}</main>
      </div>
    </div>
  );
}
