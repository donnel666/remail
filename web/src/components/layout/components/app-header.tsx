import { Link, useRouterState } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { LanguageMenu } from "@/components/language-menu";
import { NotificationPopover } from "@/components/notification-popover";
import { ThemeSwitch } from "@/components/theme-switch";
import { TOP_NAV_ITEMS } from "../config/navigation";
import type { TopNavItem } from "../types";
import { HeaderLogo } from "./header-logo";

export function AppHeader() {
  const { t } = useTranslation();
  const routerState = useRouterState();
  const pathname = routerState.location.pathname;

  const isActive = (item: TopNavItem) => {
    const paths = [item.path, ...(item.activePaths ?? [])];
    return paths.some((path) =>
      path === "/" ? pathname === "/" : pathname.startsWith(path)
    );
  };

  return (
    <header className="fixed inset-x-0 top-0 z-50 h-16 bg-transparent">
      <nav className="flex h-full w-full items-center px-2 lg:pl-0 lg:pr-2">
        <Link
          to="/"
          className="group flex shrink-0 items-center gap-2 lg:h-full lg:w-[180px] lg:px-2"
        >
          <div className="flex size-8 shrink-0 items-center justify-center transition-transform duration-200 group-hover:scale-105">
            <HeaderLogo className="size-full" />
          </div>
          <span className="hidden text-lg font-semibold tracking-tight text-foreground sm:inline">
            Remail
          </span>
        </Link>

        <div className="scrollbar-none ml-3 flex min-w-0 flex-1 items-center gap-1 overflow-x-auto md:ml-5">
          {TOP_NAV_ITEMS.map((item) => (
            <Link
              key={item.path}
              to={item.path}
              className={cn(
                "flex-shrink-0 rounded-md p-2 text-base font-semibold leading-[21px] text-foreground transition-colors duration-200 hover:text-brand",
                isActive(item) && "font-semibold"
              )}
            >
              {t(item.labelKey)}
            </Link>
          ))}
        </div>

        <div className="ml-2 flex shrink-0 items-center gap-3">
          <NotificationPopover />
          <ThemeSwitch />
          <LanguageMenu />
          <Button
            size="sm"
            className={cn(
              "h-8 w-12 rounded-full bg-surface-sunken px-0 text-sm font-semibold text-foreground shadow-none",
              "hover:bg-brand-subtle hover:text-brand"
            )}
            render={<Link to="/login" />}
          >
            {t("Login")}
          </Button>
        </div>
      </nav>
    </header>
  );
}
