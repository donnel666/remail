import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Empty,
  Input,
  Modal,
  SideSheet,
  Space,
  Tabs,
  Tag,
  Toast,
} from "@douyinfe/semi-ui";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { ExternalLink, Gift, RefreshCw } from "lucide-react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { copyText } from "@/lib/clipboard";
import { listAdminUsers as lookupAdminUsers, type UserResponse } from "@/lib/iam-api";
import { generateIdempotencyKey } from "@/lib/idempotency";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createAdminLottery,
  getAdminLottery,
  listAllAdminLotteryEntries,
  listAllAdminLotteryPayouts,
  listAdminLotteries,
  type CreateLotteryInput,
  type Lottery,
  type AdminLotteryEntry,
  type LotteryPayout,
} from "@/lib/lottery-api";
import { formatPoints, normalizePointValue } from "@/lib/points";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { BalanceAccountCell } from "./admin-finance/balance-meta";

type LotteryStatusFilter = "all" | Lottery["status"];
const DETAIL_TABLE_SCROLL_Y = "max(220px, calc(100vh - 300px))";

type FormState = {
  title: string;
  totalAmount: string;
  minPayout: string;
  maxPayout: string;
  normal: string;
  lucky: string;
  minAccountAgeDays: string;
  drawAt: Date | null;
  participantTarget: string;
};

const initialForm: FormState = {
  title: "",
  totalAmount: "300.00",
  minPayout: "1.00",
  maxPayout: "20.00",
  normal: "10",
  lucky: "5",
  minAccountAgeDays: "0",
  drawAt: null,
  participantTarget: "100",
};

const statusMeta: Record<
  Lottery["status"],
  { color: "amber" | "blue" | "orange" | "green" | "grey"; labelKey: string }
> = {
  funding: { color: "amber", labelKey: "Lottery status funding" },
  open: { color: "blue", labelKey: "Lottery status open" },
  settling: { color: "orange", labelKey: "Lottery status settling" },
  completed: { color: "green", labelKey: "Lottery status completed" },
  cancelled: { color: "grey", labelKey: "Lottery status cancelled" },
};

const LOTTERY_USER_LOOKUP_CHUNK_SIZE = 100;

const lotteryStatusTabs: Lottery["status"][] = [
  "open",
  "settling",
  "completed",
  "cancelled",
];

function comparePointAmounts(left: string, right: string) {
  const normalize = (value: string) => {
    const normalized = normalizePointValue(value);
    if (!normalized) return 0n;
    const negative = normalized.startsWith("-");
    const unsigned = negative ? normalized.slice(1) : normalized;
    const [integer, fraction = ""] = unsigned.split(".");
    const units = BigInt(`${integer}${fraction.padEnd(6, "0")}`);
    return negative ? -units : units;
  };
  const leftUnits = normalize(left);
  const rightUnits = normalize(right);
  return leftUnits === rightUnits ? 0 : leftUnits > rightUnits ? 1 : -1;
}

async function lookupLotteryUsers(userIDs: number[]) {
  const uniqueIDs = Array.from(
    new Set(userIDs.filter((id) => Number.isSafeInteger(id) && id > 0)),
  );
  if (uniqueIDs.length === 0) return {} as Record<number, UserResponse>;

  const responses = await Promise.allSettled(
    Array.from(
      { length: Math.ceil(uniqueIDs.length / LOTTERY_USER_LOOKUP_CHUNK_SIZE) },
      (_, index) => {
        const ids = uniqueIDs.slice(
          index * LOTTERY_USER_LOOKUP_CHUNK_SIZE,
          (index + 1) * LOTTERY_USER_LOOKUP_CHUNK_SIZE,
        );
        return lookupAdminUsers({ ids, limit: ids.length, offset: 0 });
      },
    ),
  );
  const directory: Record<number, UserResponse> = {};
  for (const response of responses) {
    if (response.status !== "fulfilled") continue;
    for (const user of response.value.users) directory[user.id] = user;
  }
  return directory;
}

