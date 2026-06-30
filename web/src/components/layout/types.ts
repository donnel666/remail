import type { LucideIcon } from "lucide-react";

export interface TopNavItem {
  path: string;
  labelKey: string;
  activePaths?: string[];
}

export interface SidebarNavItem {
  path: string;
  labelKey: string;
  icon: LucideIcon;
}

export interface SidebarNavGroup {
  labelKey: string;
  items: SidebarNavItem[];
}
