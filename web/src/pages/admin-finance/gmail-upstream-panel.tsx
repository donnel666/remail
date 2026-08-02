import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Button, Card, Pagination, Spin, Table, Tabs, Tag, Toast, Typography } from "@douyinfe/semi-ui";
import { RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  getGmailUpstreamFinance,
  listGmailUpstreamActivations,
  type GmailUpstreamActivation,
  type GmailUpstreamFinanceReport,
} from "@/lib/gmail-upstream-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

const { Text } = Typography;
const pageSize = 20;

function amount(value?: string) {
  const parsed = Number(value ?? 0);
  return Number.isFinite(parsed)
    ? parsed.toFixed(6).replace(/\.?0+$/, "")
    : value ?? "0";
}

function rate(value?: string) {
  const parsed = Number(value ?? 0);
  return Number.isFinite(parsed) ? `${(parsed * 100).toFixed(2)}%` : "-";
}

function time(value?: string | null) {
  if (!value) return "-";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "-" : parsed.toLocaleString();
}

function statusTag(status: GmailUpstreamActivation["status"]) {
  const color = status === "completed"
    ? "green"
    : status === "failed" || status === "unknown"
      ? "red"
      : status === "cancelled"
        ? "grey"
        : "blue";
  return <Tag color={color} shape="circle">{status}</Tag>;
}

export function GmailUpstreamPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const [report, setReport] = useState<GmailUpstreamFinanceReport | null>(null);
  const [activations, setActivations] = useState<GmailUpstreamActivation[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const loadRequestRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    loadRequestRef.current?.abort();
    const controller = new AbortController();
    loadRequestRef.current = controller;
    setLoading(true);
    try {
      const [nextReport, nextActivations] = await Promise.all([
        getGmailUpstreamFinance(controller.signal),
        listGmailUpstreamActivations((page - 1) * pageSize, pageSize, controller.signal),
      ]);
      if (controller.signal.aborted) return;
      setReport(nextReport);
      setActivations(nextActivations.items);
      setTotal(nextActivations.total);
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Gmail upstream finance load failed."));
    } finally {
      if (loadRequestRef.current === controller) {
        loadRequestRef.current = null;
        setLoading(false);
      }
    }
  }, [page, t]);

  useEffect(() => {
    void load();
    return () => loadRequestRef.current?.abort();
  }, [load]);

  const overview = report?.overview;
  const summary = [
    ["销售额", amount(overview?.sales)],
    ["退款额", amount(overview?.refunds)],
    ["净收入", amount(overview?.netRevenue)],
    ["保守成本", amount(overview?.conservativeCost)],
    ["保守毛利", amount(overview?.conservativeProfit)],
    ["保守毛利率", rate(overview?.conservativeMarginRate)],
  ];
  const breakdownColumns = useMemo(() => [
    { title: "名称", dataIndex: "name", render: (value: unknown) => String(value || "-") },
    { title: "订单", dataIndex: "orderCount" },
    { title: "净收入", dataIndex: "netRevenue", render: (value: unknown) => amount(String(value ?? 0)) },
    { title: "成本", dataIndex: "cost", render: (value: unknown) => amount(String(value ?? 0)) },
    { title: "毛利", dataIndex: "profit", render: (value: unknown) => amount(String(value ?? 0)) },
  ], []);

  return (
    <Spin spinning={loading}>
      {tabsArea}
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold text-[var(--semi-color-text-0)]">Gmail 上游经营</h2>
          <Text type="tertiary">收入按订单统计，成本按已结算、预留和未知状态保守计算</Text>
        </div>
        <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
      </div>

      <div className="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-6">
        {summary.map(([label, value]) => (
          <Card bodyStyle={{ padding: 14 }} key={label}>
            <Text type="tertiary">{label}</Text>
            <div className="mt-1 text-xl font-semibold">{value}</div>
          </Card>
        ))}
      </div>

      <Card className="mb-4" title="成本与接码分布">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div><Text type="tertiary">已结算成本</Text><div className="font-semibold">{amount(overview?.settledCost)}</div></div>
          <div><Text type="tertiary">预留成本</Text><div className="font-semibold">{amount(overview?.reservedCost)}</div></div>
          <div><Text type="tertiary">未知成本</Text><div className="font-semibold">{amount(overview?.unknownCost)}</div></div>
          <div><Text type="tertiary">订单 / 激活</Text><div className="font-semibold">{overview?.orderCount ?? 0} / {overview?.activationCount ?? 0}</div></div>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          <Tag shape="circle">0 码 {overview?.zeroCodeCount ?? 0}</Tag>
          <Tag color="blue" shape="circle">1 码 {overview?.oneCodeCount ?? 0}</Tag>
          <Tag color="orange" shape="circle">2 码 {overview?.twoCodeCount ?? 0}</Tag>
          <Tag color="green" shape="circle">3 码 {overview?.threeCodeCount ?? 0}</Tag>
        </div>
      </Card>

      <Card className="mb-4" title="经营拆分">
        <Tabs type="line">
          <Tabs.TabPane itemKey="project" tab="按项目">
            <Table columns={breakdownColumns} dataSource={report?.byProject ?? []} pagination={false} rowKey="key" />
          </Tabs.TabPane>
          <Tabs.TabPane itemKey="service" tab="按上游项目">
            <Table columns={breakdownColumns} dataSource={report?.byService ?? []} pagination={false} rowKey="key" />
          </Tabs.TabPane>
          <Tabs.TabPane itemKey="source" tab="按来源">
            <Table columns={breakdownColumns} dataSource={report?.bySource ?? []} pagination={false} rowKey="key" />
          </Tabs.TabPane>
        </Tabs>
      </Card>

      <Card title="激活记录">
        <Table
          columns={[
            { title: "订单号", dataIndex: "orderNo", width: 180 },
            { title: "项目", dataIndex: "projectName", width: 150 },
            { title: "渠道", dataIndex: "source", width: 100 },
            { title: "上游项目", dataIndex: "providerServiceCode", width: 110 },
            { title: "邮箱", dataIndex: "email", width: 220, render: (value: unknown) => String(value || "等待分配") },
            { title: "状态", dataIndex: "status", width: 120, render: (value: GmailUpstreamActivation["status"]) => statusTag(value) },
            { title: "验证码", dataIndex: "receivedCount", width: 90, render: (value: unknown) => `${Number(value || 0)}/3` },
            { title: "成本", dataIndex: "costPoints", width: 100, render: (value: unknown) => amount(String(value ?? 0)) },
            { title: "有效期", dataIndex: "expiresAt", width: 180, render: (value: unknown) => time(typeof value === "string" ? value : undefined) },
            { title: "创建时间", dataIndex: "createdAt", width: 180, render: (value: unknown) => time(String(value ?? "")) },
            { title: "安全错误", dataIndex: "lastSafeError", width: 220, render: (value: unknown) => String(value || "-") },
          ]}
          dataSource={activations}
          pagination={false}
          rowKey="id"
          scroll={{ x: 1650 }}
        />
        {total > pageSize ? (
          <div className="mt-4 flex justify-end">
            <Pagination currentPage={page} onPageChange={setPage} pageSize={pageSize} total={total} />
          </div>
        ) : null}
      </Card>
    </Spin>
  );
}