function lotteryAccountCell(
  userID: number,
  directory: Record<number, UserResponse>,
  t: TFunction,
) {
  const user = directory[userID];
  if (!user) {
    return <CopyableTableText copiedText={t("Copied")} text={`#${userID}`} />;
  }
  return (
    <BalanceAccountCell
      email={user.email}
      groupName={user.userGroup?.name}
      nickname={user.nickname}
      role={user.role}
      t={t}
      userId={user.id}
    />
  );
}

function formatTime(value: string | null | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString(language);
}

function lotteryTierCountText(lottery: Lottery, t: TFunction) {
  const participantCount = Math.max(0, lottery.participantCount);
  let lucky = Math.max(0, lottery.tierWeights.lucky);
  let normal = Math.max(0, lottery.tierWeights.normal);
  let consolation = Math.max(0, participantCount - lucky - normal);
  if (lottery.algorithmVersion !== "fixed-tier-v3") {
    // v1/v2 stored percentages. Mirror the allocator's largest-remainder
    // tie-break so admin counts remain meaningful for historical campaigns.
    const weights = lottery.tierWeights;
    const percentages = [
      Math.max(0, weights.consolation),
      Math.max(0, weights.normal),
      Math.max(0, weights.lucky),
    ];
    const counts = percentages.map((weight) =>
      Math.floor((participantCount * weight) / 100),
    );
    let remaining =
      participantCount - counts.reduce((sum, value) => sum + value, 0);
    while (remaining > 0) {
      let index = 0;
      if (
        percentages[2] >= percentages[1] &&
        percentages[2] >= percentages[0] &&
        percentages[2] > 0
      ) {
        index = 2;
      } else if (percentages[1] >= percentages[0] && percentages[1] > 0) {
        index = 1;
      }
      if (percentages[index] <= 0) break;
      counts[index] += 1;
      remaining -= 1;
    }
    if (counts[0] === 0 && participantCount > 0) {
      const donor = counts[1] > 0 ? 1 : counts[2] > 0 ? 2 : -1;
      if (donor >= 0) {
        counts[0] = 1;
        counts[donor] -= 1;
      }
    }
    [consolation, normal, lucky] = counts;
  }
  return `${t("Consolation prize")} ${consolation} · ${t("Regular prize")} ${normal} · ${t("Lucky prize")} ${lucky}`;
}

function statusTag(status: Lottery["status"], t: TFunction) {
  const meta = statusMeta[status] ?? statusMeta.cancelled;
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.labelKey)}
    </Tag>
  );
}

function fieldLabel(label: string, required = false) {
  return (
    <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
      {label}
      {required ? " *" : ""}
    </span>
  );
}

