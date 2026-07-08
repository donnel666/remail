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
import { permissionKey } from "@/context/auth-provider";
import type { SidebarNavGroup, SidebarNavItem, TopNavItem } from "../types";

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
    items: [
      {
        path: "/admin/microsoft",
        labelKey: "Admin Microsoft Emails",
        icon: Database,
        requiredPermission: permissionKey("core:resource", "read"),
      },
      {
        path: "/admin/domains",
        labelKey: "Admin Domain Emails",
        icon: Globe,
        requiredPermission: permissionKey("core:resource", "read"),
      },
      {
        path: "/admin/projects",
        labelKey: "Project Management",
        icon: PackageOpen,
        requiredPermission: permissionKey("core:project", "read"),
      },
      {
        path: "/admin/proxies",
        labelKey: "Proxy Management",
        icon: Network,
        requiredPermission: permissionKey("proxy:proxy", "read"),
      },
      {
        path: "/admin/users",
        labelKey: "User Management",
        icon: Users,
        requiredPermission: permissionKey("iam:user", "read"),
      },
      {
        path: "/admin/settings",
        labelKey: "System Settings",
        icon: Settings,
        requiredPermission: permissionKey("iam:permission", "read"),
      },
    ],
  },
];

export const SIDEBAR_NAV_ITEMS: SidebarNavItem[] = SIDEBAR_NAV_GROUPS.flatMap((group) =>
  group.items.map((item) => ({
    ...item,
    requiredPermission: item.requiredPermission ?? group.requiredPermission,
  }))
);

export const ROUTES_WITH_SIDEBAR = SIDEBAR_NAV_ITEMS.map((item) => item.path);
export const CHROMELESS_ROUTES: string[] = ["/activation"];

export function getVisibleSidebarNavGroups(permissions: string[]): SidebarNavGroup[] {
  const permissionSet = new Set(permissions);
  return SIDEBAR_NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter(
      (item) => {
        const requiredPermission = item.requiredPermission ?? group.requiredPermission;
        return !requiredPermission || permissionSet.has(requiredPermission);
      }
    ),
  })).filter((group) => group.items.length > 0);
}

export function getSidebarRouteRequiredPermission(pathname: string) {
  const matched = SIDEBAR_NAV_ITEMS.find(
    (item) => pathname === item.path || pathname.startsWith(`${item.path}/`)
  );

  return matched?.requiredPermission ?? null;
}

export const TOP_NAV_ITEMS: TopNavItem[] = [
  { path: "/", labelKey: "Home" },
  { path: "/console", labelKey: "Console", activePaths: ROUTES_WITH_SIDEBAR },
  { path: "/projects", labelKey: "Project Square" },
  { path: "/api-docs", labelKey: "API Docs" },
  { path: "/qna", labelKey: "FAQ" },
];
