import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Card,
  Empty,
  Input,
  Spin,
  Tag,
  Toast,
} from "@douyinfe/semi-ui";
import { Copy, ExternalLink, Gift, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { CardTable } from "@/components/semi/card-table";
import { copyText } from "@/lib/clipboard";
import { generateIdempotencyKey } from "@/lib/idempotency";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createAdminLottery,
  listAdminLotteryEntries,
  listAdminLotteryPayouts,
  listAdminLotteries,
  type CreateLotteryInput,
  type Lottery,
  type LotteryEntry,
  type LotteryPayout,
} from "@/lib/lottery-api";

type FormState = {
  title: string;
  totalAmount: string;
  minPayout: string;
  maxPayout: string;
  consolation: string;
  normal: string;
  lucky: string;
  minAccountAgeDays: string;
  drawAt: string;
  participantTarget: string;
};

const initialForm: FormState = {
  title: "",
  totalAmount: "300.00",
  minPayout: "1.00",
  maxPayout: "20.00",
  consolation: "80",
  normal: "15",
  lucky: "5",
  minAccountAgeDays: "0",
  drawAt: "",
  participantTarget: "100",
};

const statusMeta: Record<Lottery["status"], { color: string; label: string }> = {
  funding: { color: "amber", label: "旧活动待处理" },
  open: { color: "blue", label: "进行中" },
  settling: { color: "orange", label: "开奖中" },
  completed: { color: "green", label: "已完成" },
  cancelled: { color: "grey", label: "已取消" },
};

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function statusTag(status: Lottery["status"]) {
  const meta = statusMeta[status] ?? statusMeta.cancelled;
  return <Tag color={meta.color as never}>{meta.label}</Tag>;
}

