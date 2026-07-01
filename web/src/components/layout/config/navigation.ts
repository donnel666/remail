import {
  Banknote,
  ClipboardList,
  Database,
  Headphones,
  Shield,
  UserPlus,
  Wallet,
  Zap,
} from "lucide-react";
import type { SidebarNavGroup, SidebarNavItem, TopNavItem } from "../types";

export const SIDEBAR_NAV_GROUPS: SidebarNavGroup[] = [
  {
    labelKey: "Console",
    items: [{ path: "/dashboard", labelKey: "Code Workspace", icon: Zap }],
  },
  {
    labelKey: "Resources",
    items: [
      { path: "/my-emails", labelKey: "Project Assets", icon: Shield },
      { path: "/resources", labelKey: "Microsoft Emails", icon: Database },
      { path: "/orders", labelKey: "Order Records", icon: ClipboardList },
      { path: "/after-sales", labelKey: "After-sales Tickets", icon: Headphones },
    ],
  },
  {
    labelKey: "Finance",
    items: [
      { path: "/financial", labelKey: "Financial Center", icon: Wallet },
      { path: "/recharge", labelKey: "Online Recharge", icon: Banknote },
      { path: "/invite", labelKey: "Invite Rebates", icon: UserPlus },
    ],
  },
];

export const SIDEBAR_NAV_ITEMS: SidebarNavItem[] = SIDEBAR_NAV_GROUPS.flatMap(
  (group) => group.items
);

export const ROUTES_WITH_SIDEBAR = SIDEBAR_NAV_ITEMS.map((item) => item.path);
export const CHROMELESS_ROUTES: string[] = ["/activation"];

export const TOP_NAV_ITEMS: TopNavItem[] = [
  { path: "/", labelKey: "Home" },
  { path: "/dashboard", labelKey: "Console", activePaths: ROUTES_WITH_SIDEBAR },
  { path: "/projects", labelKey: "Project Square" },
  { path: "/api-docs", labelKey: "API Docs" },
  { path: "/qna", labelKey: "FAQ" },
];
