import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Banner, Button, Card, Spin, Tag, Toast } from "@douyinfe/semi-ui";
import { CalendarClock, CheckCircle2, Gift, Users } from "lucide-react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { useAuth } from "@/context/auth-provider";
import { storeLoginReturnTo } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { enterLottery, getPublicLottery, type PublicLottery } from "@/lib/lottery-api";
import { formatPoints, formatPointsValue } from "@/lib/points";

const statusLabelKeys: Record<string, string> = {
  funding: "Lottery status funding",
  open: "Lottery status open",
  settling: "Lottery status settling",
  completed: "Lottery status completed",
  cancelled: "Lottery status cancelled",
};

const confettiColors = ["#ff7a1a", "#ff3d73", "#facc15", "#38bdf8", "#a78bfa"];
const celebrationDurationMs = 12_000;
const celebrationCleanupGraceMs = 400;
const confettiCount = 120;
const confettiMinDurationMs = 2_800;
const confettiMaxDurationMs = 4_200;
const confettiDelayCurve = 1.7;
const confettiLastDelayMs =
  celebrationDurationMs - confettiMaxDurationMs - celebrationCleanupGraceMs;
const confetti = Array.from({ length: confettiCount }, (_, index) => {
  const progress = index / (confettiCount - 1);
  const jitterMs =
    index === 0 || index === confettiCount - 1
      ? 0
      : ((index * 53) % 71) - 35;
  const delayMs = Math.max(
    0,
    Math.min(
      confettiLastDelayMs,
      Math.round(
        Math.pow(progress, confettiDelayCurve) * confettiLastDelayMs + jitterMs,
      ),
    ),
  );
  const durationMs =
    index === confettiCount - 1
      ? confettiMaxDurationMs
      : confettiMinDurationMs +
        ((index * 83) % (confettiMaxDurationMs - confettiMinDurationMs));
  const drift = -150 + ((index * 97) % 301);
  const sway = 20 + ((index * 29) % 41);
  const spin = (index % 2 === 0 ? 1 : -1) * (540 + ((index * 67) % 361));
  return {
    color: confettiColors[index % confettiColors.length],
    delay: `${delayMs}ms`,
    duration: `${durationMs}ms`,
    left: `${8 + ((index * 37) % 84)}%`,
    spinEnd: `${Math.round(spin * 1.8)}deg`,
    spinLate: `${Math.round(spin * 0.74)}deg`,
    spinMid: `${Math.round(spin * 0.42)}deg`,
    spinFinal: `${Math.round(spin * 1.35)}deg`,
    size: `${7 + (index % 4) * 2}px`,
    top: `${-18 - ((index * 71) % 56)}px`,
    x1: `${drift + sway}px`,
    x2: `${drift - sway}px`,
    x3: `${drift + (index % 3 === 0 ? sway / 2 : -sway / 2)}px`,
    x4: `${drift + (index % 2 === 0 ? sway : -sway)}px`,
  };
});

function formatTime(value: string | null | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString(language);
}

type CountdownParts = {
  hours: number;
  minutes: number;
  seconds: number;
  expired: boolean;
} | null;

function getCountdownParts(
  drawAt: string | null | undefined,
  now: number,
): CountdownParts {
  if (!drawAt) return null;
  const deadline = new Date(drawAt).getTime();
  if (!Number.isFinite(deadline)) return null;

  const remainingSeconds = Math.max(0, Math.floor((deadline - now) / 1000));
  return {
    hours: Math.floor(remainingSeconds / 3600),
    minutes: Math.floor((remainingSeconds % 3600) / 60),
    seconds: remainingSeconds % 60,
    expired: deadline <= now,
  };
}

function padCountdown(value: number) {
  return String(value).padStart(2, "0");
}

