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
import type { TFunction } from "i18next";
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

const statusMeta: Record<Lottery["status"], { color: string; labelKey: string }> = {
  funding: { color: "amber", labelKey: "Lottery status funding" },
  open: { color: "blue", labelKey: "Lottery status open" },
  settling: { color: "orange", labelKey: "Lottery status settling" },
  completed: { color: "green", labelKey: "Lottery status completed" },
  cancelled: { color: "grey", labelKey: "Lottery status cancelled" },
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

function statusTag(status: Lottery["status"], t: TFunction) {
  const meta = statusMeta[status] ?? statusMeta.cancelled;
  return <Tag color={meta.color as never}>{t(meta.labelKey)}</Tag>;
}

export default function AdminLotteries() {
  const { i18n, t } = useTranslation();
  const language = i18n.resolvedLanguage ?? i18n.language;
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
      Toast.error(getIamErrorMessage(t, error, "Lottery campaigns load failed."));
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
        if (!cancelled) Toast.error(getIamErrorMessage(t, error, "Lottery details load failed."));
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
      Toast.warning(t("Please select a future draw time."));
      return;
    }
    if (!Number.isInteger(target) || target <= 0 || !Number.isInteger(minAge) || minAge < 0) {
      Toast.warning(t("Participant target and minimum account age must be valid integers."));
      return;
    }
    if (tierTotal !== 100) {
      Toast.warning(t("Prize tier percentages must total 100%."));
      return;
    }
    const total = Number(form.totalAmount);
    const minPayout = Number(form.minPayout);
    const maxPayout = Number(form.maxPayout);
    if (!Number.isFinite(total) || !Number.isFinite(minPayout) || !Number.isFinite(maxPayout) || total <= 0 || minPayout <= 0 || minPayout >= maxPayout) {
      Toast.warning(t("Total, minimum, and maximum amounts must be valid, and the minimum must be less than the maximum."));
      return;
    }
    if (total <= target * minPayout) {
      Toast.warning(t("Total amount must exceed the minimum payout for all target participants to allow varied rewards."));
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
      Toast.error(getIamErrorMessage(t, error, "Lottery publish failed."));
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
      Toast.success(t("Lottery published and link copied: {{url}}", { url }));
    } catch {
      Toast.success(t("Lottery published: {{url}}", { url }));
      Toast.warning(t("Copy link failed. Please copy it manually."));
    }
  };

  const lotteryColumns = [
    {
      title: t("Activity"),
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
      title: t("Status"),
      key: "status",
      render: (_: unknown, row: Lottery) => statusTag(row.status, t),
    },
    {
      title: t("Prize pool / Participants"),
      key: "pool",
      render: (_: unknown, row: Lottery) => (
        <span className="text-sm text-[var(--ink)]">
          {row.totalAmount} / {row.participantCount}
          {row.participantTarget ? ` / ${row.participantTarget}` : ""}
        </span>
      ),
    },
    {
      title: t("Draw time"),
      key: "drawAt",
      render: (_: unknown, row: Lottery) => (
        <span className="text-sm text-[var(--ink-muted)]">{formatTime(row.drawAt, language)}</span>
      ),
    },
    {
      title: t("Link"),
      key: "link",
      render: (_: unknown, row: Lottery) => {
        const url = new URL(row.publicUrl, window.location.origin).toString();
        return (
          <div className="flex items-center gap-1">
            <Button
              aria-label={t("Copy lottery link")}
              icon={<Copy size={15} />}
              onClick={() => void copyText(url).then(() => Toast.success(t("Lottery link copied."))).catch(() => Toast.error(t("Lottery link copy failed.")))}
              size="small"
              theme="borderless"
            />
            <a aria-label={t("Open lottery link")} href={url} rel="noreferrer" target="_blank">
              <ExternalLink size={15} />
            </a>
          </div>
        );
      },
    },
  ];

  const entryColumns = [
    { title: t("User ID"), dataIndex: "userId", key: "userId" },
    { title: t("Registered At"), key: "registeredAt", render: (_: unknown, row: LotteryEntry) => formatTime(row.registeredAt, language) },
  ];
  const payoutColumns = [
    { title: t("User ID"), dataIndex: "userId", key: "userId" },
    { title: t("Prize tier"), key: "tier", render: (_: unknown, row: LotteryPayout) => t(tierLabelKeys[row.tier] ?? row.tier) },
    { title: t("Amount"), dataIndex: "amount", key: "amount" },
    { title: t("Billing transaction number"), dataIndex: "billingTransactionNo", key: "billingTransactionNo" },
  ];

  return (
    <div className="console-content-width min-h-full py-5">
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Gift className="text-brand" size={22} />
            <h1 className="text-xl font-semibold text-[var(--ink)]">{t("Lottery campaigns")}</h1>
          </div>
          <p className="mt-1 text-sm text-[var(--ink-muted)]">{t("Publish campaigns, copy links, and review draw results.")}</p>
        </div>
        <Button icon={<RefreshCw size={15} />} loading={loading} onClick={() => void load()} type="tertiary">
          {t("Refresh")}
        </Button>
      </div>

      <div className="grid gap-4 xl:grid-cols-[360px_minmax(0,1fr)]">
        <Card title={t("Publish lottery")} className="!rounded-xl">
          <div className="grid gap-3">
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              {t("Activity title")}
              <Input value={form.title} onChange={(value) => setField("title", value)} placeholder={t("For example: Weekend points lottery")} />
            </label>
            <div className="grid gap-2 sm:grid-cols-3">
              {([
                ["totalAmount", "Total amount"],
                ["minPayout", "Minimum amount"],
                ["maxPayout", "Maximum amount"],
              ] as const).map(([key, label]) => (
                <label className="grid gap-1 text-xs text-[var(--ink-muted)]" key={key}>
                  {t(label)}
                  <Input value={form[key]} onChange={(value) => setField(key, value)} suffix={t("Points")} />
                </label>
              ))}
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              {([
                ["consolation", "Consolation prize"],
                ["normal", "Regular prize"],
                ["lucky", "Lucky prize"],
              ] as const).map(([key, label]) => (
                <label className="grid gap-1 text-xs text-[var(--ink-muted)]" key={key}>
                  {t(label)} %
                  <Input value={form[key]} onChange={(value) => setField(key, value)} />
                </label>
              ))}
            </div>
            <div className={`text-xs ${tierTotal === 100 ? "text-emerald-600" : "text-red-500"}`}>
              {t("Percent total: {{total}}%", { total: tierTotal })}
            </div>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              {t("Minimum account age")}
              <Input value={form.minAccountAgeDays} onChange={(value) => setField("minAccountAgeDays", value)} suffix={t("Days unit")} />
            </label>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              {t("Draw time")}
              <input className="semi-input w-full" onChange={(event) => setField("drawAt", event.target.value)} type="datetime-local" value={form.drawAt} />
            </label>
            <label className="grid gap-1 text-sm text-[var(--ink-muted)]">
              {t("Participant target")}
              <Input value={form.participantTarget} onChange={(value) => setField("participantTarget", value)} suffix={t("People unit")} />
            </label>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--ink-muted)]">
              {t("All eligible participants receive a prize. The draw starts when either condition is met first; unused budget is not awarded.")}
            </div>
            <Button block icon={<Gift size={16} />} loading={publishing} onClick={() => void publish()} theme="solid" type="primary">
              {t("Publish lottery and copy link")}
            </Button>
          </div>
        </Card>

        <Card title={t("Campaign list ({{count}})", { count: total })} className="!rounded-xl">
          {items.length === 0 && !loading ? (
            <Empty description={t("No lottery campaigns")} />
          ) : (
            <CardTable columns={lotteryColumns} dataSource={items} loading={loading} rowKey="id" hidePagination />
          )}
        </Card>
      </div>

      {selected ? (
        <Card className="mt-4 !rounded-xl" title={t("Campaign details: {{title}}", { title: selected.title })}>
          <div className="mb-4 grid gap-3 text-sm sm:grid-cols-4">
            <div><span className="text-[var(--ink-muted)]">{t("Status")}</span><div className="mt-1">{statusTag(selected.status, t)}</div></div>
            <div><span className="text-[var(--ink-muted)]">{t("Participant progress")}</span><div className="mt-1 font-medium">{selected.participantCount} / {selected.participantTarget ?? selected.maxParticipants}</div></div>
            <div><span className="text-[var(--ink-muted)]">{t("Draw time")}</span><div className="mt-1">{formatTime(selected.drawAt, language)}</div></div>
            <div><span className="text-[var(--ink-muted)]">{t("Unused budget")}</span><div className="mt-1">{t("{{amount}} points", { amount: selected.unusedAmount })}</div></div>
          </div>
          {detailLoading ? <div className="flex justify-center py-8"><Spin /></div> : (
            <div className="grid gap-5 xl:grid-cols-2">
              <div>
                <h2 className="mb-2 text-sm font-semibold text-[var(--ink)]">{t("Participants ({{count}})", { count: entryTotal })}</h2>
                <CardTable columns={entryColumns} dataSource={entries} rowKey="id" hidePagination />
              </div>
              <div>
                <h2 className="mb-2 text-sm font-semibold text-[var(--ink)]">{t("Draw results ({{count}})", { count: payoutTotal })}</h2>
                <CardTable columns={payoutColumns} dataSource={payouts} rowKey="id" hidePagination />
              </div>
            </div>
          )}
        </Card>
      ) : null}
    </div>
  );
}
