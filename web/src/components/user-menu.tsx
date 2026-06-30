import { Link } from "@tanstack/react-router";
import { ChevronDown, KeyRound, LogOut, UserRound, Wallet } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-provider";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

function getInitial(name: string) {
  return name.trim().charAt(0).toUpperCase() || "R";
}

export function UserMenu() {
  const { t } = useTranslation();
  const { currentUser, logout } = useAuth();

  if (!currentUser) return null;

  return (
    <DropdownMenu modal={false} openOnHover>
      <DropdownMenuTrigger
        className={cn(
          "flex h-8 items-center gap-1.5 rounded-full bg-surface-sunken py-1 pl-1 pr-2 text-sm font-medium text-foreground transition-colors",
          "hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        )}
        aria-label={t("User menu")}
      >
        <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-brand-subtle text-xs font-semibold text-brand">
          {getInitial(currentUser.name)}
        </span>
        <span className="hidden max-w-[120px] truncate md:inline">
          {currentUser.name}
        </span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        sideOffset={8}
        className="w-48 rounded-lg border-0 bg-popover p-0 shadow-[0_0_1px_rgba(0,0,0,0.3),0_4px_14px_rgba(0,0,0,0.1)]"
      >
        <div className="px-3 py-2">
          <div className="truncate text-sm font-semibold text-foreground">
            {currentUser.name}
          </div>
          <div className="truncate text-xs text-muted-foreground">
            {currentUser.email}
          </div>
        </div>
        <DropdownMenuSeparator className="mx-0 bg-border" />
        <DropdownMenuItem
          render={<Link to="/account" />}
          className="h-9 rounded-none px-3 text-sm text-foreground hover:bg-surface-sunken"
        >
          <UserRound className="size-4 text-muted-foreground" />
          {t("Personal Settings")}
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/apikeys" />}
          className="h-9 rounded-none px-3 text-sm text-foreground hover:bg-surface-sunken"
        >
          <KeyRound className="size-4 text-muted-foreground" />
          {t("API KEY")}
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/financial" />}
          className="h-9 rounded-none px-3 text-sm text-foreground hover:bg-surface-sunken"
        >
          <Wallet className="size-4 text-muted-foreground" />
          {t("Wallet Management")}
        </DropdownMenuItem>
        <DropdownMenuSeparator className="mx-0 bg-border" />
        <DropdownMenuItem
          onClick={() => void logout()}
          className="h-9 rounded-none px-3 text-sm text-foreground hover:bg-surface-sunken"
        >
          <LogOut className="size-4 text-muted-foreground" />
          {t("Exit")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
