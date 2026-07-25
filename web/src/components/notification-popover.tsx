import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Bell, LoaderCircle, RefreshCw, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import {
  getSystemAnnouncements,
  type SystemAnnouncement,
} from "@/lib/system-settings-api";
import { Button } from "@/components/ui/button";
import { HeaderActionButton } from "@/components/header-action-button";

const TABS = [
  { key: "notifications", labelKey: "Notifications" },
  { key: "system", labelKey: "System announcements" },
] as const;

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function getFocusableElements(container: HTMLElement) {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    .filter((element) => !element.hasAttribute("disabled") && element.offsetParent !== null);
}

export function NotificationPopover() {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<(typeof TABS)[number]["key"]>("system");
  const [open, setOpen] = useState(false);
  const [announcements, setAnnouncements] = useState<SystemAnnouncement[]>([]);
  const [announcementsLoading, setAnnouncementsLoading] = useState(false);
  const [announcementsError, setAnnouncementsError] = useState(false);
  const dialogRef = useRef<HTMLElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const requestRef = useRef<AbortController | null>(null);

  const loadAnnouncements = useCallback(async () => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setAnnouncementsLoading(true);
    setAnnouncementsError(false);
    try {
      const next = await getSystemAnnouncements(controller.signal);
      if (!controller.signal.aborted) setAnnouncements(next);
    } catch {
      if (!controller.signal.aborted) setAnnouncementsError(true);
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null;
        setAnnouncementsLoading(false);
      }
    }
  }, []);

  const openDialog = useCallback(() => {
    previousFocusRef.current = document.activeElement as HTMLElement | null;
    setOpen(true);
    void loadAnnouncements();
  }, [loadAnnouncements]);

  const closeDialog = useCallback(() => {
    requestRef.current?.abort();
    setOpen(false);
  }, []);

  useEffect(() => () => {
    requestRef.current?.abort();
    requestRef.current = null;
  }, []);

  useEffect(() => {
    if (!open) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        closeDialog();
        return;
      }

      if (event.key !== "Tab") return;

      const dialog = dialogRef.current;
      if (!dialog) return;

      const focusableElements = getFocusableElements(dialog);
      if (focusableElements.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }

      const firstElement = focusableElements[0];
      const lastElement = focusableElements[focusableElements.length - 1];

      if (event.shiftKey) {
        if (document.activeElement === firstElement || !dialog.contains(document.activeElement)) {
          event.preventDefault();
          lastElement.focus();
        }
        return;
      }

      if (document.activeElement === lastElement) {
        event.preventDefault();
        firstElement.focus();
      }
    };

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", handleKeyDown);

    window.setTimeout(() => {
      const dialog = dialogRef.current;
      if (!dialog) return;
      const firstElement = getFocusableElements(dialog)[0];
      (firstElement ?? dialog).focus();
    }, 0);

    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
      previousFocusRef.current?.focus();
    };
  }, [closeDialog, open]);

  const dialog = open
    ? createPortal(
        <div className="fixed inset-0 z-[1000]">
          <div
            aria-hidden="true"
            className="absolute inset-0 block h-full w-full bg-[rgba(22,22,26,0.6)]"
            onMouseDown={closeDialog}
          />

          <section
            ref={dialogRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby="announcement-title"
            tabIndex={-1}
            className={cn(
              "absolute left-1/2 top-20 flex h-[min(661px,calc(100svh-160px))] w-[min(920px,calc(100vw-32px))] -translate-x-1/2 flex-col",
              "rounded-[12px] border border-border bg-popover px-6 text-popover-foreground shadow-[0_0_1px_rgba(0,0,0,0.3),0_4px_14px_rgba(0,0,0,0.1)]"
            )}
          >
            <header className="flex min-h-[60px] items-center gap-4 py-6">
              <h2
                id="announcement-title"
                className="mr-auto text-base font-semibold text-foreground"
              >
                {t("System announcements")}
              </h2>

              <div className="flex items-center gap-2" role="tablist" aria-label={t("Announcement categories")}>
                {TABS.map((tab) => (
                  <button
                    key={tab.key}
                    type="button"
                    role="tab"
                    aria-selected={activeTab === tab.key}
                    onClick={() => setActiveTab(tab.key)}
                    className={cn(
                      "h-9 rounded-[10px] px-3 text-sm font-medium transition-colors",
                      activeTab === tab.key
                        ? "bg-brand-subtle text-brand"
                        : "text-muted-foreground hover:bg-surface-sunken hover:text-foreground"
                    )}
                  >
                    {t(tab.labelKey)}
                  </button>
                ))}
              </div>

              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6 rounded-[10px] text-foreground/80 hover:bg-surface-hover"
                aria-label={t("Close announcements")}
                onClick={closeDialog}
              >
                <X className="size-4" />
              </Button>
            </header>

            <div aria-live="polite" className="min-h-0 flex-1 overflow-auto pb-6">
              {activeTab === "notifications" ? (
                <EmptyAnnouncement
                  title={t("No notifications")}
                  description={t("New messages will appear here")}
                />
              ) : announcementsLoading ? (
                <div className="flex min-h-[320px] items-center justify-center gap-2 text-sm text-muted-foreground" role="status">
                  <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
                  {t("Loading announcements")}
                </div>
              ) : announcementsError ? (
                <div className="flex min-h-[320px] flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border bg-background px-6 text-center">
                  <p className="text-sm text-muted-foreground">{t("Failed to load announcements")}</p>
                  <Button variant="outline" size="sm" onClick={() => void loadAnnouncements()}>
                    <RefreshCw className="size-4" />
                    {t("Try again")}
                  </Button>
                </div>
              ) : announcements.length > 0 ? (
                <AnnouncementList announcements={announcements} />
              ) : (
                <EmptyAnnouncement
                  title={t("No system announcements")}
                  description={t("System announcements will appear here")}
                />
              )}
            </div>

            <footer className="flex items-center justify-end pb-6">
              <Button
                variant="ghost"
                size="sm"
                className="h-8 rounded-[10px] bg-surface-sunken px-3 text-brand hover:bg-brand-subtle hover:text-brand"
                onClick={closeDialog}
              >
                {t("Close announcements")}
              </Button>
            </footer>
          </section>
        </div>,
        document.body
      )
    : null;

  return (
    <>
      <HeaderActionButton aria-label={t("System announcements")} onClick={openDialog}>
        <Bell className="size-4" />
      </HeaderActionButton>
      {dialog}
    </>
  );
}