function LotteryCreateModal({
  onCancel,
  onCreated,
  visible,
}: {
  onCancel: () => void;
  onCreated: (lottery: Lottery) => void | Promise<void>;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState<FormState>(initialForm);
  const [submitting, setSubmitting] = useState(false);
  const publishKeyRef = useRef<string | null>(null);

  useEffect(() => {
    if (!visible) return;
    setForm(initialForm);
    publishKeyRef.current = null;
  }, [visible]);

  const setField = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    publishKeyRef.current = null;
    setForm((current) => ({ ...current, [key]: value }));
  };

  const submit = async () => {
    const hasTarget = form.participantTarget.trim() !== "";
    const target = hasTarget ? Number(form.participantTarget) : undefined;
    const hasDrawAt = form.drawAt !== null;
    const minAge = Number(form.minAccountAgeDays);
    const total = Number(form.totalAmount);
    const minPayout = Number(form.minPayout);
    const maxPayout = Number(form.maxPayout);
    const tierValues = [Number(form.normal), Number(form.lucky)];

    if (!form.title.trim()) {
      Toast.warning(t("Please enter an activity title."));
      return;
    }
    if (!hasDrawAt && !hasTarget) {
      Toast.warning(
        t("Please set at least one draw condition: a future draw time or a participant target.")
      );
      return;
    }
    if (hasDrawAt && form.drawAt!.getTime() <= Date.now()) {
      Toast.warning(t("Please select a future draw time."));
      return;
    }
    if (
      (hasTarget && (!Number.isInteger(target) || target! <= 0)) ||
      !Number.isInteger(minAge) ||
      minAge < 0
    ) {
      Toast.warning(
        t("Participant target and minimum account age must be valid integers.")
      );
      return;
    }
    if (
      !Number.isFinite(total) ||
      !Number.isFinite(minPayout) ||
      !Number.isFinite(maxPayout) ||
      !Number.isInteger(total) ||
      !Number.isInteger(minPayout) ||
      !Number.isInteger(maxPayout) ||
      total <= 0 ||
      minPayout <= 0 ||
      maxPayout <= 0 ||
      minPayout >= maxPayout
    ) {
      Toast.warning(
        t(
          "Total, minimum, and maximum amounts must be positive whole points, and the minimum must be less than the maximum."
        )
      );
      return;
    }
    if (
      !tierValues.every((value) => Number.isInteger(value) && value >= 0)
    ) {
      Toast.warning(t("Prize tier counts must be non-negative integers."));
      return;
    }
    if (hasTarget && total < target! * minPayout) {
      Toast.warning(
        t(
          "Total amount must cover the minimum payout for all target participants."
        )
      );
      return;
    }
    if (hasTarget && total > target! * maxPayout) {
      Toast.warning(
        t(
          "Total amount must fit within the maximum payout for all target participants."
        )
      );
      return;
    }
    if (hasTarget && tierValues[0] + tierValues[1] > target!) {
      Toast.warning(t("Prize counts cannot exceed the participant target."));
      return;
    }
    if (hasTarget) {
      const variableCapacity = maxPayout - minPayout;
      const fixedMinimum =
        target! * minPayout + tierValues[1] * variableCapacity;
      const fixedMaximum = fixedMinimum + tierValues[0] * variableCapacity;
      if (total < fixedMinimum || total > fixedMaximum) {
        Toast.warning(t("Total amount does not fit the configured prize counts."));
        return;
      }
    }

    const body: CreateLotteryInput = {
      title: form.title.trim(),
      totalAmount: form.totalAmount.trim(),
      minPayout: form.minPayout.trim(),
      maxPayout: form.maxPayout.trim(),
      tierWeights: {
        normal: Number(form.normal),
        lucky: Number(form.lucky),
        consolation: 0,
      },
      minAccountAgeDays: minAge,
      drawAt: form.drawAt?.toISOString(),
      participantTarget: hasTarget ? target : undefined,
    };

    setSubmitting(true);
    const idempotencyKey = publishKeyRef.current ?? generateIdempotencyKey();
    publishKeyRef.current = idempotencyKey;
    try {
      const lottery = await createAdminLottery(body, idempotencyKey);
      await onCreated(lottery);
      const url = new URL(lottery.publicUrl, window.location.origin).toString();
      onCancel();
      try {
        await copyText(url);
        Toast.success(t("Lottery published and link copied: {{url}}", { url }));
      } catch {
        Toast.success(t("Lottery published: {{url}}", { url }));
        Toast.warning(t("Copy link failed. Please copy it manually."));
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Lottery publish failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Publish lottery")}
      title={t("Create lottery campaign")}
      visible={visible}
      width="min(720px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <label className="block">
          {fieldLabel(t("Activity title"), true)}
          <Input
            autoFocus
            onChange={(value) => setField("title", String(value))}
            placeholder={t("For example: Weekend points lottery")}
            value={form.title}
          />
        </label>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          {(
            [
              ["totalAmount", "Total amount"],
              ["minPayout", "Minimum amount"],
              ["maxPayout", "Maximum amount"],
            ] as const
          ).map(([key, label]) => (
            <label className="block" key={key}>
              {fieldLabel(t(label), true)}
              <Input
                onChange={(value) => setField(key, String(value))}
                suffix={t("Points")}
                value={form[key]}
              />
            </label>
          ))}
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
          <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
            {t("Prize counts")}
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {(
              [
                ["normal", "Regular prize count"],
                ["lucky", "Lucky prize count"],
              ] as const
            ).map(([key, label]) => (
              <label className="block" key={key}>
                {fieldLabel(t(label), true)}
                <Input
                  onChange={(value) => setField(key, String(value))}
                  suffix={t("People unit")}
                  value={form[key]}
                />
              </label>
            ))}
          </div>
          <div className="mt-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
            {t("All other participants receive the minimum payout.")}
          </div>
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <label className="block">
            {fieldLabel(t("Minimum account age"), true)}
            <Input
              onChange={(value) => setField("minAccountAgeDays", String(value))}
              suffix={t("Days unit")}
              value={form.minAccountAgeDays}
            />
          </label>
          <label className="block">
            {fieldLabel(t("Participant target"))}
            <Input
              onChange={(value) => setField("participantTarget", String(value))}
              placeholder={t("Optional")}
              suffix={t("People unit")}
              value={form.participantTarget}
            />
          </label>
          <label className="block">
            {fieldLabel(t("Draw time"))}
            <DatePicker
              format="yyyy-MM-dd HH:mm:ss"
              onChange={(value) =>
                setField("drawAt", value instanceof Date ? value : null)
              }
              showClear
              style={{ width: "100%" }}
              type="dateTime"
              value={form.drawAt ?? undefined}
            />
          </label>
        </div>

        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "Set at least one draw condition: a future draw time or a participant target. If both are set, the first one reached starts the draw. All eligible participants receive a prize."
          )}
        </div>
      </div>
    </Modal>
  );
}

