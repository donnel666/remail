import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Banner, Button, Card, Spin, Tag, Toast } from "@douyinfe/semi-ui";
import {
  CalendarClock,
  CheckCircle2,
  Gift,
  Sparkles,
} from "lucide-react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { useAuth } from "@/context/auth-provider";
import { storeLoginReturnTo } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { enterLottery, getPublicLottery, type PublicLottery } from "@/lib/lottery-api";

const statusLabelKeys: Record<string, string> = {
  funding: "Lottery status funding",
  open: "Lottery status open",
  settling: "Lottery status settling",
  completed: "Lottery status completed",
  cancelled: "Lottery status cancelled",
};

const confettiColors = ["#ff7a1a", "#ff3d73", "#facc15", "#38bdf8", "#a78bfa"];
const confetti = Array.from({ length: 20 }, (_, index) => ({
  color: confettiColors[index % confettiColors.length],
  delay: `${(index % 7) * 55}ms`,
  left: `${8 + ((index * 37) % 84)}%`,
  rotate: `${(index * 41) % 180}deg`,
  size: `${7 + (index % 4) * 2}px`,
  x: `${-180 + ((index * 61) % 360)}px`,
}));

function formatTime(value: string | null | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString(language);
}

function Celebration({ active }: { active: boolean }) {
  if (!active) return null;
  return (
    <div aria-hidden="true" className="lottery-celebration">
      {confetti.map((piece, index) => (
        <span
          className="lottery-confetti"
          key={index}
          style={
            {
              "--lottery-color": piece.color,
              "--lottery-delay": piece.delay,
              "--lottery-left": piece.left,
              "--lottery-rotate": piece.rotate,
              "--lottery-size": piece.size,
              "--lottery-x": piece.x,
            } as CSSProperties
          }
        />
      ))}
    </div>
  );
}

