import { useEffect, useMemo, useState } from "react";
import { Banner, Button, Card, Spin, Tag, Toast } from "@douyinfe/semi-ui";
import { Clock3, Gift, Users } from "lucide-react";
import { useNavigate, useLocation } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { useAuth } from "@/context/auth-provider";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { enterLottery, getPublicLottery, type PublicLottery } from "@/lib/lottery-api";
import { storeLoginReturnTo } from "@/lib/auth-flow";

const statusLabelKeys: Record<string, string> = {
  funding: "Lottery status funding",
  open: "Lottery status open",
  settling: "Lottery status settling",
  completed: "Lottery status completed",
  cancelled: "Lottery status cancelled",
};

const tierLabelKeys: Record<string, string> = {
  consolation: "Consolation prize",
  normal: "Regular prize",
  lucky: "Lucky prize",
};

function formatTime(value: string | null | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString(language);
}

export default function Lottery() {
  const { i18n, t } = useTranslation();
  const language = i18n.resolvedLanguage ?? i18n.language;
  const navigate = useNavigate();
  const location = useLocation();
  const { currentUser } = useAuth();
  const token = useMemo(() => location.pathname.split("/").filter(Boolean)[1] ?? "", [location.pathname]);
  const [data, setData] = useState<PublicLottery | null>(null);
  const [loading, setLoading] = useState(true);
  const [entering, setEntering] = useState(false);

  const load = async () => {
    if (!token) return;
    setLoading(true);
    try {
      setData(await getPublicLottery(token));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Lottery is unavailable."));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
    // The token is the only route input; a refresh after entering uses the same resource.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  const enter = async () => {
    if (!currentUser) {
      storeLoginReturnTo();
      void navigate({ to: "/login" });
      return;
    }
    const turnstileToken = await requireTurnstile("lottery_enter");
    if (!turnstileToken) return;
    setEntering(true);
    try {
      await enterLottery(token, turnstileToken);
      Toast.success(t("Lottery entry succeeded."));
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Lottery entry failed."));
    } finally {
      setEntering(false);
    }
  };

  if (loading) return <div className="flex min-h-screen items-center justify-center"><Spin size="large" /></div>;
  if (!data) return <div className="flex min-h-screen items-center justify-center px-4"><Banner type="danger" description={t("Lottery is unavailable.")} /></div>;

  const lottery = data.lottery;
  const target = lottery.participantTarget ?? lottery.maxParticipants;
  const progress = Math.min(100, target > 0 ? (lottery.participantCount / target) * 100 : 0);
  const closed = lottery.status !== "open";
  const canEnter = !closed && !data.hasEntered;

  return (
    <main className="min-h-screen bg-[var(--semi-color-fill-0)] px-4 py-8 sm:px-6">
      <div className="mx-auto w-full max-w-2xl">
        <Card className="!rounded-2xl" bodyStyle={{ padding: 0 }}>
          <div className="border-b border-[var(--semi-color-border)] px-5 py-6 sm:px-8">
            <div className="mb-3 flex items-center justify-between gap-3">
              <Tag color={closed ? "grey" : "blue"}>{t(statusLabelKeys[lottery.status] ?? lottery.status)}</Tag>
              <span className="text-xs text-[var(--ink-muted)]">#{lottery.id}</span>
            </div>
            <div className="flex items-start gap-3">
              <div className="rounded-xl bg-brand/10 p-3 text-brand"><Gift size={24} /></div>
              <div className="min-w-0">
                <h1 className="break-words text-2xl font-semibold text-[var(--ink)]">{lottery.title}</h1>
                <p className="mt-1 text-sm text-[var(--ink-muted)]">{t("Total prize pool: {{amount}} points", { amount: lottery.totalAmount })}</p>
              </div>
            </div>
          </div>
          <div className="grid gap-5 px-5 py-6 sm:px-8">
            <div>
              <div className="mb-2 flex items-center justify-between text-sm">
                <span className="flex items-center gap-1.5 text-[var(--ink-muted)]"><Users size={15} /> {t("Participant count")}</span>
                <span className="font-medium text-[var(--ink)]">{lottery.participantCount} / {target}</span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-[var(--semi-color-fill-2)]">
                <div className="h-full rounded-full bg-brand transition-[width] duration-300" style={{ width: `${progress}%` }} />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">{t("Per-person range")}</div><div className="mt-1 font-medium">{lottery.minPayout} - {lottery.maxPayout}</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">{t("Consolation prize share")}</div><div className="mt-1 font-medium">{lottery.tierWeights.consolation}%</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">{t("Account requirement")}</div><div className="mt-1 font-medium">{t("At least {{days}} days", { days: lottery.minAccountAgeDays })}</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">{t("Draw time")}</div><div className="mt-1 flex items-center gap-1 font-medium"><Clock3 size={14} />{formatTime(lottery.drawAt, language)}</div></div>
            </div>
            {lottery.status === "completed" && data.myPayout ? (
              <Banner type="success" title={t("Congratulations")} description={t("Lottery prize result", { amount: data.myPayout.amount, tier: t(tierLabelKeys[data.myPayout.tier] ?? data.myPayout.tier) })} />
            ) : null}
            {data.hasEntered && lottery.status !== "completed" ? <Banner type="info" description={t("You have entered. Please wait for the draw.")} /> : null}
            {lottery.status === "cancelled" ? <Banner type="warning" description={t("This lottery had no eligible participants, so no budget was awarded.")} /> : null}
            <Button block disabled={!canEnter} loading={entering} onClick={() => void enter()} size="large" theme="solid" type="primary">
              {t(!currentUser ? "Log in to enter" : data.hasEntered ? "Entered" : closed ? "Entry closed" : "Enter lottery")}
            </Button>
          </div>
        </Card>
      </div>
    </main>
  );
}
