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

const statusLabel: Record<string, string> = {
  funding: "准备中",
  open: "进行中",
  settling: "开奖中",
  completed: "已开奖",
  cancelled: "已取消",
};

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

export default function Lottery() {
  const { t } = useTranslation();
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
      Toast.error(getIamErrorMessage(t, error, "抽奖不存在或已失效"));
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
      Toast.success("报名成功");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "报名失败"));
    } finally {
      setEntering(false);
    }
  };

  if (loading) return <div className="flex min-h-screen items-center justify-center"><Spin size="large" /></div>;
  if (!data) return <div className="flex min-h-screen items-center justify-center px-4"><Banner type="danger" description="抽奖不存在或已失效" /></div>;

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
              <Tag color={closed ? "grey" : "blue"}>{statusLabel[lottery.status] ?? lottery.status}</Tag>
              <span className="text-xs text-[var(--ink-muted)]">#{lottery.id}</span>
            </div>
            <div className="flex items-start gap-3">
              <div className="rounded-xl bg-brand/10 p-3 text-brand"><Gift size={24} /></div>
              <div className="min-w-0">
                <h1 className="break-words text-2xl font-semibold text-[var(--ink)]">{lottery.title}</h1>
                <p className="mt-1 text-sm text-[var(--ink-muted)]">总奖池 {lottery.totalAmount} 积分</p>
              </div>
            </div>
          </div>
          <div className="grid gap-5 px-5 py-6 sm:px-8">
            <div>
              <div className="mb-2 flex items-center justify-between text-sm">
                <span className="flex items-center gap-1.5 text-[var(--ink-muted)]"><Users size={15} /> 参与人数</span>
                <span className="font-medium text-[var(--ink)]">{lottery.participantCount} / {target}</span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-[var(--semi-color-fill-2)]">
                <div className="h-full rounded-full bg-brand transition-[width] duration-300" style={{ width: `${progress}%` }} />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">单人范围</div><div className="mt-1 font-medium">{lottery.minPayout} - {lottery.maxPayout}</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">安慰奖比例</div><div className="mt-1 font-medium">{lottery.tierWeights.consolation}%</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">账号要求</div><div className="mt-1 font-medium">≥ {lottery.minAccountAgeDays} 天</div></div>
              <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><div className="text-xs text-[var(--ink-muted)]">开奖时间</div><div className="mt-1 flex items-center gap-1 font-medium"><Clock3 size={14} />{formatTime(lottery.drawAt)}</div></div>
            </div>
            {lottery.status === "completed" && data.myPayout ? (
              <Banner type="success" title="恭喜中奖" description={`${data.myPayout.amount} 积分 · ${data.myPayout.tier === "lucky" ? "幸运奖" : data.myPayout.tier === "normal" ? "普通奖" : "安慰奖"}`} />
            ) : null}
            {data.hasEntered && lottery.status !== "completed" ? <Banner type="info" description="你已报名，请等待开奖。" /> : null}
            {lottery.status === "cancelled" ? <Banner type="warning" description="本次抽奖没有有效参与者，预算未发放。" /> : null}
            <Button block disabled={!canEnter} loading={entering} onClick={() => void enter()} size="large" theme="solid" type="primary">
              {!currentUser ? "登录后参与" : data.hasEntered ? "已报名" : closed ? "报名已结束" : "立即参与抽奖"}
            </Button>
          </div>
        </Card>
      </div>
    </main>
  );
}
