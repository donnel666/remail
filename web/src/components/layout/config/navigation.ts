import {
  BarChart3,
  ClipboardList,
  Database,
  Globe,
  Headphones,
  Network,
  PackageOpen,
  Settings,
  Users,
  Wallet,
  Zap,
} from "lucide-react";
import type { SidebarNavGroup, SidebarNavItem, TopNavItem } from "../types";

export const ADMIN_ROLE_LEVEL = 80;

export const SIDEBAR_NAV_GROUPS: SidebarNavGroup[] = [
  {
    id: "workbench",
    labelKey: "Code Receiving",
    items: [{ path: "/dashboard", labelKey: "Workbench", icon: Zap }],
  },
  {
    id: "console",
    labelKey: "Console",
    items: [
      { path: "/console", labelKey: "Data Dashboard", icon: BarChart3 },
      { path: "/microsoft", labelKey: "Microsoft Emails", icon: Database },
      { path: "/domains", labelKey: "Domain Emails", icon: Globe },
      { path: "/orders", labelKey: "Order Records", icon: ClipboardList },
      { path: "/tickets", labelKey: "After-sales Tickets", icon: Headphones },
    ],
  },
  {
    id: "personal-center",
    labelKey: "Personal Center",
    items: [
      { path: "/wallet", labelKey: "Wallet Management", icon: Wallet },
      { path: "/account", labelKey: "Personal Settings", icon: Settings },
    ],
  },
  {
    id: "admin",
    labelKey: "Administrator",
    minRoleLevel: ADMIN_ROLE_LEVEL,
    items: [
      { path: "/admin/microsoft", labelKey: "Admin Microsoft Emails", icon: Database },
      { path: "/admin/domains", labelKey: "Admin Domain Emails", icon: Globe },
      { path: "/admin/projects", labelKey: "Project Management", icon: PackageOpen },
      { path: "/admin/proxies", labelKey: "Proxy Management", icon: Network },
      { path: "/admin/users", labelKey: "User Management", icon: Users },
      { path: "/admin/settings", labelKey: "System Settings", icon: Settings },
    ],
  },
];

export const SIDEBAR_NAV_ITEMS: SidebarNavItem[] = SIDEBAR_NAV_GROUPS.flatMap((group) =>
  group.items.map((item) => ({
    ...item,
    minRoleLevel: item.minRoleLevel ?? group.minRoleLevel,
  }))
);

export const ROUTES_WITH_SIDEBAR = SIDEBAR_NAV_ITEMS.map((item) => item.path);
export const CHROMELESS_ROUTES: string[] = ["/activation"];

export function getVisibleSidebarNavGroups(roleLevel: number): SidebarNavGroup[] {
  return SIDEBAR_NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter(
      (item) => roleLevel >= (item.minRoleLevel ?? group.minRoleLevel ?? 0)
    ),
  })).filter((group) => group.items.length > 0);
}

export function getSidebarRouteRequiredRoleLevel(pathname: string) {
  const matched = SIDEBAR_NAV_ITEMS.find(
    (item) => pathname === item.path || pathname.startsWith(`${item.path}/`)
  );

  return matched?.minRoleLevel ?? 0;
}

export const TOP_NAV_ITEMS: TopNavItem[] = [
  { path: "/", labelKey: "Home" },
  { path: "/console", labelKey: "Console", activePaths: ROUTES_WITH_SIDEBAR },
  { path: "/projects", labelKey: "Project Square" },
  { path: "/api-docs", labelKey: "API Docs" },
  { path: "/qna", labelKey: "FAQ" },
];