const ANNOUNCEMENT_TYPES: Record<SystemAnnouncement["type"], { label: string; dot: string }> = {
  default: { label: "General", dot: "bg-zinc-500" },
  ongoing: { label: "In progress", dot: "bg-blue-500" },
  success: { label: "Success", dot: "bg-emerald-500" },
  warning: { label: "Warning", dot: "bg-amber-500" },
  error: { label: "Error", dot: "bg-red-500" },
};

function AnnouncementList({ announcements }: { announcements: SystemAnnouncement[] }) {
  const { t } = useTranslation();

  return (
    <div className="space-y-3">
      {announcements.map((announcement) => {
        const type = ANNOUNCEMENT_TYPES[announcement.type];
        return (
          <article key={announcement.id} className="rounded-xl border border-border bg-background p-4 transition-colors hover:bg-surface-sunken/60">
            <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
              <span className={cn("mt-1.5 size-2.5 shrink-0 rounded-full", type.dot)} aria-hidden="true" />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="break-words text-sm font-semibold text-foreground">{announcement.title}</h3>
                  <span className="rounded-full bg-surface-sunken px-2 py-0.5 text-[11px] font-medium text-muted-foreground">
                    {t(type.label)}
                  </span>
                </div>
                <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-foreground/80">{announcement.content}</p>
              </div>
              <time className="shrink-0 text-xs text-muted-foreground" dateTime={announcement.startTime || undefined}>
                {announcement.startTime
                  ? new Date(announcement.startTime).toLocaleString(undefined, { hour12: false })
                  : t("Effective immediately")}
              </time>
            </div>
          </article>
        );
      })}
    </div>
  );
}

function EmptyAnnouncement({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="flex min-h-[320px] flex-col items-center justify-center rounded-lg border border-dashed border-border bg-background px-6 py-10 text-center">
      <div className="text-sm font-medium text-foreground">{title}</div>
      <div className="mt-1 text-xs text-muted-foreground">{description}</div>
    </div>
  );
}