export default function AdminLotteries() {
  const { t } = useTranslation();
  const [form, setForm] = useState<FormState>(initialForm);
  const [items, setItems] = useState<Lottery[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [publishing, setPublishing] = useState(false);
  const [selected, setSelected] = useState<Lottery | null>(null);
  const [entries, setEntries] = useState<LotteryEntry[]>([]);
  const [payouts, setPayouts] = useState<LotteryPayout[]>([]);
  const [entryTotal, setEntryTotal] = useState(0);
  const [payoutTotal, setPayoutTotal] = useState(0);
  const [detailLoading, setDetailLoading] = useState(false);
  const publishKeyRef = useRef<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const response = await listAdminLotteries(undefined, 0, 50);
      setItems(response.items);
      setTotal(response.total);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "抽奖活动加载失败"));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!selected) {
      setEntries([]);
      setPayouts([]);
      setEntryTotal(0);
      setPayoutTotal(0);
      return;
    }
    let cancelled = false;
    setDetailLoading(true);
    void Promise.all([
      listAdminLotteryEntries(selected.id, 0, 100),
      listAdminLotteryPayouts(selected.id, 0, 100),
    ])
      .then(([entryPage, payoutPage]) => {
        if (cancelled) return;
        setEntries(entryPage.items);
        setPayouts(payoutPage.items);
        setEntryTotal(entryPage.total);
        setPayoutTotal(payoutPage.total);
      })
      .catch((error) => {
        if (!cancelled) Toast.error(getIamErrorMessage(t, error, "抽奖明细加载失败"));
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selected, t]);

  const setField = (key: keyof FormState, value: string) => {
    publishKeyRef.current = null;
    setForm((current) => ({ ...current, [key]: value }));
  };

  const tierTotal = useMemo(
    () =>
      Number(form.consolation || 0) +
      Number(form.normal || 0) +
      Number(form.lucky || 0),
    [form.consolation, form.lucky, form.normal],
  );

  const publish = async () => {
    const target = Number(form.participantTarget);
    const minAge = Number(form.minAccountAgeDays);
    const drawAt = new Date(form.drawAt);
    if (!form.drawAt || !Number.isFinite(drawAt.getTime()) || drawAt.getTime() <= Date.now()) {
      Toast.warning("请选择未来的开奖时间");
      return;
    }
    if (!Number.isInteger(target) || target <= 0 || !Number.isInteger(minAge) || minAge < 0) {
      Toast.warning("参与人数和注册天数必须是有效整数");
      return;
    }
    if (tierTotal !== 100) {
      Toast.warning("奖励比例合计必须为 100%");
      return;
    }
    const total = Number(form.totalAmount);
    const minPayout = Number(form.minPayout);
    const maxPayout = Number(form.maxPayout);
    if (!Number.isFinite(total) || !Number.isFinite(minPayout) || !Number.isFinite(maxPayout) || total <= 0 || minPayout <= 0 || minPayout >= maxPayout) {
      Toast.warning("总金额、最小金额和最大金额必须有效，且最小金额要小于最大金额");
      return;
    }
    if (total <= target * minPayout) {
      Toast.warning("总金额必须高于目标人数的最低奖励总额，才能形成差异化奖励");
      return;
    }
    const token = await requireTurnstile("lottery_publish");
    if (!token) return;
    const body: CreateLotteryInput = {
      title: form.title.trim(),
      totalAmount: form.totalAmount.trim(),
      minPayout: form.minPayout.trim(),
      maxPayout: form.maxPayout.trim(),
      tierWeights: {
        consolation: Number(form.consolation),
        normal: Number(form.normal),
        lucky: Number(form.lucky),
      },
      minAccountAgeDays: minAge,
      drawAt: drawAt.toISOString(),
      participantTarget: target,
    };
    setPublishing(true);
    const idempotencyKey = publishKeyRef.current ?? generateIdempotencyKey();
    publishKeyRef.current = idempotencyKey;
    let lottery: Lottery;
    try {
      lottery = await createAdminLottery(body, token, idempotencyKey);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "抽奖发布失败"));
      setPublishing(false);
      return;
    } finally {
      setPublishing(false);
    }
    publishKeyRef.current = null;
    const url = new URL(lottery.publicUrl, window.location.origin).toString();
    setForm(initialForm);
    setSelected(lottery);
    await load();
    try {
      await copyText(url);
      Toast.success(`抽奖已发布，链接已复制：${url}`);
    } catch {
      Toast.success(`抽奖已发布：${url}`);
      Toast.warning("链接复制失败，请手动复制");
    }
  };

  const lotteryColumns = [
    {
      title: "活动",
      key: "title",
      render: (_: unknown, row: Lottery) => (
        <button
          className="flex min-w-0 cursor-pointer flex-col text-left"
          onClick={() => setSelected(row)}
          type="button"
        >
          <span className="truncate font-medium text-[var(--ink)]">{row.title}</span>
          <span className="text-xs text-[var(--ink-muted)]">#{row.id}</span>
        </button>
      ),
    },
    {
      title: "状态",
      key: "status",
      render: (_: unknown, row: Lottery) => statusTag(row.status),
    },
    {
      title: "奖池 / 参与",
      key: "pool",
      render: (_: unknown, row: Lottery) => (
        <span className="text-sm text-[var(--ink)]">
          {row.totalAmount} / {row.participantCount}
          {row.participantTarget ? ` / ${row.participantTarget}` : ""}
        </span>
      ),
    },
    {
      title: "开奖时间",
      key: "drawAt",
      render: (_: unknown, row: Lottery) => (
        <span className="text-sm text-[var(--ink-muted)]">{formatTime(row.drawAt)}</span>
      ),
    },
    {
      title: "链接",
      key: "link",
      render: (_: unknown, row: Lottery) => {
        const url = new URL(row.publicUrl, window.location.origin).toString();
        return (
          <div className="flex items-center gap-1">
            <Button
              aria-label="复制抽奖链接"
              icon={<Copy size={15} />}
              onClick={() => void copyText(url).then(() => Toast.success("链接已复制")).catch(() => Toast.error("链接复制失败"))}
              size="small"
              theme="borderless"
            />
            <a aria-label="打开抽奖链接" href={url} rel="noreferrer" target="_blank">
              <ExternalLink size={15} />
            </a>
          </div>
        );
      },
    },
  ];

  const entryColumns = [
    { title: "用户 ID", dataIndex: "userId", key: "userId" },
    { title: "注册时间", key: "registeredAt", render: (_: unknown, row: LotteryEntry) => formatTime(row.registeredAt) },
  ];
  const payoutColumns = [
    { title: "用户 ID", dataIndex: "userId", key: "userId" },
    { title: "档位", dataIndex: "tier", key: "tier" },
    { title: "金额", dataIndex: "amount", key: "amount" },
    { title: "Billing 交易号", dataIndex: "billingTransactionNo", key: "billingTransactionNo" },
  ];

  return (
    <div className="console-content-width min-h-full py-5">
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Gift className="text-brand" size={22} />
            <h1 className="text-xl font-semibold text-[var(--ink)]">抽奖活动</h1>
          </div>
          <p className="mt-1 text-sm text-[var(--ink-muted)]">发布活动、复制链接并查看开奖结果</p>
        </div>
        <Button icon={<RefreshCw size={15} />} loading={loading} onClick={() => void load()} type="tertiary">
          刷新
        </Button>
      </div>

      <div className="grid gap-4 xl:grid-cols-[360px_minmax(0,1fr)]">
        <Card title="发布抽奖" className="!rounded-xl">
          <div className="grid gap-3">
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              活动标题
              <Input value={form.title} onChange={(value) => setField("title", value)} placeholder="例如：周末积分抽奖" />
            </label>
            <div className="grid gap-2 sm:grid-cols-3">
              {([
                ["totalAmount", "总金额"],
                ["minPayout", "最小金额"],
                ["maxPayout", "最大金额"],
              ] as const).map(([key, label]) => (
                <label className="grid gap-1 text-xs text-[var(--ink-muted)]" key={key}>
                  {label}
                  <Input value={form[key]} onChange={(value) => setField(key, value)} suffix="积分" />
                </label>
              ))}
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              {([
                ["consolation", "安慰奖"],
                ["normal", "普通奖"],
                ["lucky", "幸运奖"],
              ] as const).map(([key, label]) => (
                <label className="grid gap-1 text-xs text-[var(--ink-muted)]" key={key}>
                  {label} %
                  <Input value={form[key]} onChange={(value) => setField(key, value)} />
                </label>
              ))}
            </div>
            <div className={`text-xs ${tierTotal === 100 ? "text-emerald-600" : "text-red-500"}`}>
              比例合计：{tierTotal}%
            </div>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              最低账号注册天数
              <Input value={form.minAccountAgeDays} onChange={(value) => setField("minAccountAgeDays", value)} suffix="天" />
            </label>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              开奖时间
              <input className="semi-input w-full" onChange={(event) => setField("drawAt", event.target.value)} type="datetime-local" value={form.drawAt} />
            </label>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              满足参与人数
              <Input value={form.participantTarget} onChange={(value) => setField("participantTarget", value)} suffix="人" />
            </label>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--ink-muted)]">
              所有有效参与者都会中奖，两个开奖条件先满足的立即开奖；未使用的预算不发放。
            </div>
            <Button block icon={<Gift size={16} />} loading={publishing} onClick={() => void publish()} theme="solid" type="primary">
              发布抽奖并复制链接
            </Button>
          </div>
        </Card>

        <Card title={`活动列表 (${total})`} className="!rounded-xl">
          {items.length === 0 && !loading ? (
            <Empty description="暂无抽奖活动" />
          ) : (
            <CardTable columns={lotteryColumns} dataSource={items} loading={loading} rowKey="id" hidePagination />
          )}
        </Card>
      </div>

      {selected ? (
        <Card className="mt-4 !rounded-xl" title={`活动详情：${selected.title}`}>
          <div className="mb-4 grid gap-3 text-sm sm:grid-cols-4">
            <div><span className="text-[var(--ink-muted)]">状态</span><div className="mt-1">{statusTag(selected.status)}</div></div>
            <div><span className="text-[var(--ink-muted)]">参与进度</span><div className="mt-1 font-medium">{selected.participantCount} / {selected.participantTarget ?? selected.maxParticipants}</div></div>
            <div><span className="text-[var(--ink-muted)]">开奖时间</span><div className="mt-1">{formatTime(selected.drawAt)}</div></div>
            <div><span className="text-[var(--ink-muted)]">未发放预算</span><div className="mt-1">{selected.unusedAmount} 积分</div></div>
          </div>
          {detailLoading ? <div className="flex justify-center py-8"><Spin /></div> : (
            <div className="grid gap-5 xl:grid-cols-2">
              <div>
                <h2 className="mb-2 text-sm font-semibold text-[var(--ink)]">参与者 ({entryTotal})</h2>
                <CardTable columns={entryColumns} dataSource={entries} rowKey="id" hidePagination />
              </div>
              <div>
                <h2 className="mb-2 text-sm font-semibold text-[var(--ink)]">中奖结果 ({payoutTotal})</h2>
                <CardTable columns={payoutColumns} dataSource={payouts} rowKey="id" hidePagination />
              </div>
            </div>
          )}
        </Card>
      ) : null}
    </div>
  );
}