function formatParticipantCount(
  count: number | null | undefined,
  target: number | null | undefined,
  language: string,
) {
  const format = (value: number | null | undefined) =>
    (
      typeof value === "number" && Number.isFinite(value) ? Math.max(0, value) : 0
    ).toLocaleString(language);
  const current = format(count);
  return target == null ? current : `${current}/${format(target)}`;
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
              "--lottery-duration": piece.duration,
              "--lottery-left": piece.left,
              "--lottery-spin-end": piece.spinEnd,
              "--lottery-spin-final": piece.spinFinal,
              "--lottery-spin-late": piece.spinLate,
              "--lottery-spin-mid": piece.spinMid,
              "--lottery-size": piece.size,
              "--lottery-top": piece.top,
              "--lottery-x1": piece.x1,
              "--lottery-x2": piece.x2,
              "--lottery-x3": piece.x3,
              "--lottery-x4": piece.x4,
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
  const [clockNow, setClockNow] = useState(() => Date.now());
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

  useEffect(() => {
    const drawAt = data?.lottery.drawAt;
    if (!drawAt || data?.lottery.status !== "open") return;
    const deadline = new Date(drawAt).getTime();
    if (!Number.isFinite(deadline)) return;

    let timer: number | undefined;
    const tick = () => {
      const current = Date.now();
      setClockNow(current);
      if (current >= deadline && timer !== undefined) {
        window.clearInterval(timer);
      }
    };
    timer = window.setInterval(tick, 1000);
    tick();

    return () => {
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [data?.lottery.drawAt, data?.lottery.status]);

  const celebrate = () => {
    if (celebrationTimer.current !== undefined) {
      window.clearTimeout(celebrationTimer.current);
    }
    setCelebrating(true);
    celebrationTimer.current = window.setTimeout(() => {
      setCelebrating(false);
      celebrationTimer.current = undefined;
    }, celebrationDurationMs);
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

  const countdown = getCountdownParts(data?.lottery.drawAt, clockNow);

  // `load` stays local to this page so the request guard can discard stale
  // responses when the route or authenticated user changes.
  useEffect(() => {
    const status = data?.lottery.status;
    if (
      !countdown?.expired ||
      (status !== "open" && status !== "settling") ||
      !data?.lottery.drawAt
    ) {
      return;
    }

    let attempts = 0;
    let timer: number | undefined;
    const refresh = () => {
      attempts += 1;
      void load(false);
      if (attempts >= 10 && timer !== undefined) {
        window.clearInterval(timer);
      }
    };
    timer = window.setInterval(refresh, 3000);
    refresh();

    // ponytail: cap post-draw polling at 30s; use push updates if settlement latency grows.
    return () => {
      if (timer !== undefined) window.clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [countdown?.expired, data?.lottery.drawAt, data?.lottery.status, token]);

  if (loading) {
    return (
      <div
        aria-label={t("Loading")}
        className="flex min-h-svh items-center justify-center bg-white dark:bg-[var(--canvas)]"
        role="status"
      >
        <Spin size="large" />
      </div>
    );
  }
  if (!data) {
    return (
      <div className="flex min-h-svh items-center justify-center bg-white px-4 dark:bg-[var(--canvas)]">
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
  const showCountdown =
    lottery.status === "open" && Boolean(countdown) && !countdown?.expired;
  const showDrawInfo = Boolean(lottery.drawAt);
  const compactTotalAmount = formatPoints(lottery.totalAmount);
  const exactTotalAmount = formatPointsValue(lottery.totalAmount);
  const participantDisplay = formatParticipantCount(
    lottery.participantCount,
    lottery.participantTarget,
    language,
  );

  return (
    <main className="relative min-h-svh overflow-hidden bg-white px-4 py-8 sm:px-6 sm:py-12 dark:bg-[var(--canvas)]">
      <Celebration active={celebrating} />
      <div className="relative mx-auto w-full max-w-md">
        <div className="mb-5 flex items-center justify-between gap-3 px-1 text-sm font-medium text-[var(--ink-muted)]">
          <div className="flex items-center gap-2">
            <Gift aria-hidden="true" className="text-brand" size={17} />
            <span>{t("Activity Center")}</span>
          </div>
          <Tag color={statusColor} shape="circle" size="small">
            {t(statusLabelKeys[lottery.status] ?? lottery.status)}
          </Tag>
        </div>

        <Card
          bodyStyle={{ padding: 0 }}
          className="overflow-hidden !rounded-[28px] border border-[var(--semi-color-border)] !bg-white shadow-[0_12px_32px_rgba(31,41,55,0.1)] dark:!bg-[var(--surface)]"
        >
          <div className="border-b border-[var(--semi-color-border)] bg-white px-6 pb-7 pt-8 text-center dark:bg-[var(--surface)] sm:px-8">
            <div className="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-2xl bg-white text-brand shadow-sm ring-1 ring-brand/15 dark:bg-[var(--surface-sunken)]">
              <Gift aria-hidden="true" size={31} strokeWidth={1.8} />
            </div>
            <h1 className="break-words text-2xl font-semibold tracking-tight text-[var(--ink-primary)] sm:text-3xl">
              {lottery.title}
            </h1>
            <p className="mt-2 text-sm leading-6 text-[var(--ink-muted)]">
              {completed
                ? t("Lottery is completed")
                : lottery.status === "open"
                  ? t("Lottery is open")
                  : t(statusLabelKeys[lottery.status] ?? lottery.status)}
            </p>
          </div>

          <div className="space-y-5 px-5 py-6 sm:px-7 sm:py-7">
            <div
              aria-label={t("Total prize pool: {{amount}} points", {
                amount: exactTotalAmount,
              })}
              className="rounded-2xl border border-brand/20 bg-white px-5 py-5 text-center dark:bg-[var(--surface)] sm:px-6"
              title={exactTotalAmount}
            >
              <div className="text-sm font-medium text-[var(--ink-muted)]">
                {t("Total prize pool")}
              </div>
              <div className="mt-2 flex items-baseline justify-center gap-2">
                <span className="font-mono-data text-5xl font-bold tracking-tight text-[var(--ink-primary)] sm:text-6xl">
                  {compactTotalAmount}
                </span>
                <span className="text-sm font-medium text-[var(--ink-muted)]">
                  {t("Points")}
                </span>
              </div>
            </div>

            <div
              aria-label={t("Participants ({{count}})", { count: participantDisplay })}
              className="flex items-center justify-between gap-4 rounded-2xl border border-[var(--semi-color-border)] bg-[var(--surface)] px-4 py-4"
              role="group"
            >
              <div className="flex items-center gap-2 text-sm font-medium text-[var(--ink-muted)]">
                <Users aria-hidden="true" size={17} />
                <span>{t("Participants")}</span>
              </div>
              <span className="whitespace-nowrap font-mono-data text-2xl font-semibold tabular-nums text-[var(--ink-primary)]">
                {participantDisplay}
              </span>
            </div>

            {showDrawInfo ? (
              <div
                aria-live={showCountdown ? "off" : "polite"}
                className="rounded-2xl border border-[var(--semi-color-border)] bg-[var(--surface-sunken)] px-4 py-4 text-center"
              >
                <div className="flex items-center justify-center gap-2 text-sm font-medium text-[var(--ink-muted)]">
                  <CalendarClock aria-hidden="true" size={16} />
                  <span>{showCountdown ? t("Lottery countdown") : t("Draw time")}</span>
                </div>
                {showCountdown && countdown ? (
                  <div
                    aria-label={t("Lottery countdown")}
                    className="mt-4 grid grid-cols-3 gap-2"
                    role="timer"
                  >
                    {[
                      [countdown.hours, t("Hours short")],
                      [countdown.minutes, t("Minutes short")],
                      [countdown.seconds, t("Seconds short")],
                    ].map(([value, label]) => (
                      <div
                        className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--surface)] px-2 py-3"
                        key={String(label)}
                      >
                        <div className="font-mono-data text-3xl font-semibold tabular-nums text-[var(--ink-primary)] sm:text-4xl">
                          {padCountdown(Number(value))}
                        </div>
                        <div className="mt-1 text-xs text-[var(--ink-muted)]">{label}</div>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="mt-3 text-base font-semibold text-[var(--ink-primary)]">
                    {lottery.status === "settling"
                      ? t("Lottery status settling")
                      : completed
                        ? formatTime(lottery.drawAt, language)
                        : countdown?.expired
                          ? t("Lottery status settling")
                          : t("Draw time pending")}
                  </div>
                )}
                {showCountdown ? (
                  <div className="mt-3 text-xs text-[var(--ink-muted)]">
                    {formatTime(lottery.drawAt, language)}
                  </div>
                ) : null}
              </div>
            ) : null}

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
                      {t("Lottery prize amount", { amount: formatPoints(data.myPayout.amount) })}
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
              className="!h-12 !cursor-pointer !rounded-xl !font-semibold shadow-sm transition-transform hover:-translate-y-0.5 focus-visible:ring-2 focus-visible:ring-[var(--brand)] focus-visible:ring-offset-2 disabled:!cursor-not-allowed"
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
