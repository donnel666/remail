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
  minRoleLevel?: number;
}

export interface SidebarNavGroup {
  id: string;
  labelKey: string;
  items: SidebarNavItem[];
  minRoleLevel?: number;
}