function LotteryDetailSheet({
  detail,
  entries,
  entryTotal,
  loading,
  onCancel,
  payouts,
  payoutTotal,
  userDirectory,
  t,
  language,
}: {
  detail: Lottery | null;
  entries: AdminLotteryEntry[];
  entryTotal: number;
  loading: boolean;
  onCancel: () => void;
  payouts: LotteryPayout[];
  payoutTotal: number;
  userDirectory: Record<number, UserResponse>;
  t: TFunction;
  language: string;
}) {
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("overview");

  useEffect(() => {
    setActiveTab("overview");
  }, [detail?.id]);

  const entryColumns = useMemo(
    () =>
      [
        { dataIndex: "id", key: "id", title: "ID", width: 80 },
        {
          dataIndex: "userId",
          key: "userId",
          title: t("Account"),
          width: 280,
          render: (value: unknown) =>
            lotteryAccountCell(Number(value), userDirectory, t),
        },
        {
          dataIndex: "registeredAt",
          key: "registeredAt",
          title: t("Registered At"),
          width: 220,
          render: (value: unknown) => formatTime(String(value), language),
        },
      ] as any[],
    [language, t, userDirectory]
  );

  const payoutColumns = useMemo(
    () =>
      [
        { dataIndex: "id", key: "id", title: "ID", width: 80 },
        {
          dataIndex: "userId",
          key: "userId",
          title: t("Account"),
          width: 280,
          render: (value: unknown) =>
            lotteryAccountCell(Number(value), userDirectory, t),
        },
        {
          dataIndex: "amount",
          key: "amount",
          title: t("Amount"),
          width: 140,
          render: (value: unknown) => formatPoints(String(value)),
        },
        {
          dataIndex: "billingTransactionNo",
          key: "billingTransactionNo",
          title: t("Billing transaction number"),
          render: (value: unknown) =>
            value ? (
              <CopyableTableText copiedText={t("Copied")} text={String(value)} />
            ) : (
              "-"
            ),
        },
      ] as any[],
    [t, userDirectory]
  );
  const sortedPayouts = useMemo(
    () =>
      [...payouts].sort(
        (left, right) =>
          comparePointAmounts(right.amount, left.amount) || left.id - right.id,
      ),
    [payouts],
  );
  const publicUrl = detail
    ? new URL(detail.publicUrl, window.location.origin).toString()
    : "";

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={detail ? `${t("Lottery details")} #${detail.id}` : t("Lottery details")}
      visible={Boolean(detail)}
      width={isMobile ? "100%" : 820}
    >
      {detail ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs activeKey={activeTab} onChange={setActiveTab} type="line">
              <Tabs.TabPane itemKey="overview" tab={t("Overview")} />
              <Tabs.TabPane itemKey="entries" tab={t("Entries ({{count}})", { count: entryTotal })} />
              <Tabs.TabPane itemKey="payouts" tab={t("Payouts ({{count}})", { count: payoutTotal })} />
            </Tabs>
          </div>

          <div className="flex-1 space-y-5 p-5">
            {activeTab === "overview" ? (
              <div className="space-y-5">
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Activity")}</div>
                    <div className="mt-1 font-medium text-[var(--semi-color-text-0)]">{detail.title}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Status")}</div>
                    <div className="mt-1">{statusTag(detail.status, t)}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Created at")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{formatTime(detail.createdAt, language)}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Total amount")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{formatPoints(detail.totalAmount)} {t("Points")}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Reward range")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{formatPoints(detail.minPayout)} - {formatPoints(detail.maxPayout)} {t("Points")}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Participant progress")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">
                      {detail.participantTarget == null
                        ? detail.participantCount
                        : `${detail.participantCount} / ${detail.participantTarget}`}
                    </div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Draw time")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{formatTime(detail.drawAt, language)}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Minimum account age")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{t("At least {{days}} days", { days: detail.minAccountAgeDays })}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">{t("Unused budget")}</div>
                    <div className="mt-1 text-sm text-[var(--semi-color-text-0)]">{formatPoints(detail.unusedAmount)} {t("Points")}</div>
                  </div>
                </div>

                <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
                  <div className="mb-2 text-xs text-[var(--semi-color-text-2)]">{t("Lottery public link")}</div>
                  <div className="flex items-center gap-2">
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={publicUrl}
                    />
                    <a
                      aria-label={t("Open lottery link")}
                      href={publicUrl}
                      rel="noreferrer"
                      target="_blank"
                    >
                      <ExternalLink size={15} />
                    </a>
                  </div>
                </div>

                <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5 text-sm text-[var(--semi-color-text-1)]">
                  {t("Prize counts")}: {lotteryTierCountText(detail, t)}
                </div>
              </div>
            ) : null}

            {activeTab === "entries" ? (
              <CardTable
                columns={entryColumns}
                dataSource={entries}
                empty={<Empty description={t("No entries yet")} style={{ padding: 24 }} />}
                hidePagination
                loading={loading}
                rowKey="id"
                scroll={{ x: 680, y: DETAIL_TABLE_SCROLL_Y }}
                size="small"
              />
            ) : null}

            {activeTab === "payouts" ? (
              <CardTable
                columns={payoutColumns}
                dataSource={sortedPayouts}
                empty={<Empty description={t("No payouts yet")} style={{ padding: 24 }} />}
                hidePagination
                loading={loading}
                rowKey="id"
                scroll={{ x: 700, y: DETAIL_TABLE_SCROLL_Y }}
                size="small"
              />
            ) : null}
          </div>
        </div>
      ) : null}
    </SideSheet>
  );
}

