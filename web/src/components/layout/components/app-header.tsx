import { Link, useRouterState } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { useAuth } from "@/context/auth-provider";
import { LanguageMenu } from "@/components/language-menu";
import { NotificationPopover } from "@/components/notification-popover";
import { ThemeSwitch } from "@/components/theme-switch";
import { UserMenu } from "@/components/user-menu";
import { TOP_NAV_ITEMS } from "../config/navigation";
import type { TopNavItem } from "../types";
import { HeaderLogo } from "./header-logo";

export function AppHeader() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const routerState = useRouterState();
  const pathname = routerState.location.pathname;

  const isActive = (item: TopNavItem) => {
    const paths = [item.path, ...(item.activePaths ?? [])];
    return paths.some((path) =>
      path === "/" ? pathname === "/" : pathname.startsWith(path)
    );
  };

  return (
    <header className="sticky inset-x-0 top-0 z-50 h-16 bg-white/75 text-foreground backdrop-blur-lg transition-colors duration-300 dark:bg-zinc-900/75">
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
                "flex shrink-0 items-center gap-1 rounded-md p-2 text-base font-bold leading-[21px] text-foreground transition-all duration-200 ease-in-out hover:text-brand",
                isActive(item) && "font-bold"
              )}
            >
              {t(item.labelKey)}
            </Link>
          ))}
        </div>

        <div className="ml-2 flex shrink-0 items-center gap-2 md:gap-3">
          <NotificationPopover />
          <ThemeSwitch />
          <LanguageMenu />
          {currentUser ? <UserMenu /> : <AuthLinks />}
        </div>
      </nav>
    </header>
  );
}

function AuthLinks() {
  const { t } = useTranslation();

  return (
    <div className="flex h-8 items-center">
      <Link
        to="/login"
        className={cn(
          "flex h-8 items-center justify-center rounded-full bg-surface-sunken px-1.5 text-xs font-semibold text-foreground/80 transition-colors hover:bg-surface-hover",
          "md:rounded-l-full md:rounded-r-none"
        )}
      >
        <span className="p-1.5">{t("Login")}</span>
      </Link>
      <div className="hidden md:block">
        <Link
          to="/register"
          className="flex h-8 items-center justify-center rounded-l-none rounded-r-full bg-brand px-1.5 text-xs font-semibold text-white transition-colors hover:bg-brand-hover"
        >
          <span className="p-1.5">{t("Register")}</span>
        </Link>
      </div>
    </div>
  );
}
