import { useMemo, useState } from "react";
import { Link, useLocation } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-provider";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { cn } from "@/lib/utils";
import { getVisibleSidebarNavGroups } from "../config/navigation";

interface SidebarProps {
  mobileOpen: boolean;
  onMobileNavigate: () => void;
}

export function Sidebar({ mobileOpen, onMobileNavigate }: SidebarProps) {
  const { t } = useTranslation();
  const location = useLocation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile(1023);
  const [collapsed, setCollapsed] = useState(false);
  const permissions = currentUser?.permissions ?? [];
  const visibleGroups = useMemo(
    () => getVisibleSidebarNavGroups(permissions),
    [permissions]
  );
  const isCollapsed = collapsed && !isMobile;

  return (
    <aside
      id="app-sidebar"
      className={cn(
        "top-16 h-[calc(100vh-64px)] max-h-[calc(100vh-64px)] shrink-0 self-start flex-col bg-background text-foreground transition-[width] duration-200 lg:sticky lg:z-auto lg:flex lg:shadow-none",
        mobileOpen ? "fixed left-0 z-40 flex w-[180px] shadow-xl" : "hidden",
        isCollapsed ? "lg:w-16" : "lg:w-[180px]"
      )}
    >
      <nav className={cn("scrollbar-none flex-1 overflow-y-auto bg-background px-2", isCollapsed ? "pt-3" : "pt-6")}>
        {visibleGroups.map((group, groupIndex) => (
          <section key={group.id}>
            {groupIndex > 0 ? (
              <div
                className={cn(
                  "mb-1 mt-1 h-px bg-border",
                  isCollapsed ? "mx-3" : "ml-2 w-[164px]"
                )}
              />
            ) : null}

            {isCollapsed ? null : (
              <div
                className={cn(
                  "h-[30px] px-[15px] pb-2 pt-1 text-xs font-medium leading-[18px] text-muted-foreground",
                  groupIndex > 0 && "mt-1"
                )}
              >
                {t(group.labelKey)}
              </div>
            )}

            <div className="space-y-1">
              {group.items.map((item) => {
                const isActive =
                  location.pathname === item.path ||
                  location.pathname.startsWith(`${item.path}/`);
                const Icon = item.icon;

                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    onClick={onMobileNavigate}
                    aria-label={t(item.labelKey)}
                    title={isCollapsed ? t(item.labelKey) : undefined}
                    className={cn(
                      "group flex h-[30px] items-center rounded-[10px] px-3 py-1 text-sm font-normal leading-5 transition-colors",
                      isCollapsed ? "justify-center" : "gap-2",
                      isActive
                        ? "bg-brand-subtle text-foreground"
                        : "text-foreground hover:bg-surface-sunken"
                    )}
                  >
                    <span className="flex h-[22px] w-[22px] shrink-0 items-center justify-center">
                      <Icon
                        className={cn(
                          "size-4 shrink-0 transition-[color,transform] duration-200",
                          isActive
                            ? "scale-105 text-brand"
                            : "text-muted-foreground group-hover:text-foreground"
                        )}
                      />
                    </span>
                    {isCollapsed ? null : <span className="truncate">{t(item.labelKey)}</span>}
                  </Link>
                );
              })}
            </div>
          </section>
        ))}
      </nav>

      <div className="sticky bottom-0 hidden bg-background p-3 shadow-[0_-10px_10px_-5px_var(--background)] lg:block">
        <button
          type="button"
          onClick={() => setCollapsed((value) => !value)}
          className={cn(
            "flex h-6 w-full items-center justify-center gap-2 rounded-[10px] border border-border px-3 text-sm font-semibold text-foreground/80 transition-colors hover:bg-surface-sunken",
            isCollapsed && "px-0"
          )}
          aria-label={isCollapsed ? t("Expand sidebar") : t("Collapse sidebar")}
        >
          {isCollapsed ? (
            <ChevronRight className="size-4 text-muted-foreground" strokeWidth={2.5} />
          ) : (
            <>
              <ChevronLeft className="size-4 text-muted-foreground" strokeWidth={2.5} />
              <span>{t("Collapse sidebar")}</span>
            </>
          )}
        </button>
      </div>
    </aside>
  );
}