export default function Lottery() {
  const { i18n, t } = useTranslation();
  const language = i18n.resolvedLanguage ?? i18n.language;
  const navigate = useNavigate();
  const location = useLocation();
  const { currentUser } = useAuth();
  const token = useMemo(
    () => location.pathname.split("/").filter(Boolean)[1] ?? "",
    [location.pathname],
  );
  const [data, setData] = useState<PublicLottery | null>(null);
  const [loading, setLoading] = useState(true);
  const [entering, setEntering] = useState(false);
  const [entryError, setEntryError] = useState<string | null>(null);
  const [celebrating, setCelebrating] = useState(false);
  const celebrationTimer = useRef<number | undefined>(undefined);
  const loadRequestRef = useRef(0);

  const load = async (showLoading = true) => {
    if (!token) return;
    const requestID = ++loadRequestRef.current;
    if (showLoading) {
      setData(null);
      setEntryError(null);
      setLoading(true);
    }
    try {
      const nextData = await getPublicLottery(token);
      if (requestID === loadRequestRef.current) setData(nextData);
    } catch (error) {
      if (requestID === loadRequestRef.current) {
        Toast.error(getIamErrorMessage(t, error, "Lottery is unavailable."));
      }
    } finally {
      if (showLoading && requestID === loadRequestRef.current) {
        setLoading(false);
      }
    }
  };

  useEffect(() => {
    void load();
    // Reload when the authenticated identity changes so one account's payout state
    // cannot remain visible after logout or account switching.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentUser?.id, token]);

  useEffect(
    () => () => {
      if (celebrationTimer.current !== undefined) {
        window.clearTimeout(celebrationTimer.current);
      }
    },
    [],
  );

  const celebrate = () => {
    if (celebrationTimer.current !== undefined) {
      window.clearTimeout(celebrationTimer.current);
    }
    setCelebrating(true);
    celebrationTimer.current = window.setTimeout(() => {
      setCelebrating(false);
      celebrationTimer.current = undefined;
    }, 2600);
  };

  const enter = async () => {
    if (!currentUser) {
      storeLoginReturnTo();
      void navigate({ to: "/login" });
      return;
    }
    setEntryError(null);
    const turnstileToken = await requireTurnstile("lottery_enter");
    if (!turnstileToken) return;
    setEntering(true);
    try {
      await enterLottery(token, turnstileToken);
      celebrate();
      Toast.success(t("Lottery entry succeeded."));
      await load(false);
    } catch (error) {
      const message = getIamErrorMessage(t, error, "Lottery entry failed.");
      setEntryError(message);
      Toast.error(message);
    } finally {
      setEntering(false);
    }
  };

  if (loading) {
    return (
      <div
        aria-label={t("Loading")}
        className="flex min-h-screen items-center justify-center bg-[var(--canvas)]"
        role="status"
      >
        <Spin size="large" />
      </div>
    );
  }
  if (!data) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--canvas)] px-4">
        <Banner type="danger" description={t("Lottery is unavailable.")} />
      </div>
    );
  }

  const lottery = data.lottery;
  const closed = lottery.status !== "open";
  const completed = lottery.status === "completed";
  const statusColor =
    lottery.status === "open"
      ? "blue"
      : lottery.status === "completed"
        ? "green"
        : lottery.status === "settling"
          ? "orange"
          : "grey";
  const canEnter = !closed && !data.hasEntered;

  return (
    <main className="relative min-h-screen overflow-hidden bg-[var(--canvas)] px-4 py-8 sm:px-6 sm:py-14">
      <Celebration active={celebrating} />
      <div className="pointer-events-none absolute inset-x-0 top-0 h-72 bg-[radial-gradient(circle_at_top,rgba(255,122,26,0.18),transparent_68%)]" />
      <div className="relative mx-auto w-full max-w-xl">
        <div className="mb-6 flex items-center justify-center gap-2 text-sm font-medium text-[var(--ink-muted)]">
          <Gift aria-hidden="true" className="text-brand" size={17} />
          <span>{t("Activity Center")}</span>
        </div>

        <Card
          bodyStyle={{ padding: 0 }}
          className="overflow-hidden !rounded-3xl border border-[var(--semi-color-border)] !bg-[var(--surface)] shadow-[0_24px_70px_rgba(31,41,55,0.12)]"
        >
          <div className="border-b border-[var(--semi-color-border)] px-6 pb-7 pt-6 sm:px-9 sm:pt-8">
            <div className="mb-7 flex items-center justify-between gap-3">
              <Tag color={statusColor} shape="circle">
                {t(statusLabelKeys[lottery.status] ?? lottery.status)}
              </Tag>
              <Sparkles aria-hidden="true" className="text-brand/70" size={19} />
            </div>
            <h1 className="break-words text-3xl font-semibold tracking-tight text-[var(--ink)] sm:text-4xl">
              {lottery.title}
            </h1>
            {completed || lottery.status === "open" ? (
              <p className="mt-3 max-w-md text-sm leading-6 text-[var(--ink-muted)]">
                {completed ? t("Lottery is completed") : t("Lottery is open")}
              </p>
            ) : null}
          </div>

          <div className="space-y-6 px-6 py-7 sm:px-9 sm:py-8">
            <div className="rounded-2xl bg-[linear-gradient(135deg,var(--brand-subtle),color-mix(in_oklch,var(--brand-light)_35%,transparent))] px-5 py-5 sm:px-6">
              <div className="text-sm font-medium text-[var(--ink-muted)]">
                {t("Total prize pool")}
              </div>
              <div className="mt-2 flex items-baseline gap-2">
                <span className="text-4xl font-bold tracking-tight text-[var(--ink)] sm:text-5xl">
                  {lottery.totalAmount}
                </span>
                <span className="text-sm font-medium text-[var(--ink-muted)]">
                  {t("Points")}
                </span>
              </div>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="rounded-xl border border-[var(--semi-color-border)] px-4 py-4">
                <div className="flex items-center gap-2 text-sm text-[var(--ink-muted)]">
                  <CalendarClock aria-hidden="true" size={16} />
                  <span>{t("Draw time")}</span>
                </div>
                <div className="mt-2 text-sm font-semibold leading-6 text-[var(--ink)]">
                  {lottery.drawAt
                    ? formatTime(lottery.drawAt, language)
                    : completed
                      ? t("Lottery status completed")
                      : t("Draw time pending")}
                </div>
              </div>
              <div className="rounded-xl border border-[var(--semi-color-border)] px-4 py-4">
                <div className="flex items-center gap-2 text-sm text-[var(--ink-muted)]">
                  <CheckCircle2 aria-hidden="true" size={16} />
                  <span>{t("Participation")}</span>
                </div>
                <div className="mt-2 text-sm font-semibold leading-6 text-[var(--ink)]">
                  {data.hasEntered
                    ? t("You have entered.")
                    : lottery.status === "open"
                      ? t("Open to eligible accounts")
                      : t("Entry closed")}
                </div>
              </div>
            </div>

            {entryError ? (
              <Banner
                aria-live="polite"
                className="!rounded-xl"
                description={entryError}
                type="danger"
              />
            ) : null}

            {completed && data.myPayout ? (
              <div
                aria-live="polite"
                className="lottery-result-card rounded-2xl border border-emerald-200 bg-emerald-50 px-5 py-5 dark:border-emerald-900/60 dark:bg-emerald-950/30"
              >
                <div className="flex items-start gap-3">
                  <CheckCircle2 className="mt-0.5 shrink-0 text-emerald-600" size={22} />
                  <div>
                    <div className="text-sm font-medium text-emerald-800 dark:text-emerald-300">
                      {t("Congratulations")}
                    </div>
                    <div className="mt-1 text-xl font-bold text-emerald-900 dark:text-emerald-200">
                      {t("Lottery prize amount", { amount: data.myPayout.amount })}
                    </div>
                  </div>
                </div>
              </div>
            ) : null}

            {lottery.status === "cancelled" ? (
              <Banner
                aria-live="polite"
                className="!rounded-xl"
                description={t(
                  "This lottery had no eligible participants, so no budget was awarded.",
                )}
                type="warning"
              />
            ) : null}

            {data.hasEntered && !completed && lottery.status !== "cancelled" ? (
              <Banner
                aria-live="polite"
                className="!rounded-xl"
                description={t("You have entered. Please wait for the draw.")}
                type="info"
              />
            ) : null}

            <Button
              block
              className="!h-12 !rounded-xl !font-semibold shadow-sm transition-transform hover:-translate-y-0.5 focus-visible:ring-2 focus-visible:ring-[var(--brand)] focus-visible:ring-offset-2"
              disabled={!canEnter}
              loading={entering}
              onClick={() => void enter()}
              size="large"
              theme="solid"
              type="primary"
            >
              {t(
                closed
                  ? "Entry closed"
                  : !currentUser
                    ? "Log in to enter"
                    : data.hasEntered
                      ? "Entered"
                      : "Join lottery",
              )}
            </Button>
          </div>
        </Card>
      </div>
    </main>
  );
}
