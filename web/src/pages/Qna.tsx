import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronDown, HelpCircle, LifeBuoy, Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { getSystemFAQs, type SystemFAQ } from "@/lib/system-settings-api";

export default function Qna() {
  const { t } = useTranslation();
  const [items, setItems] = useState<SystemFAQ[]>([]);
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [query, setQuery] = useState("");
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setFailed(false);
    void getSystemFAQs(controller.signal)
      .then((response) => {
        setEnabled(response.enabled);
        setItems(response.items);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [reloadKey]);

  const filteredItems = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return items;
    return items.filter((item) =>
      `${item.question}\n${item.answer}`.toLocaleLowerCase().includes(needle),
    );
  }, [items, query]);

  return (
    <div className="min-h-[calc(100svh-64px)] bg-background">
      <section className="border-b border-border bg-gradient-to-b from-brand-subtle/55 to-background px-4 py-12 sm:px-6 sm:py-16">
        <div className="mx-auto max-w-3xl text-center">
          <span className="mx-auto flex size-11 items-center justify-center rounded-2xl bg-brand text-white shadow-sm">
            <HelpCircle className="size-5" aria-hidden="true" />
          </span>
          <p className="mt-4 text-sm font-semibold text-brand">{t("Help center")}</p>
          <h1 className="mt-2 text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
            {t("Frequently asked questions")}
          </h1>
          <p className="mx-auto mt-3 max-w-xl text-base leading-7 text-muted-foreground">
            {t("Find quick answers to common account, order, and service questions.")}
          </p>
          <label className="relative mx-auto mt-7 block max-w-xl" htmlFor="faq-search">
            <span className="sr-only">{t("Search questions")}</span>
            <Search className="pointer-events-none absolute left-4 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <input
              id="faq-search"
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Search questions")}
              className="h-12 w-full rounded-xl border border-input bg-background pl-11 pr-4 text-base text-foreground shadow-sm outline-none transition-colors placeholder:text-muted-foreground focus:border-brand focus:ring-2 focus:ring-brand/20"
            />
          </label>
        </div>
      </section>

      <div className="mx-auto w-full max-w-4xl px-4 py-8 sm:px-6 sm:py-10">
        {loading ? <FAQSkeleton /> : failed ? (
          <EmptyState title={t("FAQ load failed.")} description={t("Please try again in a moment.")}>
            <Button onClick={() => setReloadKey((key) => key + 1)}>{t("Try again")}</Button>
          </EmptyState>
        ) : !enabled ? (
          <EmptyState title={t("FAQ is currently unavailable")} description={t("Please check back later.")} />
        ) : items.length === 0 ? (
          <EmptyState title={t("No FAQ entries available")} description={t("Published answers will appear here.")} />
        ) : filteredItems.length === 0 ? (
          <EmptyState title={t("No matching questions")} description={t("Try another keyword.")} />
        ) : (
          <section aria-label={t("Frequently asked questions")} className="overflow-hidden rounded-2xl border border-border bg-card shadow-sm">
            {filteredItems.map((item) => (
              <details key={item.id} className="group border-b border-border last:border-b-0 open:bg-surface-sunken/45">
                <summary className="flex min-h-16 cursor-pointer list-none items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-surface-sunken/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-brand sm:px-6 [&::-webkit-details-marker]:hidden">
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-brand-subtle text-sm font-bold text-brand" aria-hidden="true">Q</span>
                  <span className="min-w-0 flex-1 break-words text-base font-semibold leading-6 text-foreground">{item.question}</span>
                  <ChevronDown className="size-5 shrink-0 text-muted-foreground transition-transform duration-200 group-open:rotate-180 motion-reduce:transition-none" aria-hidden="true" />
                </summary>
                <div className="px-5 pb-5 pl-[4.25rem] sm:px-6 sm:pb-6 sm:pl-[4.75rem]">
                  <p className="whitespace-pre-wrap break-words text-sm leading-7 text-foreground/75">{item.answer}</p>
                </div>
              </details>
            ))}
          </section>
        )}

        <section className="mt-8 flex flex-col items-start justify-between gap-4 rounded-2xl border border-border bg-surface-sunken/55 p-5 sm:flex-row sm:items-center sm:p-6">
          <div className="flex items-start gap-3">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-brand-subtle text-brand">
              <LifeBuoy className="size-5" aria-hidden="true" />
            </span>
            <div>
              <h2 className="font-semibold text-foreground">{t("Still need help?")}</h2>
              <p className="mt-1 text-sm leading-6 text-muted-foreground">{t("Submit a support ticket and our team will follow up.")}</p>
            </div>
          </div>
          <Button className="h-11 w-full bg-brand-active text-white hover:bg-brand-active hover:brightness-90 dark:bg-brand-light dark:hover:bg-brand-light sm:w-auto" render={<Link to="/tickets" />}>
            {t("Submit ticket")}
          </Button>
        </section>
      </div>
    </div>
  );
}

function FAQSkeleton() {
  return <div aria-label="loading" className="space-y-3">
    {[0, 1, 2].map((item) => <Skeleton key={item} className="h-20 w-full rounded-2xl" />)}
  </div>;
}

function EmptyState({ title, description, children }: { title: string; description: string; children?: React.ReactNode }) {
  return <div className="flex min-h-64 flex-col items-center justify-center rounded-2xl border border-dashed border-border bg-card px-6 text-center">
    <HelpCircle className="size-9 text-muted-foreground" aria-hidden="true" />
    <h2 className="mt-4 text-lg font-semibold text-foreground">{title}</h2>
    <p className="mt-2 text-sm leading-6 text-muted-foreground">{description}</p>
    {children ? <div className="mt-4">{children}</div> : null}
  </div>;
}