export default function AdminLotteries() {
  const { i18n, t } = useTranslation();
  const language = i18n.resolvedLanguage ?? i18n.language;
  const isMobile = useIsMobile();
  const [statusFilter, setStatusFilter] = useState<LotteryStatusFilter>("all");
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [reloadTick, setReloadTick] = useState(0);
  const [items, setItems] = useState<Lottery[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [detail, setDetail] = useState<Lottery | null>(null);
  const [entries, setEntries] = useState<AdminLotteryEntry[]>([]);
  const [payouts, setPayouts] = useState<LotteryPayout[]>([]);
  const [userDirectory, setUserDirectory] = useState<Record<number, UserResponse>>({});
  const [entryTotal, setEntryTotal] = useState(0);
  const [payoutTotal, setPayoutTotal] = useState(0);
  const [detailLoading, setDetailLoading] = useState(false);
  const detailRequestRef = useRef(0);

  useEffect(() => {
    setActivePage(1);
  }, [pageSize, statusFilter]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const offset = (activePage - 1) * pageSize;
    void listAdminLotteries(
      statusFilter === "all" ? undefined : statusFilter,
      offset,
      pageSize
    )
      .then((response) => {
        if (cancelled) return;
        setItems(response.items);
        setTotal(response.total);
      })
      .catch((error) => {
        if (!cancelled) {
          setItems([]);
          setTotal(0);
          Toast.error(getIamErrorMessage(t, error, "Lottery campaigns load failed."));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activePage, pageSize, reloadTick, statusFilter, t]);

  const refresh = useCallback(() => {
    setActivePage(1);
    setReloadTick((value) => value + 1);
  }, []);

  const openDetail = useCallback(async (lottery: Lottery) => {
    const requestId = ++detailRequestRef.current;
    setDetail(lottery);
    setEntries([]);
    setPayouts([]);
    setUserDirectory({});
    setEntryTotal(0);
    setPayoutTotal(0);
    setDetailLoading(true);
    try {
      const [nextDetail, entryPage, payoutPage] = await Promise.all([
        getAdminLottery(lottery.id),
        listAllAdminLotteryEntries(lottery.id),
        listAllAdminLotteryPayouts(lottery.id),
      ]);
      if (requestId !== detailRequestRef.current) return;
      const directory = await lookupLotteryUsers([
        ...entryPage.items.map((entry) => entry.userId),
        ...payoutPage.items.map((payout) => payout.userId),
      ]);
      if (requestId !== detailRequestRef.current) return;
      setDetail(nextDetail);
      setEntries(entryPage.items);
      setPayouts(payoutPage.items);
      setUserDirectory(directory);
      setEntryTotal(entryPage.total);
      setPayoutTotal(payoutPage.total);
    } catch (error) {
      if (requestId === detailRequestRef.current) {
        Toast.error(getIamErrorMessage(t, error, "Lottery details load failed."));
      }
    } finally {
      if (requestId === detailRequestRef.current) setDetailLoading(false);
    }
  }, [t]);

  const onCreated = useCallback(
    async (lottery: Lottery) => {
      setStatusFilter("all");
      setActivePage(1);
      setReloadTick((value) => value + 1);
      void openDetail(lottery);
    },
    [openDetail]
  );

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "title",
          key: "title",
          title: t("Activity"),
          width: 250,
          render: (value: unknown, row: Lottery) => (
            <button
              className="flex min-w-0 cursor-pointer flex-col text-left"
              onClick={() => void openDetail(row)}
              type="button"
            >
              <span className="truncate font-medium text-[var(--semi-color-text-0)]">
                {String(value || t("Untitled lottery"))}
              </span>
              <span className="text-xs text-[var(--semi-color-text-2)]">#{row.id}</span>
            </button>
          ),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 130,
          render: (value: unknown) => statusTag(String(value) as Lottery["status"], t),
        },
        {
          key: "rules",
          title: t("Reward rules"),
          width: 285,
          render: (_: unknown, row: Lottery) => (
            <div className="min-w-0">
              <div className="whitespace-nowrap text-sm tabular-nums text-[var(--semi-color-text-0)]">
                {formatPoints(row.minPayout)} - {formatPoints(row.maxPayout)} {t("Points")}
              </div>
              <div className="text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t("Prize counts")}: {lotteryTierCountText(row, t)}
              </div>
              <div className="text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t("Minimum account age")}: {t("At least {{days}} days", { days: row.minAccountAgeDays })}
              </div>
            </div>
          ),
        },
        {
          dataIndex: "totalAmount",
          key: "totalAmount",
          title: t("Total amount"),
          width: 145,
          render: (value: unknown) => (
            <span className="whitespace-nowrap tabular-nums text-[var(--semi-color-text-1)]">
              {formatPoints(String(value))} {t("Points")}
            </span>
          ),
        },
        {
          key: "participants",
          title: t("Participants"),
          width: 150,
          render: (_: unknown, row: Lottery) => (
            <span className="whitespace-nowrap tabular-nums text-[var(--semi-color-text-1)]">
              {row.participantTarget == null
                ? row.participantCount
                : `${row.participantCount} / ${row.participantTarget}`}
            </span>
          ),
        },
        {
          dataIndex: "drawAt",
          key: "drawAt",
          title: t("Draw time"),
          width: 190,
          render: (value: unknown) => (
            <span className="whitespace-nowrap tabular-nums text-[var(--semi-color-text-1)]">
              {formatTime(String(value || ""), language)}
            </span>
          ),
        },
        {
          dataIndex: "publicUrl",
          key: "publicUrl",
          title: t("Link"),
          width: 250,
          render: (value: unknown) => {
            const url = new URL(String(value), window.location.origin).toString();
            return (
              <Space spacing={6} wrap={false}>
                <CopyableTableText copiedText={t("Copied")} text={url} />
                <a
                  aria-label={t("Open lottery link")}
                  href={url}
                  rel="noreferrer"
                  target="_blank"
                >
                  <ExternalLink size={14} />
                </a>
              </Space>
            );
          },
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Action"),
          width: 120,
          render: (_: unknown, row: Lottery) => (
            <Button onClick={() => void openDetail(row)} size="small" type="tertiary">
              {t("Details")}
            </Button>
          ),
        },
      ] as any[],
    [language, openDetail, t]
  );

  const tabsArea = (
    <Tabs
      activeKey={statusFilter}
      className="mb-2"
      collapsible
      onChange={(key) => {
        setStatusFilter(String(key) as LotteryStatusFilter);
        setActivePage(1);
      }}
      type="card"
    >
      <Tabs.TabPane itemKey="all" tab={t("All")} />
      {lotteryStatusTabs.map((status) => (
        <Tabs.TabPane
          itemKey={status}
          key={status}
          tab={t(statusMeta[status].labelKey)}
        />
      ))}
    </Tabs>
  );

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          icon={<Gift size={14} />}
          onClick={() => setCreateOpen(true)}
          size="small"
          type="primary"
        >
          {t("Create")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          icon={<RefreshCw size={14} />}
          loading={loading}
          onClick={refresh}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
      </div>
    </div>
  );

  const paginationArea = createCardProPagination({
    currentPage: activePage,
    isMobile,
    onPageChange: setActivePage,
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total,
    t,
  });

  return (
    <div className="console-content-width py-5">
      <CardPro
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
        tabsArea={tabsArea}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={columns}
          dataSource={items}
          empty={
            <Empty
              darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />}
              description={t("No lottery campaigns")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          scroll={{ x: "max(100%, 1530px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <LotteryCreateModal
        onCancel={() => setCreateOpen(false)}
        onCreated={onCreated}
        visible={createOpen}
      />

      <LotteryDetailSheet
        detail={detail}
        entries={entries}
        entryTotal={entryTotal}
        language={language}
        loading={detailLoading}
        onCancel={() => {
          detailRequestRef.current += 1;
          setDetail(null);
          setDetailLoading(false);
        }}
        payouts={payouts}
        payoutTotal={payoutTotal}
        userDirectory={userDirectory}
        t={t}
      />
    </div>
  );
}
